// Package resolve turns Chart.yaml version constraints into exact versions
// the way Helm's resolver does, for OCI registries, chart repositories and
// local file:// charts.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"

	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/par"
	"github.com/mmarszalek/helm-forge/internal/registry"
)

// TagLister lists an OCI repository's versions, highest first (registry.Client.Tags).
type TagLister interface {
	Tags(ctx context.Context, repo string) ([]string, error)
}

// VersionLister lists a chart's versions in a chart repository's index,
// highest first (chartrepo.Client.Versions).
type VersionLister interface {
	Versions(ctx context.Context, repoURL, name string) ([]string, error)
}

// Sources is where Resolve looks versions up.
type Sources struct {
	Tags     TagLister
	Index    VersionLister
	ChartDir string // base of relative file:// paths
}

// list is one network lookup: an OCI repository's tags, or one chart's
// versions in a chart repository.
type list struct {
	oci     string // OCI repository reference, or empty
	repoURL string // chart repository URL
	name    string
}

func (l list) host() string {
	if l.oci != "" {
		return registry.Host(l.oci)
	}
	return chartrepo.Host(l.repoURL)
}

// Resolve returns one lock entry per dependency, in order, like Helm:
//   - oci:// exact versions are locked as written without a request; ranges
//     pick the highest matching tag, listing each repository once;
//   - chart repository versions always come from the index (downloaded once
//     per repository), so the lock holds the index's spelling;
//   - file:// charts lock the local chart's version if it satisfies the
//     constraint.
func Resolve(ctx context.Context, s Sources, deps []*chart.Dependency) ([]*chart.Dependency, error) {
	constraints := make([]*semver.Constraints, len(deps))
	lookups := make([]*list, len(deps)) // nil: no lookup needed
	locked := make([]*chart.Dependency, len(deps))
	var lists []list
	var missing, hints []string
	for i, d := range deps {
		c, err := semver.NewConstraint(d.Version)
		if err != nil {
			return nil, fmt.Errorf("dependency %q has an invalid version/constraint format: %w", d.Name, err)
		}
		constraints[i] = c
		locked[i] = &chart.Dependency{Name: d.Name, Repository: d.Repository, Version: d.Version}

		var l list
		switch {
		case strings.HasPrefix(d.Repository, "file://"):
			ch, err := LoadLocal(d.Repository, s.ChartDir)
			if err != nil {
				return nil, err
			}
			v, err := semver.NewVersion(ch.Metadata.Version)
			if err != nil {
				return nil, fmt.Errorf("dependency %q: local chart has an invalid version %q: %w", d.Name, ch.Metadata.Version, err)
			}
			if !c.Check(v) {
				missing = append(missing, fmt.Sprintf("%q (repository %q, version %q)", d.Name, d.Repository, d.Version))
				hints = append(hints, fmt.Sprintf("local version of %s: %s", d.Name, ch.Metadata.Version))
				continue
			}
			locked[i].Version = ch.Metadata.Version
			continue
		case strings.HasPrefix(d.Repository, "oci://"):
			if _, err := semver.NewVersion(d.Version); err == nil {
				continue
			}
			repo, err := registry.RepoRef(d.Repository, d.Name)
			if err != nil {
				return nil, err
			}
			l = list{oci: repo}
		default:
			l = list{repoURL: d.Repository, name: d.Name}
		}
		if !slices.Contains(lists, l) {
			lists = append(lists, l)
		}
		lookups[i] = &l
	}

	versions, err := listAll(ctx, s, lists)
	if err != nil {
		return nil, err
	}

	for i, d := range deps {
		if lookups[i] == nil {
			continue
		}
		vs := versions[*lookups[i]]
		found := false
		for _, t := range vs {
			if v, err := semver.NewVersion(t); err == nil && constraints[i].Check(v) {
				locked[i].Version = v.Original()
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, fmt.Sprintf("%q (repository %q, version %q)", d.Name, d.Repository, d.Version))
			hints = append(hints, fmt.Sprintf("available versions of %s: %s", d.Name, nearest(vs)))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("can't get a valid version for %d subchart(s): %s. Make sure a matching chart version exists in the repo, or change the version constraint in Chart.yaml\n  %s",
			len(missing), strings.Join(missing, ", "), strings.Join(hints, "\n  "))
	}
	return locked, nil
}

func listAll(ctx context.Context, s Sources, lists []list) (map[list][]string, error) {
	var (
		mu       sync.Mutex
		out      = map[list][]string{}
		firstErr error
		ociRepos []string
	)
	for _, l := range lists {
		if l.oci != "" {
			ociRepos = append(ociRepos, l.oci)
		}
	}
	ctx = registry.WithPullScopes(ctx, ociRepos)
	par.ByHost(ctx, lists, list.host, func(ctx context.Context, l list) {
		var (
			vs  []string
			err error
		)
		if l.oci != "" {
			vs, err = s.Tags.Tags(ctx, l.oci)
			if err != nil {
				err = fmt.Errorf("could not retrieve list of tags for repository %s: %w", l.oci, err)
			}
		} else {
			vs, err = s.Index.Versions(ctx, l.repoURL, l.name)
			var nf *chartrepo.NotFoundError
			if err != nil && !errors.As(err, &nf) {
				err = fmt.Errorf("could not retrieve the index of repository %s: %w", l.repoURL, err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		out[l] = vs
	})
	return out, firstErr
}

// LocalPath is Helm's resolution of a file:// repository: absolute, or
// relative to the chart directory. The directory must exist.
func LocalPath(repository, chartDir string) (string, error) {
	p := strings.TrimPrefix(repository, "file://")
	var dir string
	if strings.HasPrefix(p, "/") {
		var err error
		if dir, err = filepath.Abs(p); err != nil {
			return "", err
		}
	} else {
		dir = filepath.Join(chartDir, p)
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("directory %s not found", dir)
	} else if err != nil {
		return "", err
	}
	return dir, nil
}

// LoadLocal loads the chart a file:// repository points at.
func LoadLocal(repository, chartDir string) (*chart.Chart, error) {
	dir, err := LocalPath(repository, chartDir)
	if err != nil {
		return nil, err
	}
	return loader.LoadDir(dir)
}

func nearest(tags []string) string {
	if len(tags) == 0 {
		return "none"
	}
	return strings.Join(tags[:min(5, len(tags))], ", ")
}
