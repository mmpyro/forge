//go:build integration

// Package integration runs forge against the local registry started by
// `make fixtures` and counts every request that reaches it.
package integration

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/chartmeta"
	"github.com/mmarszalek/helm-forge/internal/cli"
)

const upstream = "localhost:5001"

// countingProxy fronts the registry and counts requests reaching it.
func countingProxy(t *testing.T) (host string, count *atomic.Int64) {
	t.Helper()
	target, _ := url.Parse("http://" + upstream)
	rp := httputil.NewSingleHostReverseProxy(target)
	count = new(atomic.Int64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), count
}

// copyFixture copies testdata/charts/<name>, pointing its dependencies at host.
func copyFixture(t *testing.T, name, host string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "..", "testdata", "charts", name))); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dst, "Chart.yaml")
	b, _ := os.ReadFile(p)
	b = bytes.ReplaceAll(b, []byte("oci://"+upstream+"/"), []byte("oci://"+host+"/"))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func forge(t *testing.T, args ...string) {
	t.Helper()
	var out, errb bytes.Buffer
	if code := cli.Run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("forge %v: exit %d\n%s%s", args, code, out.String(), errb.String())
	}
}

func TestColdAndWarmRequestCounts(t *testing.T) {
	if resp, err := http.Get("http://" + upstream + "/v2/"); err != nil {
		t.Skipf("registry not running on %s (run `make fixtures`): %v", upstream, err)
	} else {
		resp.Body.Close()
	}
	host, count := countingProxy(t)
	cfg := filepath.Join(t.TempDir(), "config.json")
	for _, fx := range []string{"exact", "ranges", "prerelease", "alias", "condition-tags", "build-metadata"} {
		t.Run(fx, func(t *testing.T) {
			dir := copyFixture(t, fx, host)
			t.Setenv("HELM_FORGE_CACHE", t.TempDir())
			forge(t, "dep", "update", "--plain-http", "--registry-config", cfg, dir)

			c, err := chartmeta.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			unique := map[string]bool{}
			for _, d := range c.Lock.Dependencies {
				unique[d.Name+"@"+d.Version] = true
			}

			// Cold: fresh cache.
			t.Setenv("HELM_FORGE_CACHE", t.TempDir())
			_ = os.RemoveAll(filepath.Join(dir, "charts"))
			count.Store(0)
			forge(t, "dep", "build", "--plain-http", "--registry-config", cfg, dir)
			if got, limit := count.Load(), int64(2*len(unique)+1); got > limit {
				t.Errorf("cold build: %d registry requests, want <= %d", got, limit)
			}

			// Warm: same cache, charts/ deleted.
			_ = os.RemoveAll(filepath.Join(dir, "charts"))
			count.Store(0)
			forge(t, "dep", "build", "--plain-http", "--registry-config", cfg, dir)
			if got := count.Load(); got != 0 {
				t.Errorf("warm build: %d registry requests, want 0", got)
			}
			if entries, _ := os.ReadDir(filepath.Join(dir, "charts")); len(entries) != len(unique) {
				t.Errorf("charts/ has %d entries, want %d", len(entries), len(unique))
			}
		})
	}
}
