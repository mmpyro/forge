package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	chart "helm.sh/helm/v4/pkg/chart/v2"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
	"github.com/mmarszalek/helm-forge/internal/resolve"
	"github.com/mmarszalek/helm-forge/internal/store"
)

// InspectOptions configures Inspect. It makes no network requests.
type InspectOptions struct {
	ChartDir   string
	RepoConfig *repoconfig.Config // resolves @name repositories; nil means none
	Store      *store.Store       // nil: nothing is cached
}

// State is what forge knows locally about one dependency.
type State string

const (
	StateCached    State = "cached"     // the locked version is in the cache
	StateNotCached State = "not_cached" // locked, but dep build would download it
	StateLocal     State = "local"      // file:// dependency, packaged on every run
	StateNoLock    State = "no_lock"    // no Chart.lock entry: run dep update
)

// Row describes one Chart.yaml dependency.
type Row struct {
	Name       string
	Alias      string
	Constraint string // as written in Chart.yaml
	Repository string // @name replaced by its URL, as Helm locks it
	Condition  string
	Tags       []string
	Locked     string // version in Chart.lock; empty without a lock entry
	State      State
	Digest     string // cached archive; empty unless State is cached
	Size       int64
	FetchedAt  time.Time
	LastUsed   time.Time
	BlobPath   string
	FileName   string // archive name in charts/; empty without a lock entry
	InCharts   bool   // charts/<FileName> exists
}

// Report is what Inspect found.
type Report struct {
	Chart        string // Chart.yaml name
	ChartVersion string
	HasLock      bool
	LockInSync   bool // Chart.lock's digest matches Chart.yaml
	Rows         []Row
}

// Inspect describes every dependency of the chart from Chart.yaml,
// Chart.lock and the cache, without any network request.
func Inspect(o InspectOptions) (Report, error) {
	r, _, err := inspect(o)
	return r, err
}

func inspect(o InspectOptions) (Report, []*chart.Dependency, error) {
	c, err := chartmeta.Load(o.ChartDir)
	if err != nil {
		return Report{}, nil, err
	}
	deps, err := prepare(c.Dependencies, Options{ChartDir: o.ChartDir, RepoConfig: o.RepoConfig})
	if err != nil {
		return Report{}, nil, err
	}
	rep := Report{Chart: c.Name, ChartVersion: c.Version, HasLock: c.Lock != nil, Rows: make([]Row, len(deps))}

	locked := make([]*chart.Dependency, len(deps))
	if c.Lock != nil {
		c.Dependencies = deps
		rep.LockInSync = c.CheckLock() == nil
		if rep.LockInSync && len(c.Lock.Dependencies) == len(deps) {
			copy(locked, c.Lock.Dependencies)
		} else {
			used := make([]bool, len(c.Lock.Dependencies))
			for i, d := range deps {
				for k, l := range c.Lock.Dependencies {
					if !used[k] && l.Name == d.Name && l.Repository == d.Repository {
						used[k], locked[i] = true, l
						break
					}
				}
			}
		}
	}

	for i, d := range deps {
		row := Row{
			Name: d.Name, Alias: d.Alias, Constraint: d.Version, Repository: d.Repository,
			Condition: d.Condition, Tags: d.Tags, State: StateNoLock,
		}
		if l := locked[i]; l != nil {
			row.Locked = l.Version
			describeLocked(&row, o)
		}
		rep.Rows[i] = row
	}
	return rep, deps, nil
}

