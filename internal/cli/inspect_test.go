package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var blobPathRe = regexp.MustCompile(`"blobPath": "[^"]+"`)

// inspectEnv is a chart with an aliased range dependency and an exact one,
// locked and fetched by `dep update`.
func inspectEnv(t *testing.T) *jsonEnv {
	t.Helper()
	e := newJSONEnv(t, "  - name: dep-a\n    version: ^1.0.0\n    repository: REPO\n    alias: cache\n    condition: cache.enabled\n"+
		"  - name: dep-b\n    version: 2.0.0\n    repository: REPO\n")
	e.fr.AddChart("charts/dep-a", "1.0.0", stableChart(t, "dep-a", "1.0.0"))
	e.fr.AddChart("charts/dep-b", "2.0.0", stableChart(t, "dep-b", "2.0.0"))
	if code, _, errs := e.run("update"); code != 0 {
		t.Fatalf("update: %s", errs)
	}
	e.fr.Reset()
	return e
}

func (e *jsonEnv) normalizeInspect(t *testing.T, out string) []byte {
	t.Helper()
	b := normalize(t, out, e.chartDir, "CHART", os.Getenv("HELM_FORGE_CACHE"), "CACHE", e.fr.Host(), "REGISTRY")
	b = timestampRe.ReplaceAll(b, []byte(`"$1": "TIME"`))
	return blobPathRe.ReplaceAll(b, []byte(`"blobPath": "BLOB"`))
}

func TestDepTree(t *testing.T) {
	e := inspectEnv(t)
	code, out, errs := run("dep", "tree", e.chartDir)
	if code != 0 || errs != "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	for _, want := range []string{"app 0.1.0\n", "├── dep-a (cache)", "└── dep-b", "^1.0.0", "1.0.0", "cached"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run("dep", "tree", "-o", "json", e.chartDir)
	if code != 0 {
		t.Fatalf("json: code=%d", code)
	}
	golden(t, "dep-tree", e.normalizeInspect(t, out))
	if n := len(e.fr.Requests()); n != 0 {
		t.Fatalf("dep tree made %d registry requests", n)
	}
}

func TestDepTreeWithoutLock(t *testing.T) {
	e := newJSONEnv(t, "  - name: dep-a\n    version: ^1.0.0\n    repository: REPO\n")
	code, out, _ := run("dep", "tree", e.chartDir)
	if code != 0 || !strings.Contains(out, "no lock") || !strings.Contains(out, "No Chart.lock: run 'forge dep update'") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDepWhy(t *testing.T) {
	e := inspectEnv(t)
	for _, name := range []string{"dep-a", "cache"} { // chart name or alias
		code, out, errs := run("dep", "why", name, e.chartDir)
		if code != 0 || errs != "" {
			t.Fatalf("%s: code=%d err=%q", name, code, errs)
		}
		for _, want := range []string{"dep-a (cache)", "Chart.yaml:", "^1.0.0 from oci://", "condition:", "cache.enabled",
			"Chart.lock:", "1.0.0", "cached, sha256:", "charts/:", "dep-a-1.0.0.tgz (present)"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: why lacks %q:\n%s", name, want, out)
			}
		}
	}
	code, out, _ := run("dep", "why", "dep-b", "-o", "json", e.chartDir)
	if code != 0 {
		t.Fatalf("json: code=%d", code)
	}
	golden(t, "dep-why", e.normalizeInspect(t, out))

	_ = os.RemoveAll(filepath.Join(e.chartDir, "charts"))
	if _, out, _ := run("dep", "why", "dep-b", e.chartDir); !strings.Contains(out, "missing: run 'forge dep build'") {
		t.Errorf("why after removing charts/:\n%s", out)
	}
	if n := len(e.fr.Requests()); n != 0 {
		t.Fatalf("dep why made %d registry requests", n)
	}
}

func TestDepWhyUnknownNameIsUsageError(t *testing.T) {
	e := inspectEnv(t)
	code, _, errs := run("dep", "why", "nope", e.chartDir)
	if code != 2 || !strings.Contains(errs, `no dependency named "nope"; dependencies: dep-a (cache), dep-b`) {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	code, out, _ := run("dep", "why", "nope", e.chartDir, "-o", "json")
	if code != 2 || !strings.Contains(out, `"command": "dep why"`) || !strings.Contains(out, `"code": "usage"`) {
		t.Fatalf("json: code=%d out=%s", code, out)
	}
	if code, _, _ := run("dep", "why"); code != 2 {
		t.Fatalf("no name: exit %d, want 2", code)
	}
}

func (e *jsonEnv) outdated(args ...string) (int, string, string) {
	return run(append([]string{"dep", "outdated", "--plain-http", "--registry-config", e.cfg, e.chartDir}, args...)...)
}

func TestDepOutdated(t *testing.T) {
	e := inspectEnv(t)
	code, out, errs := e.outdated()
	if code != 0 || !strings.Contains(out, "All 2 dependencies are up to date") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}

	e.fr.AddChart("charts/dep-a", "1.1.0", stableChart(t, "dep-a", "1.1.0"))
	e.fr.AddChart("charts/dep-a", "2.0.0-rc.1", stableChart(t, "dep-a", "2.0.0-rc.1"))
	e.fr.AddChart("charts/dep-b", "3.0.0", stableChart(t, "dep-b", "3.0.0"))
	e.fr.Reset()
	code, out, errs = e.outdated()
	if code != 0 || !strings.Contains(out, "2 of 2 dependencies are not up to date") ||
		!strings.Contains(out, "update in constraint") || !strings.Contains(out, "newer outside constraint") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if strings.Contains(out, "2.0.0-rc.1") {
		t.Fatalf("prerelease shown without --include-prerelease:\n%s", out)
	}
	if n, tags := len(e.fr.Requests()), e.fr.Count("/tags/list"); tags != 2 || n != 2 {
		t.Fatalf("requests = %d (tags/list %d), want one tags/list per repository: %v", n, tags, e.fr.Requests())
	}

	code, out, _ = e.outdated("--include-prerelease", "--fail-if-outdated", "-o", "json")
	if code != 1 {
		t.Fatalf("--fail-if-outdated: code=%d", code)
	}
	golden(t, "dep-outdated", e.normalizeInspect(t, out))

	code, _, errs = e.outdated("--fail-if-outdated")
	if code != 1 || !strings.Contains(errs, "2 of 2 dependencies are not up to date") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestDepOutdatedUsageErrors(t *testing.T) {
	t.Setenv("HELM_FORGE_CACHE", t.TempDir())
	for _, args := range [][]string{
		{"dep", "outdated", "--concurrency", "0"},
		{"dep", "outdated", "a", "b"},
		{"dep", "outdated", "-o", "yaml"},
		{"dep", "tree", "a", "b"},
		{"dep", "why", "x", "a", "b"},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
