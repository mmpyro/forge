// Package engine implements `forge dep build` and `forge dep update`.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/fetch"
	"github.com/mmarszalek/helm-forge/internal/lockdigest"
	"github.com/mmarszalek/helm-forge/internal/materialize"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
	"github.com/mmarszalek/helm-forge/internal/resolve"
	"github.com/mmarszalek/helm-forge/internal/store"
)

// Registry is everything the engine needs from an OCI registry client.
type Registry interface {
	resolve.TagLister
	fetch.Registry
}

// ChartRepos is everything the engine needs from a chart repository client.
type ChartRepos interface {
	resolve.VersionLister
	fetch.ChartRepos
}

var (
	_ Registry   = (*registry.Client)(nil)
	_ ChartRepos = (*chartrepo.Client)(nil)
)

// Options configures one run.
type Options struct {
	ChartDir   string
	Registry   Registry
	Repos      ChartRepos
	RepoConfig *repoconfig.Config // resolves @name repositories; nil means none
	Store      *store.Store
	Refresh    bool
}

// Summary counts what a run did. Aliases of one chart count separately.
type Summary struct {
	Charts     int
	Cached     int
	Downloaded int
	Local      int // packaged from file:// directories
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
	if c.Dependencies, err = prepare(c.Dependencies, o); err != nil {
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
	deps, err := prepare(c.Dependencies, o)
	if err != nil {
		return Summary{}, err
	}
	locked, err := resolve.Resolve(ctx, resolve.Sources{Tags: o.Registry, Index: o.Repos, ChartDir: c.Dir}, deps)
	if err != nil {
		return Summary{}, err
	}
	sum, err := install(ctx, o, locked)
	if err != nil {
		return sum, err
	}
	digest, err := lockdigest.Compute(deps, locked)
	if err != nil {
		return sum, err
	}
	if c.Lock != nil && c.Lock.Digest == digest {
		return sum, nil
	}
	return sum, chartmeta.WriteLock(c.Dir, &chart.Lock{Generated: time.Now(), Digest: digest, Dependencies: locked})
}

// prepare checks every repository before any network call and returns
// copies of deps with @name repositories replaced by their URLs, which is
// what Helm hashes and locks.
func prepare(deps []*chart.Dependency, o Options) ([]*chart.Dependency, error) {
	cfg := o.RepoConfig
	if cfg == nil {
		cfg = &repoconfig.Config{}
	}
	out := make([]*chart.Dependency, len(deps))
	var unsupported, undefined []string
	for i, d := range deps {
		cp := *d
		out[i] = &cp
		switch r := d.Repository; {
		case strings.HasPrefix(r, "oci://"):
		case strings.HasPrefix(r, "file://"):
			if _, err := resolve.LocalPath(r, o.ChartDir); err != nil {
				return nil, err
			}
		case repoconfig.IsAlias(r):
			u, _, ok := cfg.Resolve(r)
			if !ok {
				undefined = append(undefined, r)
				continue
			}
			cp.Repository = u
		case chartrepo.IsRepoURL(r):
		case r == "":
			unsupported = append(unsupported, fmt.Sprintf("%q (no repository: charts/ subdirectory)", d.Name))
		default:
			if u, err := url.ParseRequestURI(r); err == nil && u.Scheme != "" {
				unsupported = append(unsupported, fmt.Sprintf("%q (repository %q)", d.Name, r))
				continue
			}
			undefined = append(undefined, r)
		}
	}
	if len(undefined) > 0 {
		return nil, &repoconfig.NotDefinedError{Repositories: undefined}
	}
	if len(unsupported) > 0 {
		return nil, fmt.Errorf("forge supports oci://, http(s)://, file:// and @alias repositories; unsupported: %s", strings.Join(unsupported, ", "))
	}
	return out, nil
}

// install fetches every dependency, then replaces charts/ only if all succeeded.
func install(ctx context.Context, o Options, deps []*chart.Dependency) (Summary, error) {
	var items []fetch.Item
	var local []*chart.Dependency
	for _, d := range deps {
		if strings.HasPrefix(d.Repository, "file://") {
			local = append(local, d)
			continue
		}
		items = append(items, fetch.Item{Name: d.Name, Version: d.Version, Repository: d.Repository})
	}
	f := &fetch.Fetcher{Registry: o.Registry, Repos: o.Repos, Store: o.Store, Refresh: o.Refresh}
	results := f.Fetch(ctx, items)
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	sum := Summary{Charts: len(deps)}
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

	var archives []string
	if len(local) > 0 {
		tmp, err := os.MkdirTemp(o.Store.TempDir(), "local-*")
		if err != nil {
			return sum, err
		}
		defer os.RemoveAll(tmp)
		for _, d := range local {
			p, err := packageLocal(d, o.ChartDir, tmp)
			if err != nil {
				fails = append(fails, Failure{Name: d.Name, Version: d.Version, Repository: d.Repository, Reason: err.Error()})
				continue
			}
			archives = append(archives, p)
			sum.Local++
		}
	}
	if len(fails) > 0 {
		return sum, &DependencyError{Failures: fails}
	}

	stage, err := materialize.NewStage(o.ChartDir)
	if err != nil {
		return sum, err
	}
	add := func(src, name string) error {
		if err := stage.Add(src, name); err != nil {
			_ = stage.Abort()
			return err
		}
		return nil
	}
	for _, r := range results {
		if err := add(r.BlobPath, r.FileName); err != nil {
			return sum, err
		}
	}
	for _, p := range archives {
		if err := add(p, filepath.Base(p)); err != nil {
			return sum, err
		}
	}
	if err := stage.Commit(); err != nil {
		_ = stage.Abort()
		return sum, err
	}
	return sum, nil
}

// packageLocal archives a file:// dependency into dir the way Helm does, if
// the local chart still has the locked version, and returns the archive.
func packageLocal(d *chart.Dependency, chartDir, dir string) (string, error) {
	ch, err := resolve.LoadLocal(d.Repository, chartDir)
	if err != nil {
		return "", err
	}
	c, err := semver.NewConstraint(d.Version)
	if err != nil {
		return "", fmt.Errorf("dependency %s has an invalid version/constraint format: %w", d.Name, err)
	}
	v, err := semver.NewVersion(ch.Metadata.Version)
	if err != nil {
		return "", err
	}
	if !c.Check(v) {
		return "", fmt.Errorf("can't get a valid version for dependency %s: local chart is version %s; run 'forge dep update'", d.Name, ch.Metadata.Version)
	}
	return chartutil.Save(ch, dir)
}

func reason(r fetch.Result) string {
	if errors.Is(r.Err, store.ErrDigestMismatch) {
		return "downloaded content did not match its digest twice; possible corruption or a proxy rewriting responses"
	}
	var se *chartrepo.StatusError
	if errors.As(r.Err, &se) {
		switch se.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Sprintf("401 unauthorized; add credentials with 'helm repo add <name> %s --username …'", r.Item.Repository)
		case http.StatusForbidden:
			return "403 forbidden; check your permissions on this repository"
		case http.StatusNotFound:
			return "404 not found: " + se.URL
		}
		return se.Error()
	}
	if !strings.HasPrefix(r.Item.Repository, "oci://") {
		if errors.Is(r.Err, context.DeadlineExceeded) {
			return "timed out"
		}
		return r.Err.Error()
	}
	host := ""
	if repo, err := registry.RepoRef(r.Item.Repository, r.Item.Name); err == nil {
		host = registry.Host(repo)
	}
	return registry.Explain(r.Err, host)
}
