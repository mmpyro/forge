package resolve_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	chart "helm.sh/helm/v4/pkg/chart/v2"

	"github.com/mmarszalek/helm-forge/internal/resolve"
)

const repoURL = "oci://r.example/charts"

type fakeTags struct {
	mu    sync.Mutex
	tags  map[string][]string // keyed by "r.example/charts/<name>"
	calls map[string]int
	err   error
}

func newFake(tags map[string][]string) *fakeTags {
	return &fakeTags{tags: tags, calls: map[string]int{}}
}

func (f *fakeTags) Tags(_ context.Context, repo string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[repo]++
	if f.err != nil {
		return nil, f.err
	}
	return f.tags[repo], nil
}

func dep(name, version string) *chart.Dependency {
	return &chart.Dependency{Name: name, Version: version, Repository: repoURL}
}

func resolveOne(t *testing.T, f *fakeTags, d *chart.Dependency) string {
	t.Helper()
	got, err := resolve.Resolve(context.Background(), resolve.Sources{Tags: f}, []*chart.Dependency{d})
	if err != nil {
		t.Fatal(err)
	}
	return got[0].Version
}

func TestExactVersionSkipsTagListing(t *testing.T) {
	f := newFake(nil)
	if v := resolveOne(t, f, dep("dep-a", "1.2.3")); v != "1.2.3" {
		t.Fatalf("version = %s", v)
	}
	if len(f.calls) != 0 {
		t.Fatalf("tags listed for an exact version: %v", f.calls)
	}
}

func TestRangePicksHighestMatch(t *testing.T) {
	f := newFake(map[string][]string{"r.example/charts/dep-a": {"2.0.0", "1.2.0", "1.1.0", "1.0.0"}})
	if v := resolveOne(t, f, dep("dep-a", "^1.0.0")); v != "1.2.0" {
		t.Fatalf("version = %s", v)
	}
}

func TestPrereleasesNeedAPrereleaseConstraint(t *testing.T) {
	f := newFake(map[string][]string{"r.example/charts/dep-b": {"0.2.0-rc.1", "0.1.0"}})
	if v := resolveOne(t, f, dep("dep-b", ">=0.1.0")); v != "0.1.0" {
		t.Fatalf("plain range picked %s", v)
	}
	if v := resolveOne(t, f, dep("dep-b", ">=0.2.0-0")); v != "0.2.0-rc.1" {
		t.Fatalf("prerelease range picked %s", v)
	}
}

func TestBuildMetadataVersionIsKept(t *testing.T) {
	f := newFake(map[string][]string{"r.example/charts/dep-c": {"1.0.0+build.1"}})
	if v := resolveOne(t, f, dep("dep-c", "~1.0.0")); v != "1.0.0+build.1" {
		t.Fatalf("version = %s", v)
	}
}

func TestSameRepositoryListedOnce(t *testing.T) {
	f := newFake(map[string][]string{"r.example/charts/dep-a": {"1.2.0"}})
	a, b := dep("dep-a", "^1.0.0"), dep("dep-a", "~1.2.0")
	a.Alias, b.Alias = "first", "second"
	got, err := resolve.Resolve(context.Background(), resolve.Sources{Tags: f}, []*chart.Dependency{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["r.example/charts/dep-a"] != 1 {
		t.Fatalf("calls = %v, want 1", f.calls)
	}
	// Helm's lock entries carry only name, repository and version.
	if got[0].Alias != "" || got[1].Version != "1.2.0" || got[1].Repository != repoURL {
		t.Fatalf("locked = %+v %+v", got[0], got[1])
	}
}

func TestInvalidConstraint(t *testing.T) {
	_, err := resolve.Resolve(context.Background(), resolve.Sources{Tags: newFake(nil)}, []*chart.Dependency{dep("dep-a", "not a version")})
	if err == nil || !strings.Contains(err.Error(), `dependency "dep-a" has an invalid version/constraint format`) {
		t.Fatalf("err = %v", err)
	}
}

func TestNoMatchingVersion(t *testing.T) {
	f := newFake(map[string][]string{"r.example/charts/dep-a": {"1.0.0"}})
	_, err := resolve.Resolve(context.Background(), resolve.Sources{Tags: f}, []*chart.Dependency{dep("dep-a", "^2.0.0")})
	if err == nil ||
		!strings.Contains(err.Error(), "can't get a valid version for 1 subchart(s)") ||
		!strings.Contains(err.Error(), "available versions of dep-a: 1.0.0") {
		t.Fatalf("err = %v", err)
	}
	var nm *resolve.NoMatchError
	if !errors.As(err, &nm) || len(nm.Missing) != 1 || nm.Missing[0].Constraint != "^2.0.0" || nm.Missing[0].Nearest() != "1.0.0" {
		t.Fatalf("want *NoMatchError for dep-a, got %#v", err)
	}
}

func TestTagListingError(t *testing.T) {
	f := newFake(nil)
	f.err = errors.New("boom")
	_, err := resolve.Resolve(context.Background(), resolve.Sources{Tags: f}, []*chart.Dependency{dep("dep-a", "^1.0.0")})
	if err == nil || !strings.Contains(err.Error(), "could not retrieve list of tags for repository r.example/charts/dep-a") {
		t.Fatalf("err = %v", err)
	}
}
