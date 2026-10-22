package resolve_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Masterminds/semver/v3"
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

func TestPick(t *testing.T) {
	vs := []string{"2.0.0", "1.3.0-rc.1", "1.2.0+build.7", "1.1.0", "junk"}
	for constraint, want := range map[string]string{
		"^1.0.0":    "1.2.0+build.7",
		">=1.3.0-0": "2.0.0",
		"~1.1.0":    "1.1.0",
		"^3.0.0":    "",
	} {
		c, err := semver.NewConstraint(constraint)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := resolve.Pick(vs, c)
		if got != want || ok != (want != "") {
			t.Errorf("Pick(%q) = %q, %v; want %q", constraint, got, ok, want)
		}
	}
}

func TestNewest(t *testing.T) {
	vs := []string{"1.0.0", "2.1.0-rc.1", "2.0.0", "not-semver"}
	if got := resolve.Newest(vs, false); got != "2.0.0" {
		t.Errorf("stable newest = %q", got)
	}
	if got := resolve.Newest(vs, true); got != "2.1.0-rc.1" {
		t.Errorf("newest with prereleases = %q", got)
	}
	if got := resolve.Newest([]string{"1.0.0-rc.1"}, false); got != "" {
		t.Errorf("only prereleases = %q, want none", got)
	}
}

type fakeIndex map[string][]string // "<repoURL> <name>" → versions

func (f fakeIndex) Versions(_ context.Context, repoURL, name string) ([]string, error) {
	return f[repoURL+" "+name], nil
}

func TestAvailableListsEveryKindOnce(t *testing.T) {
	f := newFake(map[string][]string{"r.example/charts/dep-a": {"2.0.0", "1.0.0"}})
	idx := fakeIndex{"https://charts.example dep-b": {"0.2.0", "0.1.0"}}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "local", "Chart.yaml"), []byte("apiVersion: v2\nname: local\nversion: 0.3.0\n"), 0o644)

	a1, a2 := dep("dep-a", "1.0.0"), dep("dep-a", "^1.0.0") // exact versions are listed too
	b := &chart.Dependency{Name: "dep-b", Version: "~0.1.0", Repository: "https://charts.example"}
	l := &chart.Dependency{Name: "local", Version: "0.3.0", Repository: "file://local"}
	got, err := resolve.Available(context.Background(), resolve.Sources{Tags: f, Index: idx, ChartDir: dir}, []*chart.Dependency{a1, a2, b, l})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"2.0.0", "1.0.0"}, {"2.0.0", "1.0.0"}, {"0.2.0", "0.1.0"}, {"0.3.0"}}
	for i := range want {
		if strings.Join(got[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("dep %d: %v, want %v", i, got[i], want[i])
		}
	}
	if f.calls["r.example/charts/dep-a"] != 1 {
		t.Fatalf("calls = %v, want 1", f.calls)
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
