package cli_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mmarszalek/helm-forge/internal/testutil/fakeregistry"
)

var update = flag.Bool("update", false, "rewrite testdata/json goldens")

// stableChart is a minimal chart archive with fixed timestamps, so its
// digest and size are the same on every run.
func stableChart(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := []byte("apiVersion: v2\nname: " + name + "\nversion: " + version + "\n")
	hdr := &tar.Header{Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0)}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jsonEnv is a chart backed by a fake registry.
type jsonEnv struct {
	fr       *fakeregistry.Registry
	chartDir string
	cfg      string
}

func newJSONEnv(t *testing.T, deps string) *jsonEnv {
	t.Helper()
	e := &jsonEnv{fr: fakeregistry.New(t), chartDir: t.TempDir(), cfg: filepath.Join(t.TempDir(), "config.json")}
	yaml := "apiVersion: v2\nname: app\nversion: 0.1.0\ndependencies:\n" +
		strings.ReplaceAll(deps, "REPO", e.fr.Repository())
	if err := os.WriteFile(filepath.Join(e.chartDir, "Chart.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELM_FORGE_CACHE", t.TempDir())
	return e
}

func (e *jsonEnv) run(cmd string) (int, string, string) {
	return run("dep", cmd, "--plain-http", "--registry-config", e.cfg, "-o", "json", e.chartDir)
}

var (
	durationRe  = regexp.MustCompile(`"durationMs": \d+`)
	placementRe = regexp.MustCompile(`"placement": "[a-z]+"`)
)

// normalize replaces what changes between runs: durations, the temp chart
// path, the registry's port and the filesystem-dependent placement method.
// It keeps stdout byte for byte otherwise, so goldens show the real layout.
func normalize(t *testing.T, out string, repl ...string) []byte {
	t.Helper()
	if !json.Valid([]byte(out)) {
		t.Fatalf("stdout is not one JSON document:\n%s", out)
	}
	out = strings.NewReplacer(repl...).Replace(out)
	out = durationRe.ReplaceAllString(out, `"durationMs": 0`)
	out = placementRe.ReplaceAllString(out, `"placement": "PLACEMENT"`)
	return []byte(out)
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "json", name+".json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch (run go test ./internal/cli -update)\n--- got\n%s\n--- want\n%s", p, got, want)
	}
}

func (e *jsonEnv) normalize(t *testing.T, out string) []byte {
	return normalize(t, out, e.chartDir, "CHART", e.fr.Host(), "REGISTRY")
}

func TestJSONSuccess(t *testing.T) {
	e := newJSONEnv(t, "  - name: dep-a\n    version: ^1.0.0\n    repository: REPO\n    alias: cache\n"+
		"  - name: dep-b\n    version: 2.0.0\n    repository: REPO\n")
	e.fr.AddChart("charts/dep-a", "1.0.0", stableChart(t, "dep-a", "1.0.0"))
	e.fr.AddChart("charts/dep-a", "1.1.0", stableChart(t, "dep-a", "1.1.0"))
	e.fr.AddChart("charts/dep-b", "2.0.0", stableChart(t, "dep-b", "2.0.0"))

	code, out, errs := e.run("update")
	if code != 0 || errs != "" {
		t.Fatalf("update: code=%d stderr=%q", code, errs)
	}
	golden(t, "success-cold", e.normalize(t, out))
	if n := len(e.fr.Requests()); !strings.Contains(out, `"requests": 5`) || n != 5 {
		t.Fatalf("registry saw %d requests; stdout:\n%s", n, out)
	}

	code, out, errs = e.run("build")
	if code != 0 || errs != "" {
		t.Fatalf("build: code=%d stderr=%q", code, errs)
	}
	golden(t, "success-warm", e.normalize(t, out))
}

func TestJSONPartialFailure(t *testing.T) {
	e := newJSONEnv(t, "  - name: dep-a\n    version: 1.0.0\n    repository: REPO\n"+
		"  - name: dep-missing\n    version: 1.0.0\n    repository: REPO\n")
	e.fr.AddChart("charts/dep-a", "1.0.0", stableChart(t, "dep-a", "1.0.0"))

	code, out, errs := e.run("build")
	if code != 1 || !strings.Contains(errs, "✗ dep-missing 1.0.0") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	golden(t, "partial-failure", e.normalize(t, out))
}

func TestJSONNoMatchingVersion(t *testing.T) {
	e := newJSONEnv(t, "  - name: dep-a\n    version: ^1.0.0\n    repository: REPO\n"+
		"  - name: dep-b\n    version: ^3.0.0\n    repository: REPO\n")
	e.fr.AddChart("charts/dep-a", "1.0.0", stableChart(t, "dep-a", "1.0.0"))
	e.fr.AddChart("charts/dep-b", "2.0.0", stableChart(t, "dep-b", "2.0.0"))

	if code, out, _ := e.run("update"); code != 1 {
		t.Fatalf("code=%d", code)
	} else {
		golden(t, "no-matching-version", e.normalize(t, out))
	}
}

func TestJSONLockOutOfSync(t *testing.T) {
	e := newJSONEnv(t, "  - name: dep-a\n    version: 1.0.0\n    repository: REPO\n")
	e.fr.AddChart("charts/dep-a", "1.0.0", stableChart(t, "dep-a", "1.0.0"))
	if code, _, errs := e.run("update"); code != 0 {
		t.Fatalf("update: %s", errs)
	}
	lock := filepath.Join(e.chartDir, "Chart.lock")
	b, _ := os.ReadFile(lock)
	_ = os.WriteFile(lock, bytes.Replace(b, []byte("digest: sha256:"), []byte("digest: sha256:0"), 1), 0o644)

	code, out, _ := e.run("build")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	golden(t, "lock-out-of-sync", e.normalize(t, out))
}

func TestJSONUsageError(t *testing.T) {
	for name, args := range map[string][]string{
		"usage-error":      {"dep", "build", "a", "b", "-o", "json"},
		"usage-error-flag": {"dependency", "update", "--bogus", "--output=json"},
	} {
		code, out, errs := run(args...)
		if code != 2 || !strings.HasPrefix(errs, "Error:") {
			t.Fatalf("%v: code=%d stderr=%q", args, code, errs)
		}
		golden(t, name, normalize(t, out))
	}
}

func TestUsageErrorStaysTextWithoutJSON(t *testing.T) {
	for _, args := range [][]string{
		{"dep", "build", "a", "b"},
		{"dep", "build", "-o", "yaml"},
		{"bogus", "-o", "json"},
	} {
		if code, out, _ := run(args...); code != 2 || out != "" {
			t.Errorf("%v: code=%d stdout=%q", args, code, out)
		}
	}
}
