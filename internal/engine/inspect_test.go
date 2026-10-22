package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/engine"
	"github.com/mmarszalek/helm-forge/internal/testutil/fakerepo"
)

func inspectOpts(o engine.Options) engine.InspectOptions {
	return engine.InspectOptions{ChartDir: o.ChartDir, RepoConfig: o.RepoConfig, Store: o.Store}
}

func states(rows []engine.Row) string {
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = string(r.State)
	}
	return strings.Join(s, ",")
}

func TestInspectStates(t *testing.T) {
	fr, o := newEngine(t)
	r := fakerepo.New(t)
	push(t, fr, "dep-a", "1.0.0", "1.2.0")
	addCharts(t, r, "dep-b", "0.1.0")
	writeLocalChart(t, filepath.Join(o.ChartDir, "local"), "local", "0.3.0")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository(), Alias: "first"},
		dep{Name: "dep-a", Version: "~1.0.0", Repository: fr.Repository(), Alias: "second"},
		dep{Name: "dep-b", Version: "0.1.0", Repository: r.URL()},
		dep{Name: "local", Version: "0.3.0", Repository: "file://local"},
	)

	rep, err := engine.Inspect(inspectOpts(o))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chart != "redis" || rep.ChartVersion != "0.1.0" || rep.HasLock || states(rep.Rows) != "no_lock,no_lock,no_lock,no_lock" {
		t.Fatalf("before update: %+v", rep)
	}

	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	fr.Reset()
	rep, err = engine.Inspect(inspectOpts(o))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasLock || !rep.LockInSync || states(rep.Rows) != "cached,cached,cached,local" {
		t.Fatalf("after update: %+v", rep)
	}
	first, second, b := rep.Rows[0], rep.Rows[1], rep.Rows[2]
	if first.Alias != "first" || first.Locked != "1.2.0" || second.Locked != "1.0.0" || first.Constraint != "^1.0.0" {
		t.Fatalf("aliases: %+v / %+v", first, second)
	}
	if first.Digest == "" || first.Size == 0 || first.LastUsed.IsZero() || first.BlobPath == "" ||
		first.FileName != "dep-a-1.2.0.tgz" || !first.InCharts {
		t.Fatalf("cached row: %+v", first)
	}
	if b.FileName != "dep-b-0.1.0.tgz" || !b.InCharts || b.Repository != r.URL() {
		t.Fatalf("chart repository row: %+v", b)
	}
	if n := len(fr.Requests()); n != 0 {
		t.Fatalf("inspect made %d registry requests", n)
	}

	// A different cache: nothing cached, but the lock still says what to fetch.
	rep, _ = engine.Inspect(inspectOpts(freshStore(t, o)))
	if states(rep.Rows) != "not_cached,not_cached,not_cached,local" {
		t.Fatalf("fresh cache: %s", states(rep.Rows))
	}

	// Chart.yaml changed after the lock: rows still find their lock entry.
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "^1.1.0", Repository: fr.Repository(), Alias: "first"},
		dep{Name: "dep-a", Version: "~1.0.0", Repository: fr.Repository(), Alias: "second"},
		dep{Name: "dep-b", Version: "0.1.0", Repository: r.URL()},
		dep{Name: "local", Version: "0.3.0", Repository: "file://local"},
		dep{Name: "dep-new", Version: "1.0.0", Repository: fr.Repository()},
	)
	rep, err = engine.Inspect(inspectOpts(o))
	if err != nil {
		t.Fatal(err)
	}
	if rep.LockInSync || states(rep.Rows) != "cached,cached,cached,local,no_lock" || rep.Rows[0].Locked != "1.2.0" {
		t.Fatalf("stale lock: in sync=%v states=%s rows=%+v", rep.LockInSync, states(rep.Rows), rep.Rows)
	}
}

func freshness(rows []engine.OutdatedRow) string {
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = r.Name + "=" + string(r.Freshness) + ":" + r.Locked + ">" + r.Wanted + "/" + r.Latest
	}
	return strings.Join(s, " ")
}

func TestOutdated(t *testing.T) {
	fr, o := newEngine(t)
	r := fakerepo.New(t)
	push(t, fr, "dep-a", "1.0.0", "1.2.0", "2.0.0")
	push(t, fr, "dep-b", "2.0.0", "3.0.0")
	push(t, fr, "dep-c", "1.0.0")
	addCharts(t, r, "dep-d", "0.1.0", "0.2.0")
	writeLocalChart(t, filepath.Join(o.ChartDir, "local"), "local", "0.3.0")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository()},
		dep{Name: "dep-b", Version: "2.0.0", Repository: fr.Repository()},
		dep{Name: "dep-c", Version: "~1.0.0", Repository: fr.Repository()},
		dep{Name: "dep-d", Version: "~0.1.0", Repository: r.URL()},
		dep{Name: "local", Version: "^0.3.0", Repository: "file://local"},
	)
	if _, err := engine.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	push(t, fr, "dep-a", "1.3.0")
	push(t, fr, "dep-b", "3.1.0-rc.1")
	writeLocalChart(t, filepath.Join(o.ChartDir, "local"), "local", "0.3.1")
	fr.Reset()

	rep, err := engine.Outdated(ctx, o, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "dep-a=update_in_constraint:1.2.0>1.3.0/2.0.0 " +
		"dep-b=newer_outside_constraint:2.0.0>2.0.0/3.0.0 " +
		"dep-c=up_to_date:1.0.0>1.0.0/1.0.0 " +
		"dep-d=newer_outside_constraint:0.1.0>0.1.0/0.2.0 " +
		"local=update_in_constraint:0.3.0>0.3.1/0.3.1"
	if got := freshness(rep.Rows); got != want {
		t.Fatalf("outdated:\n got %s\nwant %s", got, want)
	}
	if rep.Rows[2].Outdated() || !rep.Rows[0].Outdated() {
		t.Fatal("Outdated() disagrees with Freshness")
	}
	// One tags/list per OCI repository, even for exact versions.
	if n, tags := len(fr.Requests()), fr.Count("/tags/list"); tags != 3 || n > 3+1 {
		t.Fatalf("requests = %d (tags/list %d): %v", n, tags, fr.Requests())
	}

	rep, err = engine.Outdated(ctx, o, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Rows[1].Latest; got != "3.1.0-rc.1" {
		t.Fatalf("latest with prereleases = %s", got)
	}
}

func TestOutdatedNoMatchAndNoLock(t *testing.T) {
	fr, o := newEngine(t)
	push(t, fr, "dep-a", "2.0.0")
	writeChart(t, o.ChartDir,
		dep{Name: "dep-a", Version: "^1.0.0", Repository: fr.Repository()},
		dep{Name: "dep-b", Version: "^2.0.0", Repository: fr.Repository()},
	)
	push(t, fr, "dep-b", "2.0.0")
	lock := "dependencies:\n- name: dep-a\n  repository: " + fr.Repository() + "\n  version: 1.0.0\n" +
		"digest: sha256:0000000000000000000000000000000000000000000000000000000000000000\ngenerated: \"2026-01-01T00:00:00Z\"\n"
	if err := os.WriteFile(filepath.Join(o.ChartDir, "Chart.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := engine.Outdated(ctx, o, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := freshness(rep.Rows), "dep-a=no_match:1.0.0>/2.0.0 dep-b=no_lock:>2.0.0/2.0.0"; got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	if rep.LockInSync {
		t.Fatal("hand-written lock reported in sync")
	}
}
