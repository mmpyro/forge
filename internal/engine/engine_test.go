package engine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/store"
	"github.com/mmarszalek/helm-forge/internal/testutil"
	"github.com/mmarszalek/helm-forge/internal/testutil/fakeregistry"
)

var ctx = context.Background()

type dep struct{ Name, Version, Repository, Alias string }

func writeChart(t *testing.T, dir string, deps ...dep) {
	t.Helper()
	var b strings.Builder
	b.WriteString("apiVersion: v2\nname: redis\nversion: 0.1.0\n")
	if len(deps) > 0 {
		b.WriteString("dependencies:\n")
	}
	for _, d := range deps {
		fmt.Fprintf(&b, "  - name: %s\n    version: %q\n    repository: %q\n", d.Name, d.Version, d.Repository)
		if d.Alias != "" {
			fmt.Fprintf(&b, "    alias: %s\n", d.Alias)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEngine(t *testing.T) (*fakeregistry.Registry, engine.Options) {
	t.Helper()
	fr := fakeregistry.New(t)
	client, err := registry.New(registry.Options{
		CredentialsFile: filepath.Join(t.TempDir(), "config.json"),
		PlainHTTP:       true,
		Limits:          registry.Limits{Global: 16, PerHost: 8},
		RequestTimeout:  10 * time.Second,
		UserAgent:       "forge-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return fr, engine.Options{ChartDir: t.TempDir(), Registry: client, Store: st}
}

func push(t *testing.T, fr *fakeregistry.Registry, name string, versions ...string) {
	for _, v := range versions {
		fr.AddChart("charts/"+name, v, testutil.ChartTgz(t, name, v))
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func chartsList(t *testing.T, dir string) []string {
	entries, _ := os.ReadDir(filepath.Join(dir, "charts"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestUpdateResolvesDownloadsAndWritesLock(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0", "1.2.0", "2.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository()})
	sum, err := engine.Update(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if sum != (engine.Summary{Charts: 1, Downloaded: 1}) {
		t.Fatalf("summary = %+v", sum)
	}
	if !exists(filepath.Join(o.ChartDir, "charts", "dep-a-1.2.0.tgz")) {
		t.Fatalf("charts/ = %v", chartsList(t, o.ChartDir))
	}
	c, err := chartmeta.Load(o.ChartDir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Lock == nil || c.Lock.Dependencies[0].Version != "1.2.0" {
		t.Fatalf("lock = %+v", c.Lock)
	}
	if err := c.CheckLock(); err != nil {
		t.Fatalf("written lock not in sync: %v", err)
	}
}

func TestUpdateKeepsLockWhenNothingChanged(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(o.ChartDir, "Chart.lock"))
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(o.ChartDir, "Chart.lock"))
	if !bytes.Equal(before, after) {
		t.Fatal("Chart.lock rewritten although nothing changed")
	}
}

func TestBuildUsesLockedVersionsWithoutListingTags(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.2.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	push(t, fr, "dep-a", "1.3.0")
	_ = os.RemoveAll(filepath.Join(o.ChartDir, "charts"))
	fr.Reset()
	if _, err := engine.Build(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(o.ChartDir, "charts", "dep-a-1.2.0.tgz")) {
		t.Fatalf("charts/ = %v", chartsList(t, o.ChartDir))
	}
	if fr.Count("/tags/list") != 0 {
		t.Fatal("build listed tags")
	}
}

func TestBuildWithoutLockRunsUpdate(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()})
	if _, err := engine.Build(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(o.ChartDir, "Chart.lock")) {
		t.Fatal("Chart.lock not written")
	}
}

func TestBuildRejectsOutOfSyncLock(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0", "2.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "^2.0.0", Repository: fr.Repository()})
	fr.Reset()
	_, err := engine.Build(ctx, o)
	if !errors.Is(err, chartmeta.ErrLockOutOfSync) {
		t.Fatalf("err = %v, want ErrLockOutOfSync", err)
	}
	if n := len(fr.Requests()); n != 0 {
		t.Fatalf("made %d requests before failing", n)
	}
}

func TestWarmBuildMakesNoRequests(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(o.ChartDir, "charts"))
	fr.Reset()
	sum, err := engine.Build(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if sum != (engine.Summary{Charts: 1, Cached: 1}) || len(fr.Requests()) != 0 {
		t.Fatalf("summary = %+v, requests = %v", sum, fr.Requests())
	}
	if !exists(filepath.Join(o.ChartDir, "charts", "dep-a-1.0.0.tgz")) {
		t.Fatal("archive not placed")
	}
}

func TestFailureLeavesChartsUntouched(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	charts := filepath.Join(o.ChartDir, "charts")
	_ = os.MkdirAll(charts, 0o755)
	keep := testutil.WriteChartTgz(t, charts, "old-dep", "9.9.9")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()},
		dep{Name: "dep-missing", Version: "1.0.0", Repository: fr.Repository()})
	_, err := engine.Update(ctx, o)
	var de *engine.DependencyError
	if !errors.As(err, &de) || len(de.Failures) != 1 {
		t.Fatalf("err = %v", err)
	}
	want := "✗ dep-missing 1.0.0 (" + fr.Repository() + "): 404 not found"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
	if got := chartsList(t, o.ChartDir); len(got) != 1 || !exists(keep) {
		t.Fatalf("charts/ changed: %v", got)
	}
	if _, ok := o.Store.GetRef(fr.Host()+"/charts/dep-a", "1.0.0"); !ok {
		t.Fatal("successful dependency not cached")
	}
	if exists(filepath.Join(o.ChartDir, "Chart.lock")) {
		t.Fatal("Chart.lock written despite failure")
	}
}

func TestUnsupportedRepositoryFailsBeforeNetwork(t *testing.T) {
	fr, o := newEngine(t)
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()},
		dep{Name: "local", Version: "0.1.0", Repository: "file://../local"},
		dep{Name: "classic", Version: "1.0.0", Repository: "https://charts.example.com"})
	for name, run := range map[string]func(context.Context, engine.Options) (engine.Summary, error){"build": engine.Build, "update": engine.Update} {
		_, err := run(ctx, o)
		if err == nil ||
			!strings.Contains(err.Error(), `"local" (repository "file://../local")`) ||
			!strings.Contains(err.Error(), `"classic" (repository "https://charts.example.com")`) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if len(fr.Requests()) != 0 || exists(filepath.Join(o.ChartDir, "charts")) {
		t.Fatalf("requests = %v", fr.Requests())
	}
}

func TestNoDependenciesIsNoop(t *testing.T) {
	fr, o := newEngine(t)
	writeChart(t, o.ChartDir)
	for _, run := range []func(context.Context, engine.Options) (engine.Summary, error){engine.Build, engine.Update} {
		if _, err := run(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if exists(filepath.Join(o.ChartDir, "Chart.lock")) || exists(filepath.Join(o.ChartDir, "charts")) || len(fr.Requests()) != 0 {
		t.Fatal("no-dependency chart was modified or hit the network")
	}
}

func TestUpdateRemovesStaleArchivesOnly(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	charts := filepath.Join(o.ChartDir, "charts")
	_ = os.MkdirAll(filepath.Join(charts, "vendored"), 0o755)
	testutil.WriteChartTgz(t, charts, "old-dep", "9.9.9")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(chartsList(t, o.ChartDir), ",")
	if got != "dep-a-1.0.0.tgz,vendored" {
		t.Fatalf("charts/ = %s", got)
	}
}

func TestLeftoverStagingIsCleaned(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	_ = os.MkdirAll(filepath.Join(o.ChartDir, ".forge-staging", "junk"), 0o755)
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(o.ChartDir, ".forge-staging")) {
		t.Fatal(".forge-staging left behind")
	}
}

func TestAliasesProduceOneArchive(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.2.0")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "1.2.0", Repository: fr.Repository(), Alias: "one"},
		dep{Name: "dep-a", Version: "1.2.0", Repository: fr.Repository(), Alias: "two"})
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got := chartsList(t, o.ChartDir); len(got) != 1 || got[0] != "dep-a-1.2.0.tgz" {
		t.Fatalf("charts/ = %v", got)
	}
	c, _ := chartmeta.Load(o.ChartDir)
	if len(c.Lock.Dependencies) != 2 {
		t.Fatalf("lock entries = %d, want 2", len(c.Lock.Dependencies))
	}
}

func TestCommitFailureCleansStaging(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "1.0.0")
	writeChart(t, o.ChartDir, dep{Name: "dep-a", Version: "1.0.0", Repository: fr.Repository()})
	if err := os.WriteFile(filepath.Join(o.ChartDir, "charts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Update(ctx, o); err == nil {
		t.Fatal("want error when charts/ is not a directory")
	}
	if exists(filepath.Join(o.ChartDir, ".forge-staging")) {
		t.Fatal(".forge-staging left behind after failed commit")
	}
}
