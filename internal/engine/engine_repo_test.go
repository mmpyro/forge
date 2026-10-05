package engine_test

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
	"github.com/mmarszalek/helm-forge/internal/store"
	"github.com/mmarszalek/helm-forge/internal/testutil"
	"github.com/mmarszalek/helm-forge/internal/testutil/fakerepo"
)

// withRepos points o at chart repositories configured by cfg (nil: none).
func withRepos(o engine.Options, cfg *repoconfig.Config) engine.Options {
	o.Repos = chartrepo.New(registry.NewHTTP(registry.Limits{Global: 16, PerHost: 8}, 10*time.Second), cfg, "forge-test")
	o.RepoConfig = cfg
	return o
}

func freshStore(t *testing.T, o engine.Options) engine.Options {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.Store = st
	return o
}

func addCharts(t *testing.T, r *fakerepo.Repo, name string, versions ...string) {
	for _, v := range versions {
		r.AddChart(name, v, testutil.ChartTgz(t, name, v))
	}
}

func TestRepoUpdateResolvesRangeThenWarmBuildIsOffline(t *testing.T) {
	_, o := newEngine(t)
	r := fakerepo.New(t)
	addCharts(t, r, "dep-a", "1.0.0", "1.2.0", "2.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^1.0.0", Repository: r.URL()})

	sum, err := engine.Update(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Charts != 1 || sum.Downloaded != 1 || sum.Cached != 0 || sum.Local != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if d := sum.Deps[0]; d.Status != engine.StatusDownloaded || d.Version != "1.2.0" || d.Digest == "" || d.Size == 0 || d.Placement == "" {
		t.Fatalf("dep = %+v", d)
	}
	if want := []string{"GET /charts/index.yaml", "GET /charts/dep-a-1.2.0.tgz"}; !slices.Equal(r.Requests(), want) {
		t.Fatalf("requests = %v, want %v", r.Requests(), want)
	}
	c, err := chartmeta.Load(o.ChartDir)
	if err != nil {
		t.Fatal(err)
	}
	if l := c.Lock.Dependencies[0]; l.Version != "1.2.0" || l.Repository != r.URL() {
		t.Fatalf("lock = %+v", l)
	}

	r.Reset()
	_ = os.RemoveAll(filepath.Join(o.ChartDir, "charts"))
	sum, err = engine.Build(ctx, withRepos(o, nil))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Cached != 1 || len(r.Requests()) != 0 {
		t.Fatalf("warm build: summary %+v, requests %v", sum, r.Requests())
	}
	if !exists(filepath.Join(o.ChartDir, "charts", "dep-a-1.2.0.tgz")) {
		t.Fatalf("charts = %v", chartsList(t, o.ChartDir))
	}
}

func TestRepoColdBuildFetchesIndexOncePerRepository(t *testing.T) {
	_, o := newEngine(t)
	r := fakerepo.New(t)
	addCharts(t, r, "dep-a", "1.0.0")
	addCharts(t, r, "dep-b", "0.1.0")
	addCharts(t, r, "dep-c", "3.0.0")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "1.0.0", Repository: r.URL()},
		dep{Name: "dep-b", Version: "0.1.0", Repository: r.URL() + "/"},
		dep{Name: "dep-c", Version: "3.0.0", Repository: r.URL(), Alias: "c1"},
		dep{Name: "dep-c", Version: "3.0.0", Repository: r.URL(), Alias: "c2"})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}

	r.Reset()
	o = withRepos(freshStore(t, o), nil) // a new run: empty cache, no index in memory
	if _, err := engine.Build(ctx, o); err != nil {
		t.Fatal(err)
	}
	reqs := r.Requests()
	if n := strings.Count(strings.Join(reqs, "\n"), "index.yaml"); n != 1 || len(reqs) != 4 {
		t.Fatalf("requests = %v; want 1 index + 3 archives", reqs)
	}
	if got := chartsList(t, o.ChartDir); len(got) != 3 {
		t.Fatalf("charts = %v", got)
	}
}

func TestRepoArchiveNameComesFromChartURL(t *testing.T) {
	_, o := newEngine(t)
	r := fakerepo.New(t)
	r.Serve(fakerepo.Path+"/pkgs/custom.tgz", testutil.ChartTgz(t, "dep-a", "1.0.0"))
	r.AddEntry("dep-a", "1.0.0", "pkgs/custom.tgz")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: r.URL()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(o.ChartDir, "charts"))
	if _, err := engine.Build(ctx, o); err != nil { // from cache
		t.Fatal(err)
	}
	if got := chartsList(t, o.ChartDir); !slices.Equal(got, []string{"custom.tgz"}) {
		t.Fatalf("charts = %v", got)
	}
}