// describeLocked fills in how row's locked version would be installed.
func describeLocked(row *Row, o InspectOptions) {
	row.FileName = row.Name + "-" + row.Locked + ".tgz"
	defer func() {
		if row.FileName != "" {
			_, err := os.Stat(filepath.Join(o.ChartDir, "charts", row.FileName))
			row.InCharts = err == nil
		}
	}()
	if strings.HasPrefix(row.Repository, "file://") {
		row.State = StateLocal
		return
	}
	row.State = StateNotCached
	var key string
	if strings.HasPrefix(row.Repository, "oci://") {
		repo, err := registry.RepoRef(row.Repository, row.Name)
		if err != nil {
			return
		}
		key = repo
	} else {
		key = chartrepo.CacheKey(row.Repository, row.Name)
	}
	if o.Store == nil {
		return
	}
	e, ok := o.Store.Lookup(key, row.Locked)
	if !ok || !e.Valid || !e.BlobPresent {
		return
	}
	if e.Source == store.SourceRepo {
		if e.Ref.File == "" {
			return
		}
		row.FileName = e.Ref.File
	}
	row.State = StateCached
	row.Digest, row.Size = e.Ref.Layer.String(), e.BlobSize
	row.FetchedAt, row.LastUsed = e.Ref.FetchedAt, e.LastUsed
	row.BlobPath = o.Store.BlobPath(e.Ref.Layer)
}

// Freshness is how a dependency's locked version compares with what its
// repository offers now.
type Freshness string

const (
	UpToDate               Freshness = "up_to_date"
	UpdateInConstraint     Freshness = "update_in_constraint"     // dep update would lock a newer version
	NewerOutsideConstraint Freshness = "newer_outside_constraint" // newer exists; Chart.yaml's constraint excludes it
	NoMatch                Freshness = "no_match"                 // nothing satisfies the constraint any more
	NotLocked              Freshness = "no_lock"                  // no Chart.lock entry
)

// OutdatedRow is a Row with what its repository offers.
type OutdatedRow struct {
	Row
	Wanted    string // what dep update would lock now; empty for NoMatch
	Latest    string // highest version available
	Freshness Freshness
}

// Outdated reports whether the given row needs attention.
func (r OutdatedRow) Outdated() bool { return r.Freshness != UpToDate }

// OutdatedReport is what Outdated found.
type OutdatedReport struct {
	Chart        string
	ChartVersion string
	HasLock      bool
	LockInSync   bool
	Rows         []OutdatedRow
}

// Outdated compares every locked version with the versions its repository
// lists now: one tags/list per OCI repository and one index.yaml per chart
// repository. Prereleases count as "latest" only with includePrerelease, or
// when the constraint itself picks one.
func Outdated(ctx context.Context, o Options, includePrerelease bool) (OutdatedReport, error) {
	rep, deps, err := inspect(InspectOptions{ChartDir: o.ChartDir, RepoConfig: o.RepoConfig, Store: o.Store})
	if err != nil {
		return OutdatedReport{}, err
	}
	avail, err := resolve.Available(ctx, resolve.Sources{Tags: o.Registry, Index: o.Repos, ChartDir: o.ChartDir}, deps)
	if err != nil {
		return OutdatedReport{}, err
	}
	out := OutdatedReport{
		Chart: rep.Chart, ChartVersion: rep.ChartVersion, HasLock: rep.HasLock, LockInSync: rep.LockInSync,
		Rows: make([]OutdatedRow, len(rep.Rows)),
	}
	for i, row := range rep.Rows {
		c, err := semver.NewConstraint(row.Constraint)
		if err != nil {
			return OutdatedReport{}, fmt.Errorf("dependency %q has an invalid version/constraint format: %w", row.Name, err)
		}
		r := OutdatedRow{Row: row}
		r.Wanted, _ = resolve.Pick(avail[i], c)
		r.Latest = resolve.Newest(avail[i], includePrerelease)
		if newer(r.Wanted, r.Latest) {
			r.Latest = r.Wanted
		}
		switch {
		case row.Locked == "":
			r.Freshness = NotLocked
		case r.Wanted == "":
			r.Freshness = NoMatch
		case newer(r.Wanted, row.Locked):
			r.Freshness = UpdateInConstraint
		case newer(r.Latest, r.Wanted) && newer(r.Latest, row.Locked):
			r.Freshness = NewerOutsideConstraint
		default:
			r.Freshness = UpToDate
		}
		out.Rows[i] = r
	}
	return out, nil
}

// newer reports whether version a is higher than b. Unparsable or empty
// versions are never newer.
func newer(a, b string) bool {
	va, err := semver.NewVersion(a)
	if err != nil {
		return false
	}
	vb, err := semver.NewVersion(b)
	if err != nil {
		return true
	}
	return va.GreaterThan(vb)
}
