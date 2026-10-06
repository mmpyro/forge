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
	"slices"
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

	Deps        []Dep // one per dependency, in order; empty if the run stopped before resolving
	LockWritten bool
}

// Status is what happened to one dependency.
type Status string

const (
	StatusCached     Status = "cached"     // served from the cache without any request
	StatusDownloaded Status = "downloaded" // needed at least one request
	StatusLocal      Status = "local"      // packaged from a file:// directory
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
	Digest     string // empty for file:// dependencies
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

const supportedRepositories = "forge supports oci://, http(s)://, file:// and @alias repositories"

// UnsupportedError lists dependencies whose repository forge cannot fetch from.
type UnsupportedError struct{ Deps []*chart.Dependency }

func (e *UnsupportedError) Error() string {
	bad := make([]string, len(e.Deps))
	for i, d := range e.Deps {
		if d.Repository == "" {
			bad[i] = fmt.Sprintf("%q (no repository: charts/ subdirectory)", d.Name)
		} else {
			bad[i] = fmt.Sprintf("%q (repository %q)", d.Name, d.Repository)
		}
	}
	return fmt.Sprintf("%s; unsupported: %s", supportedRepositories, strings.Join(bad, ", "))
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
	deps, err := prepare(c.Dependencies, o)
	if err != nil {
		return unsupportedSummary(c.Dependencies, err), err
	}
	c.Dependencies = deps
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
	deps, err := prepare(c.Dependencies, o)
	if err != nil {
		return unsupportedSummary(c.Dependencies, err), err
	}
	locked, err := resolve.Resolve(ctx, resolve.Sources{Tags: o.Registry, Index: o.Repos, ChartDir: c.Dir}, deps)
	if err != nil {
		return noMatchSummary(deps, err), err
	}
	sum, err := install(ctx, o, locked, deps)
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
	if err := chartmeta.WriteLock(c.Dir, &chart.Lock{Generated: time.Now(), Digest: digest, Dependencies: locked}); err != nil {
		return sum, err
	}
	sum.LockWritten = true
	return sum, nil
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
	var unsupported []*chart.Dependency
	var undefined []string
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
			unsupported = append(unsupported, d)
		default:
			if u, err := url.ParseRequestURI(r); err == nil && u.Scheme != "" {
				unsupported = append(unsupported, d)
				continue
			}
			undefined = append(undefined, r)
		}
	}
	if len(undefined) > 0 {
		return nil, &repoconfig.NotDefinedError{Repositories: undefined}
	}
	if len(unsupported) > 0 {
		return nil, &UnsupportedError{Deps: unsupported}
	}
	return out, nil
}

// specDep describes a Chart.yaml dependency that was never fetched.
func specDep(d *chart.Dependency) Dep {
	return Dep{Name: d.Name, Alias: d.Alias, Constraint: d.Version, Repository: d.Repository, Status: StatusSkipped}
}

