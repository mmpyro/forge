// Package resolve turns Chart.yaml version constraints into exact versions
// the way Helm's resolver does for OCI repositories.
package resolve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	chart "helm.sh/helm/v4/pkg/chart/v2"

	"github.com/mmarszalek/helm-forge/internal/par"
	"github.com/mmarszalek/helm-forge/internal/registry"
)

// TagLister lists a repository's versions, highest first (registry.Client.Tags).
type TagLister interface {
	Tags(ctx context.Context, repo string) ([]string, error)
}

// Resolve returns one lock entry per dependency, in order. Exact versions are
// locked as written without asking the registry, like Helm; ranges pick the
// highest matching tag, listing each repository once.
func Resolve(ctx context.Context, tl TagLister, deps []*chart.Dependency) ([]*chart.Dependency, error) {
	constraints := make([]*semver.Constraints, len(deps))
	repoOf := make([]string, len(deps)) // empty for exact versions
	var rangeRepos []string
	for i, d := range deps {
		c, err := semver.NewConstraint(d.Version)
		if err != nil {
			return nil, fmt.Errorf("dependency %q has an invalid version/constraint format: %w", d.Name, err)
		}
		constraints[i] = c
		if _, err := semver.NewVersion(d.Version); err == nil {
			continue
		}
		repo, err := registry.RepoRef(d.Repository, d.Name)
		if err != nil {
			return nil, err
		}
		repoOf[i] = repo
		if !slices.Contains(rangeRepos, repo) {
			rangeRepos = append(rangeRepos, repo)
		}
	}

	tags, err := listTags(ctx, tl, rangeRepos)
	if err != nil {
		return nil, err
	}

	locked := make([]*chart.Dependency, len(deps))
	var missing []Missing
	for i, d := range deps {
		locked[i] = &chart.Dependency{Name: d.Name, Repository: d.Repository, Version: d.Version}
		if repoOf[i] == "" {
			continue
		}
		found := false
		for _, t := range tags[repoOf[i]] {
			if v, err := semver.NewVersion(t); err == nil && constraints[i].Check(v) {
				locked[i].Version = v.Original()
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, Missing{Index: i, Name: d.Name, Repository: d.Repository, Constraint: d.Version, Available: tags[repoOf[i]]})
		}
	}
	if len(missing) > 0 {
		return nil, &NoMatchError{Missing: missing}
	}
	return locked, nil
}

// Missing is a dependency whose constraint no tag satisfies.
type Missing struct {
	Index      int // position in the deps passed to Resolve
	Name       string
	Repository string
	Constraint string
	Available  []string // highest first
}

// Nearest lists up to five of the highest available versions, or "none".
func (m Missing) Nearest() string { return nearest(m.Available) }

// NoMatchError lists every dependency without a matching version.
type NoMatchError struct{ Missing []Missing }

func (e *NoMatchError) Error() string {
	names := make([]string, len(e.Missing))
	hints := make([]string, len(e.Missing))
	for i, m := range e.Missing {
		names[i] = fmt.Sprintf("%q (repository %q, version %q)", m.Name, m.Repository, m.Constraint)
		hints[i] = fmt.Sprintf("available versions of %s: %s", m.Name, m.Nearest())
	}
	return fmt.Sprintf("can't get a valid version for %d subchart(s): %s. Make sure a matching chart version exists in the repo, or change the version constraint in Chart.yaml\n  %s",
		len(e.Missing), strings.Join(names, ", "), strings.Join(hints, "\n  "))
}

// TagsError is a failure to list a repository's tags.
type TagsError struct {
	Repo string
	Err  error
}

func (e *TagsError) Error() string {
	return fmt.Sprintf("could not retrieve list of tags for repository %s: %v", e.Repo, e.Err)
}

func (e *TagsError) Unwrap() error { return e.Err }

func listTags(ctx context.Context, tl TagLister, repos []string) (map[string][]string, error) {
	var (
		mu       sync.Mutex
		out      = map[string][]string{}
		firstErr error
	)
	ctx = registry.WithPullScopes(ctx, repos)
	par.ByHost(ctx, repos, registry.Host, func(ctx context.Context, repo string) {
		tags, err := tl.Tags(ctx, repo)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = &TagsError{Repo: repo, Err: err}
			}
			return
		}
		out[repo] = tags
	})
	return out, firstErr
}

func nearest(tags []string) string {
	if len(tags) == 0 {
		return "none"
	}
	return strings.Join(tags[:min(5, len(tags))], ", ")
}
