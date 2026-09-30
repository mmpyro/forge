package materialize_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/materialize"
	"github.com/mmarszalek/helm-forge/internal/testutil"
)

func blob(t *testing.T) string {
	t.Helper()
	p := testutil.WriteChartTgz(t, t.TempDir(), "dep-a", "1.0.0")
	if err := os.Chmod(p, 0o444); err != nil { // like a cached blob
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestCommitPlacesFilesAndRemovesStaleCharts(t *testing.T) {
	chartDir := t.TempDir()
	charts := filepath.Join(chartDir, "charts")
	_ = os.MkdirAll(filepath.Join(charts, "vendored"), 0o755)
	testutil.WriteChartTgz(t, charts, "old-dep", "9.9.9")
	_ = os.WriteFile(filepath.Join(charts, "notes.txt"), []byte("keep me"), 0o644)
	src := blob(t)

	st, err := materialize.NewStage(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Add(src, "dep-a-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(charts, "dep-a-1.0.0.tgz"))
	want, _ := os.ReadFile(src)
	if !bytes.Equal(got, want) {
		t.Fatal("placed archive differs from blob")
	}
	if exists(filepath.Join(charts, "old-dep-9.9.9.tgz")) {
		t.Error("stale chart archive not removed")
	}
	if !exists(filepath.Join(charts, "notes.txt")) {
		t.Error("non-chart file removed; Helm keeps those")
	}
	if !exists(filepath.Join(charts, "vendored")) {
		t.Error("subchart directory removed; Helm keeps those")
	}
	if exists(filepath.Join(chartDir, ".forge-staging")) {
		t.Error("staging directory left behind")
	}
}

func TestCommitCreatesChartsDir(t *testing.T) {
	chartDir := t.TempDir()
	st, _ := materialize.NewStage(chartDir)
	_ = st.Add(blob(t), "dep-a-1.0.0.tgz")
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(chartDir, "charts", "dep-a-1.0.0.tgz")) {
		t.Fatal("archive not placed")
	}
}

func TestCommitFailsWhenChartsIsAFile(t *testing.T) {
	chartDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(chartDir, "charts"), nil, 0o644)
	st, _ := materialize.NewStage(chartDir)
	_ = st.Add(blob(t), "dep-a-1.0.0.tgz")
	if err := st.Commit(); err == nil {
		t.Fatal("want error when charts is not a directory")
	}
}

func TestNewStageRemovesLeftoverStaging(t *testing.T) {
	chartDir := t.TempDir()
	leftover := filepath.Join(chartDir, ".forge-staging", "garbage.tgz")
	_ = os.MkdirAll(filepath.Dir(leftover), 0o755)
	_ = os.WriteFile(leftover, []byte("x"), 0o644)
	if _, err := materialize.NewStage(chartDir); err != nil {
		t.Fatal(err)
	}
	if exists(leftover) {
		t.Fatal("leftover staging content survived")
	}
}

func TestAbortLeavesChartsUntouched(t *testing.T) {
	chartDir := t.TempDir()
	charts := filepath.Join(chartDir, "charts")
	_ = os.MkdirAll(charts, 0o755)
	existing := testutil.WriteChartTgz(t, charts, "old-dep", "9.9.9")
	st, _ := materialize.NewStage(chartDir)
	_ = st.Add(blob(t), "dep-a-1.0.0.tgz")
	if err := st.Abort(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(charts)
	if len(entries) != 1 || !exists(existing) {
		t.Fatalf("charts/ changed: %v", entries)
	}
	if exists(filepath.Join(chartDir, ".forge-staging")) {
		t.Fatal("staging directory left behind")
	}
}

func TestAddSameFileTwiceIsNoop(t *testing.T) {
	st, _ := materialize.NewStage(t.TempDir())
	src := blob(t)
	if err := st.Add(src, "dep-a-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}
	if err := st.Add(src, "dep-a-1.0.0.tgz"); err != nil {
		t.Fatalf("second Add: %v", err)
	}
}

func TestAddRejectsPathTraversal(t *testing.T) {
	chartDir := t.TempDir()
	st, _ := materialize.NewStage(chartDir)
	src := blob(t)

	if err := st.Add(src, "../evil.tgz"); err == nil {
		t.Fatal("want error for ../evil.tgz")
	}
	if err := st.Add(src, "sub/x.tgz"); err == nil {
		t.Fatal("want error for sub/x.tgz")
	}

	if exists(filepath.Join(chartDir, "evil.tgz")) {
		t.Fatal("path traversal attack created file outside staging")
	}
}
