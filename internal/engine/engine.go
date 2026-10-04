// Package engine implements `forge dep build` and `forge dep update`.
package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

	Deps        []Dep // one per dependency, in order; empty if the run stopped before resolving
	LockWritten bool
}

// Status is what happened to one dependency.
type Status string

const (
	StatusCached     Status = "cached"     // served from the cache without any request
	StatusDownloaded Status = "downloaded" // needed at least one registry request
	StatusFailed     Status = "failed"
	StatusSkipped    Status = "skipped" // not attempted because the run stopped earlier
)

// Dep is the outcome for one dependency.
type Dep struct {
	Name       string
	Alias      string
	Version    string // locked version; empty if resolving stopped the run
	Constraint string // as written in Chart.yaml
	Repository string
	Status     Status
	Digest     string
	Size       int64
	Placement  materialize.Method // empty unless charts/ was updated
	Duration   time.Duration
	Problem    *registry.Problem // set when Status is failed
}

// Failure is one dependency that could not be fetched.
type Failure struct {
	Name, Version, Repository, Reason string
	Problem                           registry.Problem
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

// UnsupportedError lists dependencies whose repository is not oci://.
type UnsupportedError struct{ Deps []*chart.Dependency }

func (e *UnsupportedError) Error() string {
	bad := make([]string, len(e.Deps))
	for i, d := range e.Deps {
		bad[i] = fmt.Sprintf("%q (repository %q)", d.Name, d.Repository)
	}
	return fmt.Sprintf("forge supports only oci:// dependencies; unsupported: %s", strings.Join(bad, ", "))
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
		return unsupportedSummary(c.Dependencies, err), err
	}
	if err := c.CheckLock(); err != nil {
		return Summary{}, err
	}
	return install(ctx, o, c.Lock.Dependencies, c.Dependencies)
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
		return unsupportedSummary(c.Dependencies, err), err
	}
	locked, err := resolve.Resolve(ctx, o.Registry, c.Dependencies)
	if err != nil {
		return noMatchSummary(c.Dependencies, err), err
	}
	sum, err := install(ctx, o, locked, c.Dependencies)
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
	if err := chartmeta.WriteLock(c.Dir, &chart.Lock{Generated: time.Now(), Digest: digest, Dependencies: locked}); err != nil {
		return sum, err
	}
	sum.LockWritten = true
	return sum, nil
}

func checkSupported(deps []*chart.Dependency) error {
	var bad []*chart.Dependency
	for _, d := range deps {
		if !strings.HasPrefix(d.Repository, "oci://") {
			bad = append(bad, d)
		}
	}
	if len(bad) > 0 {
		return &UnsupportedError{Deps: bad}
	}
	return nil
}

// specDep describes a Chart.yaml dependency that was never fetched.
func specDep(d *chart.Dependency) Dep {
	return Dep{Name: d.Name, Alias: d.Alias, Constraint: d.Version, Repository: d.Repository, Status: StatusSkipped}
}

// unsupportedSummary marks non-oci:// dependencies failed and the rest skipped.
func unsupportedSummary(deps []*chart.Dependency, err error) Summary {
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		return Summary{}
	}
	sum := Summary{Deps: make([]Dep, len(deps))}
	for i, d := range deps {
		sum.Deps[i] = specDep(d)
		if slices.Contains(ue.Deps, d) {
			sum.Deps[i].Status = StatusFailed
			sum.Deps[i].Problem = &registry.Problem{
				Code:    registry.CodeUnsupportedRepository,
				Message: fmt.Sprintf("repository %q is not an oci:// URL", d.Repository),
				Hint:    "forge supports only oci:// dependencies",
			}
		}
	}
	return sum
}

// noMatchSummary marks dependencies without a matching version failed and
// the rest skipped. Other resolve errors carry no per-dependency detail.
func noMatchSummary(deps []*chart.Dependency, err error) Summary {
	var nm *resolve.NoMatchError
	if !errors.As(err, &nm) {
		return Summary{}
	}
	sum := Summary{Deps: make([]Dep, len(deps))}
	for i, d := range deps {
		sum.Deps[i] = specDep(d)
	}
	for _, m := range nm.Missing {
		sum.Deps[m.Index].Status = StatusFailed
		sum.Deps[m.Index].Problem = &registry.Problem{
			Code:    registry.CodeNoMatchingVersion,
			Message: fmt.Sprintf("no version of %s matches %q", m.Name, m.Constraint),
			Hint:    "available versions: " + m.Nearest(),
		}
	}
	return sum
}

// install fetches every dependency, then replaces charts/ only if all
// succeeded. specs are the Chart.yaml entries deps were locked from; they
// supply aliases and constraints when they line up with deps.
func install(ctx context.Context, o Options, deps, specs []*chart.Dependency) (Summary, error) {
	items := make([]fetch.Item, len(deps))
	for i, d := range deps {
		items[i] = fetch.Item{Name: d.Name, Version: d.Version, Repository: d.Repository}
	}
	f := &fetch.Fetcher{Registry: o.Registry, Store: o.Store, Refresh: o.Refresh}
	results := f.Fetch(ctx, items)
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	sum := Summary{Charts: len(results), Deps: make([]Dep, len(results))}
	aligned := len(specs) == len(deps)
	var fails []Failure
	for i, r := range results {
		d := Dep{Name: r.Item.Name, Version: r.Item.Version, Repository: r.Item.Repository,
			Digest: r.Digest.String(), Size: r.Size, Duration: r.Duration}
		if aligned && specs[i].Name == d.Name {
			d.Alias, d.Constraint = specs[i].Alias, specs[i].Version
		}
		switch {
		case r.Err != nil:
			p := problem(r)
			d.Status, d.Problem = StatusFailed, &p
			fails = append(fails, Failure{Name: r.Item.Name, Version: r.Item.Version, Repository: r.Item.Repository, Reason: reason(r), Problem: p})
		case r.Cached:
			d.Status = StatusCached
			sum.Cached++
		default:
			d.Status = StatusDownloaded
			sum.Downloaded++
		}
		sum.Deps[i] = d
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
	for i, r := range results {
		sum.Deps[i].Placement = stage.Placement(r.Item.FileName())
	}
	return sum, nil
}

const digestMismatchReason = "downloaded content did not match its digest twice; possible corruption or a proxy rewriting responses"

func reason(r fetch.Result) string {
	if errors.Is(r.Err, store.ErrDigestMismatch) {
		return digestMismatchReason
	}
	return registry.Explain(r.Err, itemHost(r.Item))
}

func problem(r fetch.Result) registry.Problem {
	if errors.Is(r.Err, store.ErrDigestMismatch) {
		return registry.Problem{Code: registry.CodeDigestMismatch, Message: digestMismatchReason,
			Hint: "retry; if it persists, check for a proxy rewriting registry responses"}
	}
	return registry.Classify(r.Err, itemHost(r.Item))
}

func itemHost(it fetch.Item) string {
	if repo, err := registry.RepoRef(it.Repository, it.Name); err == nil {
		return registry.Host(repo)
	}
	return ""
}