func TestRepoSkipsVersionsWithoutURLs(t *testing.T) {
	_, o := newEngine(t)
	r := fakerepo.New(t)
	addCharts(t, r, "dep-a", "1.0.0")
	r.AddEntry("dep-a", "1.5.0", "")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^1", Repository: r.URL()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	c, _ := chartmeta.Load(o.ChartDir)
	if v := c.Lock.Dependencies[0].Version; v != "1.0.0" {
		t.Fatalf("locked %s", v)
	}
}

func TestRepoMissingChartOrVersion(t *testing.T) {
	_, o := newEngine(t)
	r := fakerepo.New(t)
	addCharts(t, r, "dep-a", "1.0.0")

	writeChart(t, o.ChartDir, dep{Name: "nope", Version: "1.0.0", Repository: r.URL()})
	if _, err := engine.Update(ctx, o); err == nil || !strings.Contains(err.Error(), "nope chart not found in repo "+r.URL()) {
		t.Fatalf("missing chart: %v", err)
	}
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^2", Repository: r.URL()})
	if _, err := engine.Update(ctx, o); err == nil || !strings.Contains(err.Error(), "available versions of dep-a: 1.0.0") {
		t.Fatalf("missing version: %v", err)
	}
}

func TestAliasRepositoryLocksResolvedURL(t *testing.T) {
	for _, ref := range []string{"@myrepo", "alias:myrepo"} {
		t.Run(ref, func(t *testing.T) {
			_, o := newEngine(t)
			r := fakerepo.New(t)
			addCharts(t, r, "dep-a", "1.0.0")
			o = withRepos(o, repoconfig.New(&repoconfig.Entry{Name: "myrepo", URL: r.URL()}))
			writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: ref})
			if _, err := engine.Update(ctx, o); err != nil {
				t.Fatal(err)
			}
			lock, _ := os.ReadFile(filepath.Join(o.ChartDir, "Chart.lock"))
			if !strings.Contains(string(lock), "repository: "+r.URL()) {
				t.Fatalf("lock:\n%s", lock)
			}
			// Build hashes the resolved URL too, so the lock is in sync.
			if _, err := engine.Build(ctx, o); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUndefinedRepositoryFailsBeforeNetwork(t *testing.T) {
	_, o := newEngine(t)
	writeChart(t, o.ChartDir,
		dep{Name: "a", Version: "1.0.0", Repository: "@nope"},
		dep{Name: "b", Version: "1.0.0", Repository: "stable"})
	_, err := engine.Update(ctx, o)
	var nd *repoconfig.NotDefinedError
	if !errors.As(err, &nd) || !strings.Contains(err.Error(), "no repository definition for @nope, stable") ||
		!strings.Contains(err.Error(), "Note that repositories must be URLs or aliases") {
		t.Fatalf("err = %v", err)
	}
}

func TestRepoCredentialsStayOnRepositoryHost(t *testing.T) {
	for _, passAll := range []bool{false, true} {
		_, o := newEngine(t)
		r := fakerepo.New(t, fakerepo.WithBasicAuth("u", "p"))
		other := fakerepo.New(t)
		other.Serve("/elsewhere/dep-a-1.0.0.tgz", testutil.ChartTgz(t, "dep-a", "1.0.0"))
		r.AddEntry("dep-a", "1.0.0", "http://"+other.Host()+"/elsewhere/dep-a-1.0.0.tgz")
		o = withRepos(o, repoconfig.New(&repoconfig.Entry{Name: "r", URL: r.URL(), Username: "u", Password: "p", PassCredentialsAll: passAll}))
		writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: r.URL()})
		if _, err := engine.Update(ctx, o); err != nil {
			t.Fatal(err)
		}
		if want := []string{"GET /charts/index.yaml (auth)"}; !slices.Equal(r.Requests(), want) {
			t.Fatalf("repo requests = %v", r.Requests())
		}
		want := "GET /elsewhere/dep-a-1.0.0.tgz"
		if passAll {
			want += " (auth)"
		}
		if !slices.Equal(other.Requests(), []string{want}) {
			t.Fatalf("passAll=%v: other host requests = %v", passAll, other.Requests())
		}
	}
}

