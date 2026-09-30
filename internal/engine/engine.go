// Package engine implements `forge dep build` and `forge dep update`.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	chart "helm.sh/helm/v4/pkg/chart/v2"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/fetch"
	"github.com/mmarszalek/helm-forge/internal/lockdigest"
	"github.com/mmarszalek/helm-forge/internal/materialize"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/resolve"
	"github.com/mmarszalek/helm-forge/internal/store"
)

// Registry is everything the engine needs from a registry client.
type Registry interface {
	resolve.TagLister
	fetch.Registry
}

var _ Registry = (*registry.Client)(nil)

// Options configures one run.
type Options struct {
	ChartDir string
	Registry Registry
	Store    *store.Store
	Refresh  bool
}

// Summary counts what a run did. Aliases of one chart count separately.
type Summary struct {
	Charts     int
	Cached     int
	Downloaded int
}

// Failure is one dependency that could not be fetched.
type Failure struct {
	Name, Version, Repository, Reason string
}

// DependencyError lists every dependency that failed in a run.
type DependencyError struct{ Failures []Failure }

func (e *DependencyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d dependencies failed:", len(e.Failures))
	for _, f := range e.Failures {
		fmt.Fprintf(&b, "\n  ✗ %s %s (%s): %s", f.Name, f.Version, f.Repository, f.Reason)
	}
	return b.String()
}

// Build installs the versions pinned in Chart.lock, or runs Update when
// there is no lock, as Helm does.
func Build(ctx context.Context, o Options) (Summary, error) {
	c, err := chartmeta.Load(o.ChartDir)
	if err != nil {
		return Summary{}, err
	}
	if c.Lock == nil {
		return update(ctx, o, c)
	}
	if err := checkSupported(c.Dependencies); err != nil {
		return Summary{}, err
	}
	if err := c.CheckLock(); err != nil {
		return Summary{}, err
	}
	return install(ctx, o, c.Lock.Dependencies)
}

// Update resolves Chart.yaml's constraints, installs the result and writes
// Chart.lock when its digest changed.
func Update(ctx context.Context, o Options) (Summary, error) {
	c, err := chartmeta.Load(o.ChartDir)
	if err != nil {
		return Summary{}, err
	}
	return update(ctx, o, c)
}

func update(ctx context.Context, o Options, c *chartmeta.Chart) (Summary, error) {
	if len(c.Dependencies) == 0 {
		return Summary{}, nil
	}
	if err := checkSupported(c.Dependencies); err != nil {
		return Summary{}, err
	}
	locked, err := resolve.Resolve(ctx, o.Registry, c.Dependencies)
	if err != nil {
		return Summary{}, err
	}
	sum, err := install(ctx, o, locked)
	if err != nil {
		return sum, err
	}
	digest, err := lockdigest.Compute(c.Dependencies, locked)
	if err != nil {
		return sum, err
	}
	if c.Lock != nil && c.Lock.Digest == digest {
		return sum, nil
	}
	return sum, chartmeta.WriteLock(c.Dir, &chart.Lock{Generated: time.Now(), Digest: digest, Dependencies: locked})
}

func checkSupported(deps []*chart.Dependency) error {
	var bad []string
	for _, d := range deps {
		if !strings.HasPrefix(d.Repository, "oci://") {
			bad = append(bad, fmt.Sprintf("%q (repository %q)", d.Name, d.Repository))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("forge supports only oci:// dependencies; unsupported: %s", strings.Join(bad, ", "))
	}
	return nil
}

// install fetches every dependency, then replaces charts/ only if all succeeded.
func install(ctx context.Context, o Options, deps []*chart.Dependency) (Summary, error) {
	items := make([]fetch.Item, len(deps))
	for i, d := range deps {
		items[i] = fetch.Item{Name: d.Name, Version: d.Version, Repository: d.Repository}
	}
	f := &fetch.Fetcher{Registry: o.Registry, Store: o.Store, Refresh: o.Refresh}
	results := f.Fetch(ctx, items)
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	sum := Summary{Charts: len(results)}
	var fails []Failure
	for _, r := range results {
		switch {
		case r.Err != nil:
			fails = append(fails, Failure{Name: r.Item.Name, Version: r.Item.Version, Repository: r.Item.Repository, Reason: reason(r)})
		case r.Cached:
			sum.Cached++
		default:
			sum.Downloaded++
		}
	}
	if len(fails) > 0 {
		return sum, &DependencyError{Failures: fails}
	}

	stage, err := materialize.NewStage(o.ChartDir)
	if err != nil {
		return sum, err
	}
	for _, r := range results {
		if err := stage.Add(r.BlobPath, r.Item.FileName()); err != nil {
			_ = stage.Abort()
			return sum, err
		}
	}
	if err := stage.Commit(); err != nil {
		_ = stage.Abort()
		return sum, err
	}
	return sum, nil
}

func reason(r fetch.Result) string {
	if errors.Is(r.Err, store.ErrDigestMismatch) {
		return "downloaded content did not match its digest twice; possible corruption or a proxy rewriting responses"
	}
	host := ""
	if repo, err := registry.RepoRef(r.Item.Repository, r.Item.Name); err == nil {
		host = registry.Host(repo)
	}
	return registry.Explain(r.Err, host)
}
