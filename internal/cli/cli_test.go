package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/cli"
	"github.com/mmarszalek/helm-forge/internal/testutil"
	"github.com/mmarszalek/helm-forge/internal/testutil/fakeregistry"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCachePathHonoursEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELM_FORGE_CACHE", dir)
	code, out, _ := run("cache", "path")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCacheCleanRemovesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	_ = os.MkdirAll(filepath.Join(dir, "blobs"), 0o755)
	t.Setenv("HELM_FORGE_CACHE", dir)
	if code, _, errs := run("cache", "clean"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errs)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cache still exists")
	}
}

func TestUsageErrorsExitWith2(t *testing.T) {
	for _, args := range [][]string{
		{"dep", "build", "--concurrency", "0"},
		{"dep", "build", "--bogus"},
		{"dep", "build", "a", "b"},
		{"bogus"},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestDepUpdateThenBuild(t *testing.T) {
	fr := fakeregistry.New(t)
	fr.AddChart("charts/dep-a", "1.0.0", testutil.ChartTgz(t, "dep-a", "1.0.0"))
	chartDir := t.TempDir()
	yaml := "apiVersion: v2\nname: redis\nversion: 0.1.0\ndependencies:\n  - name: dep-a\n    version: 1.0.0\n    repository: " + fr.Repository() + "\n"
	_ = os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(yaml), 0o644)
	t.Setenv("HELM_FORGE_CACHE", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "config.json")

	code, out, errs := run("dep", "update", "--plain-http", "--registry-config", cfg, chartDir)
	if code != 0 || !strings.Contains(out, "Saved 1 charts (0 cached, 1 downloaded)") {
		t.Fatalf("update: code=%d out=%q err=%q", code, out, errs)
	}
	code, out, errs = run("dep", "build", "--plain-http", "--registry-config", cfg, chartDir)
	if code != 0 || !strings.Contains(out, "Saved 1 charts (1 cached, 0 downloaded)") {
		t.Fatalf("build: code=%d out=%q err=%q", code, out, errs)
	}
}

func TestDependencyFailureExitsWith1(t *testing.T) {
	fr := fakeregistry.New(t)
	chartDir := t.TempDir()
	yaml := "apiVersion: v2\nname: redis\nversion: 0.1.0\ndependencies:\n  - name: dep-missing\n    version: 1.0.0\n    repository: " + fr.Repository() + "\n"
	_ = os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(yaml), 0o644)
	t.Setenv("HELM_FORGE_CACHE", t.TempDir())
	code, _, errs := run("dep", "build", "--plain-http", "--registry-config", filepath.Join(t.TempDir(), "c.json"), chartDir)
	if code != 1 || !strings.Contains(errs, "✗ dep-missing 1.0.0") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}