func TestRepoUnauthorizedExplains(t *testing.T) {
	_, o := newEngine(t)
	r := fakerepo.New(t, fakerepo.WithBasicAuth("u", "p"))
	addCharts(t, r, "dep-a", "1.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: r.URL()})
	_, err := engine.Update(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestRepoArchiveFailureLeavesChartsUntouched(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	r := fakerepo.New(t)
	addCharts(t, r, "dep-b", "1.0.0")
	r.Fail(fakerepo.Path+"/dep-b-1.0.0.tgz", http.StatusNotFound)
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()},
		dep{Name: "dep-b", Version: "1.0.0", Repository: r.URL()})
	_, err := engine.Update(ctx, o)
	var de *engine.DependencyError
	if !errors.As(err, &de) || len(de.Failures) != 1 || !strings.Contains(de.Failures[0].Reason, "404 not found: http://") {
		t.Fatalf("err = %v", err)
	}
	if exists(filepath.Join(o.ChartDir, "charts")) || exists(filepath.Join(o.ChartDir, "Chart.lock")) {
		t.Fatal("charts/ or Chart.lock written despite failure")
	}
}

func writeLocalChart(t *testing.T, dir, name, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	y := "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-" + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "templates", "cm.yaml"), []byte(cm), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileDependencyIsPackagedLikeHelm(t *testing.T) {
	_, o := newEngine(t)
	local := filepath.Join(o.ChartDir, "local-dep")
	writeLocalChart(t, local, "local-dep", "0.3.0")
	writeChart(t, o.ChartDir, dep{Name: "local-dep", Version: "~0.3.0", Repository: "file://local-dep"})

	sum, err := engine.Update(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Charts != 1 || sum.Local != 1 || sum.Cached != 0 || sum.Downloaded != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if d := sum.Deps[0]; d.Status != engine.StatusLocal || d.Version != "0.3.0" || d.Size == 0 || d.Placement == "" {
		t.Fatalf("dep = %+v", d)
	}
	c, _ := chartmeta.Load(o.ChartDir)
	if l := c.Lock.Dependencies[0]; l.Version != "0.3.0" || l.Repository != "file://local-dep" {
		t.Fatalf("lock = %+v", l)
	}
	got, err := os.ReadFile(filepath.Join(o.ChartDir, "charts", "local-dep-0.3.0.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := loader.LoadDir(local)
	p, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := os.ReadFile(p); !bytes.Equal(got, want) {
		t.Fatal("archive differs from chartutil.Save of the same directory")
	}

	// The local chart moved on: build refuses the stale lock, update re-locks.
	writeLocalChart(t, local, "local-dep", "0.3.1")
	if _, err := engine.Build(ctx, o); err == nil || !strings.Contains(err.Error(), "can't get a valid version for dependency local-dep") {
		t.Fatalf("build: %v", err)
	}
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got := chartsList(t, o.ChartDir); !slices.Equal(got, []string{"local-dep-0.3.1.tgz"}) {
		t.Fatalf("charts = %v", got)
	}
	writeLocalChart(t, local, "local-dep", "0.4.0")
	if _, err := engine.Update(ctx, o); err == nil || !strings.Contains(err.Error(), "local version of local-dep: 0.4.0") {
		t.Fatalf("update: %v", err)
	}
}

func TestFileDependencyMissingDirFailsBeforeNetwork(t *testing.T) {
	fr, o := newEngine(t)
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()},
		dep{Name: "gone", Version: "1.0.0", Repository: "file://../gone"})
	if _, err := engine.Update(ctx, o); err == nil || !strings.Contains(err.Error(), "gone not found") {
		t.Fatalf("err = %v", err)
	}
	if len(fr.Requests()) != 0 {
		t.Fatalf("requests = %v", fr.Requests())
	}
}

func TestMixedSourcesInOneChart(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0", "1.1.0")
	r := fakerepo.New(t)
	addCharts(t, r, "dep-b", "0.1.0", "0.2.0")
	writeLocalChart(t, filepath.Join(o.ChartDir, "local-dep"), "local-dep", "1.0.0")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "~1.0", Repository: fr.Repository()},
		dep{Name: "dep-b", Version: ">0.1.0", Repository: r.URL()},
		dep{Name: "local-dep", Version: "1.0.0", Repository: "file://./local-dep"})
	sum, err := engine.Update(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Charts != 3 || sum.Downloaded != 2 || sum.Local != 1 || sum.Cached != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	var statuses []engine.Status
	for _, d := range sum.Deps {
		statuses = append(statuses, d.Status)
	}
	if want := []engine.Status{engine.StatusDownloaded, engine.StatusDownloaded, engine.StatusLocal}; !slices.Equal(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	want := []string{"dep-a-1.0.0.tgz", "dep-b-0.2.0.tgz", "local-dep-1.0.0.tgz"}
	if got := chartsList(t, o.ChartDir); !slices.Equal(got, want) {
		t.Fatalf("charts = %v", got)
	}
}
