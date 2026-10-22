package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var timestampRe = regexp.MustCompile(`"(lastUsed|fetchedAt)": "[^"]+"`)

// cacheEnv is a cache filled by `dep update` of a chart with two
// dependencies from a fake registry.
func cacheEnv(t *testing.T) (*jsonEnv, string) {
	t.Helper()
	e := newJSONEnv(t, "  - name: dep-a\n    version: 1.0.0\n    repository: REPO\n"+
		"  - name: dep-b\n    version: 2.0.0\n    repository: REPO\n")
	e.fr.AddChart("charts/dep-a", "1.0.0", stableChart(t, "dep-a", "1.0.0"))
	e.fr.AddChart("charts/dep-b", "2.0.0", stableChart(t, "dep-b", "2.0.0"))
	if code, _, errs := e.run("update"); code != 0 {
		t.Fatalf("update: %s", errs)
	}
	return e, os.Getenv("HELM_FORGE_CACHE")
}

func (e *jsonEnv) normalizeCache(t *testing.T, out, root string) []byte {
	t.Helper()
	b := normalize(t, out, root, "CACHE", e.fr.Host(), "REGISTRY")
	return timestampRe.ReplaceAll(b, []byte(`"$1": "TIME"`))
}

// age makes every ref in the cache look last used d ago.
func age(t *testing.T, root string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	err := filepath.WalkDir(filepath.Join(root, "refs"), func(p string, de os.DirEntry, err error) error {
		if err == nil && de.Type().IsRegular() {
			err = os.Chtimes(p, when, when)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(root, "blobs", "sha256")
	des, _ := os.ReadDir(blobs)
	for _, de := range des {
		_ = os.Chtimes(filepath.Join(blobs, de.Name()), when, when)
	}
}

func TestCacheLsEmptyDoesNotCreateCache(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	t.Setenv("HELM_FORGE_CACHE", root)
	code, out, errs := run("cache", "ls")
	if code != 0 || !strings.Contains(out, "0 charts, 0 B in "+root) {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cache ls created the cache")
	}
}

func TestCacheLs(t *testing.T) {
	e, root := cacheEnv(t)
	code, out, errs := run("cache", "ls", "--sort", "size")
	if code != 0 || errs != "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	for _, want := range []string{"CHART", "dep-a", "dep-b", "oci://" + e.fr.Host() + "/charts", "2 charts, "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run("cache", "ls", "-o", "json")
	if code != 0 {
		t.Fatalf("json: code=%d", code)
	}
	golden(t, "cache-ls", e.normalizeCache(t, out, root))
}

func TestCacheGCDryRunThenRemove(t *testing.T) {
	e, root := cacheEnv(t)
	age(t, root, 40*24*time.Hour)

	code, out, _ := run("cache", "gc", "--older-than", "30d", "--dry-run", "-o", "json")
	if code != 0 {
		t.Fatalf("dry run: code=%d", code)
	}
	golden(t, "cache-gc-dry-run", e.normalizeCache(t, out, root))

	code, out, errs := run("cache", "gc", "--older-than", "30d")
	if code != 0 || !strings.HasPrefix(out, "Removed 2 charts (2 blobs)") {
		t.Fatalf("gc: code=%d out=%q err=%q", code, out, errs)
	}
	if _, out, _ = run("cache", "ls"); !strings.Contains(out, "0 charts") {
		t.Fatalf("cache not empty after gc:\n%s", out)
	}
	// The next build downloads again.
	if code, out, errs := run("dep", "build", "--plain-http", "--registry-config", e.cfg, e.chartDir); code != 0 ||
		!strings.Contains(out, "(0 cached, 2 downloaded)") {
		t.Fatalf("build after gc: code=%d out=%q err=%q", code, out, errs)
	}
}

func TestCacheGCMaxSizeKeepsRecentlyUsed(t *testing.T) {
	e, root := cacheEnv(t)
	age(t, root, 2*time.Hour)
	// dep build touches both refs: within the default 1h grace, nothing goes.
	if code, _, errs := run("dep", "build", "--plain-http", "--registry-config", e.cfg, e.chartDir); code != 0 {
		t.Fatal(errs)
	}
	code, out, _ := run("cache", "gc", "--max-size", "1B")
	if code != 0 || !strings.HasPrefix(out, "Removed 0 charts (0 blobs)") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = run("cache", "gc", "--max-size", "1B", "--grace", "0s", "-v")
	if code != 0 || !strings.Contains(out, "Removed 2 charts (2 blobs)") || !strings.Contains(out, "dep-a") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCacheGCUsageErrors(t *testing.T) {
	t.Setenv("HELM_FORGE_CACHE", t.TempDir())
	for _, args := range [][]string{
		{"cache", "gc"},
		{"cache", "gc", "--max-size", "0"},
		{"cache", "gc", "--max-size", "lots"},
		{"cache", "gc", "--older-than", "0s"},
		{"cache", "gc", "--older-than", "-1d"},
		{"cache", "gc", "--older-than", "1d", "--grace", "-1h"},
		{"cache", "gc", "--older-than", "1d", "-o", "yaml"},
		{"cache", "ls", "--sort", "colour"},
		{"cache", "ls", "extra"},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	code, out, _ := run("cache", "gc", "-o", "json")
	if code != 2 || !strings.Contains(out, `"command": "cache gc"`) || !strings.Contains(out, `"code": "usage"`) {
		t.Fatalf("json usage error: code=%d out=%s", code, out)
	}
}