// unsupportedSummary marks dependencies with an unsupported repository
// failed and the rest skipped.
func unsupportedSummary(deps []*chart.Dependency, err error) Summary {
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		return Summary{}
	}
	sum := Summary{Deps: make([]Dep, len(deps))}
	for i, d := range deps {
		sum.Deps[i] = specDep(d)
		if slices.Contains(ue.Deps, d) {
			msg := fmt.Sprintf("repository %q is not supported", d.Repository)
			if d.Repository == "" {
				msg = "no repository: charts/ subdirectory"
			}
			sum.Deps[i].Status = StatusFailed
			sum.Deps[i].Problem = &registry.Problem{
				Code:    registry.CodeUnsupportedRepository,
				Message: msg,
				Hint:    supportedRepositories,
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
		hint := "available versions: " + m.Nearest()
		if m.Local {
			hint = "local version: " + m.Nearest()
		}
		sum.Deps[m.Index].Status = StatusFailed
		sum.Deps[m.Index].Problem = &registry.Problem{
			Code:    registry.CodeNoMatchingVersion,
			Message: fmt.Sprintf("no version of %s matches %q", m.Name, m.Constraint),
			Hint:    hint,
		}
	}
	return sum
}

// install fetches every dependency, then replaces charts/ only if all
// succeeded. specs are the Chart.yaml entries deps were locked from; they
// supply aliases and constraints when they line up with deps.
func install(ctx context.Context, o Options, deps, specs []*chart.Dependency) (Summary, error) {
	var items []fetch.Item
	var itemIdx, localIdx []int // positions in deps
	for i, d := range deps {
		if strings.HasPrefix(d.Repository, "file://") {
			localIdx = append(localIdx, i)
			continue
		}
		items = append(items, fetch.Item{Name: d.Name, Version: d.Version, Repository: d.Repository})
		itemIdx = append(itemIdx, i)
	}
	f := &fetch.Fetcher{Registry: o.Registry, Repos: o.Repos, Store: o.Store, Refresh: o.Refresh}
	results := f.Fetch(ctx, items)
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	sum := Summary{Charts: len(deps), Deps: make([]Dep, len(deps))}
	aligned := len(specs) == len(deps)
	for i, d := range deps {
		sum.Deps[i] = Dep{Name: d.Name, Version: d.Version, Repository: d.Repository}
		if aligned && specs[i].Name == d.Name {
			sum.Deps[i].Alias, sum.Deps[i].Constraint = specs[i].Alias, specs[i].Version
		}
	}
	var fails []Failure
	for k, r := range results {
		d := &sum.Deps[itemIdx[k]]
		d.Digest, d.Size, d.Duration = r.Digest.String(), r.Size, r.Duration
		switch {
		case r.Err != nil:
			p := problem(r)
			d.Status, d.Problem = StatusFailed, &p
			fails = append(fails, Failure{Name: r.Item.Name, Version: r.Item.Version, Repository: r.Item.Repository, Reason: reason(p), Problem: p})
		case r.Cached:
			d.Status = StatusCached
			sum.Cached++
		default:
			d.Status = StatusDownloaded
			sum.Downloaded++
		}
	}

	archives := make([]string, len(localIdx))
	if len(localIdx) > 0 {
		tmp, err := os.MkdirTemp(o.Store.TempDir(), "local-*")
		if err != nil {
			return sum, err
		}
		defer os.RemoveAll(tmp)
		for k, i := range localIdx {
			dep, d := deps[i], &sum.Deps[i]
			start := time.Now()
			p, err := packageLocal(dep, o.ChartDir, tmp)
			d.Duration = time.Since(start)
			if err != nil {
				pr := registry.Problem{Code: registry.CodeUnknown, Message: err.Error()}
				d.Status, d.Problem = StatusFailed, &pr
				fails = append(fails, Failure{Name: dep.Name, Version: dep.Version, Repository: dep.Repository, Reason: err.Error(), Problem: pr})
				continue
			}
			if fi, err := os.Stat(p); err == nil {
				d.Size = fi.Size()
			}
			d.Status = StatusLocal
			archives[k] = p
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
	names := make([]string, len(deps))
	add := func(i int, src, name string) error {
		names[i] = name
		if err := stage.Add(src, name); err != nil {
			_ = stage.Abort()
			return err
		}
		return nil
	}
	for k, r := range results {
		if err := add(itemIdx[k], r.BlobPath, r.FileName); err != nil {
			return sum, err
		}
	}
	for k, p := range archives {
		if err := add(localIdx[k], p, filepath.Base(p)); err != nil {
			return sum, err
		}
	}
	if err := stage.Commit(); err != nil {
		_ = stage.Abort()
		return sum, err
	}
	for i, name := range names {
		sum.Deps[i].Placement = stage.Placement(name)
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

const digestMismatchReason = "downloaded content did not match its digest twice; possible corruption or a proxy rewriting responses"

// reason is the one-line text form of p for the DependencyError message.
func reason(p registry.Problem) string {
	if p.Code == registry.CodeUnauthorized || p.Code == registry.CodeForbidden {
		return p.Message + "; " + p.Hint
	}
	return p.Message
}

func problem(r fetch.Result) registry.Problem {
	if errors.Is(r.Err, store.ErrDigestMismatch) {
		return registry.Problem{Code: registry.CodeDigestMismatch, Message: digestMismatchReason,
			Hint: "retry; if it persists, check for a proxy rewriting registry responses"}
	}
	if strings.HasPrefix(r.Item.Repository, "oci://") {
		return registry.Classify(r.Err, ociHost(r.Item))
	}
	return repoProblem(r.Err, r.Item.Repository)
}

// repoProblem classifies a chart repository error.
func repoProblem(err error, repoURL string) registry.Problem {
	var se *chartrepo.StatusError
	if errors.As(err, &se) {
		switch se.StatusCode {
		case http.StatusUnauthorized:
			return registry.Problem{Code: registry.CodeUnauthorized, HTTPStatus: se.StatusCode, Message: "401 unauthorized",
				Hint: fmt.Sprintf("add credentials with 'helm repo add <name> %s --username …'", repoURL)}
		case http.StatusForbidden:
			return registry.Problem{Code: registry.CodeForbidden, HTTPStatus: se.StatusCode, Message: "403 forbidden",
				Hint: "check your permissions on this repository"}
		case http.StatusNotFound:
			return registry.Problem{Code: registry.CodeNotFound, HTTPStatus: se.StatusCode, Message: "404 not found: " + se.URL,
				Hint: "Fix the version, or run 'forge dep update'"}
		}
		return registry.Problem{Code: registry.CodeUnknown, HTTPStatus: se.StatusCode, Message: se.Error()}
	}
	var nf *chartrepo.NotFoundError
	switch {
	case errors.As(err, &nf):
		return registry.Problem{Code: registry.CodeNotFound, Message: err.Error(), Hint: "Fix the version, or run 'forge dep update'"}
	case errors.Is(err, context.DeadlineExceeded):
		return registry.Problem{Code: registry.CodeTimeout, Message: "timed out"}
	}
	return registry.Problem{Code: registry.CodeUnknown, Message: err.Error()}
}

func ociHost(it fetch.Item) string {
	if repo, err := registry.RepoRef(it.Repository, it.Name); err == nil {
		return registry.Host(repo)
	}
	return ""
}
