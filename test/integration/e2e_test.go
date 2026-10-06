//go:build integration

// Package integration runs forge against the local registry and chart
// repository started by `make fixtures` and counts every request that
// reaches them.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
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

const (
	upstream  = "localhost:5001" // OCI registry
	chartrepo = "localhost:5002" // classic chart repository
)

// countingProxy fronts target and counts requests reaching it.
func countingProxy(t *testing.T, target string, count *atomic.Int64) string {
	t.Helper()
	u, _ := url.Parse("http://" + target)
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// copyFixture copies testdata/charts/<name>, pointing its dependencies at
// the proxies.
func copyFixture(t *testing.T, name, regHost, repoHost string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "..", "testdata", "charts", name))); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dst, "Chart.yaml")
	b, _ := os.ReadFile(p)
	b = bytes.ReplaceAll(b, []byte("oci://"+upstream+"/"), []byte("oci://"+regHost+"/"))
	b = bytes.ReplaceAll(b, []byte("http://"+chartrepo), []byte("http://"+repoHost))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func forge(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	if code := cli.Run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("forge %v: exit %d\n%s%s", args, code, out.String(), errb.String())
	}
	return out.String()
}

// result is the part of `-o json` output these tests check.
type result struct {
	Dependencies []struct {
		Name       string `json:"name"`
		Repository string `json:"repository"`
		Status     string `json:"status"`
	} `json:"dependencies"`
	Registries []struct {
		Host     string `json:"host"`
		Requests int64  `json:"requests"`
	} `json:"registries"`
}

func forgeJSON(t *testing.T, args ...string) result {
	t.Helper()
	var r result
	out := forge(t, append(args, "-o", "json")...)
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("forge %v: bad JSON: %v\n%s", args, err, out)
	}
	return r
}

func (r result) requests() int64 {
	var n int64
	for _, h := range r.Registries {
		n += h.Requests
	}
	return n
}

func reachable(t *testing.T, u string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Skipf("%s not reachable (run `make fixtures`): %v", u, err)
	}
	resp.Body.Close()
}

func TestColdAndWarmRequestCounts(t *testing.T) {
	reachable(t, "http://"+upstream+"/v2/")
	reachable(t, "http://"+chartrepo+"/index.yaml")
	count := new(atomic.Int64)
	regHost := countingProxy(t, upstream, count)
	repoHost := countingProxy(t, chartrepo, count)
	flags := []string{
		"--plain-http",
		"--registry-config", filepath.Join(t.TempDir(), "config.json"),
		// No repositories.yaml: unregistered chart repository URLs must work.
		"--repository-config", filepath.Join(t.TempDir(), "repositories.yaml"),
	}
	dep := func(t *testing.T, mode, dir string) {
		forge(t, append([]string{"dep", mode, dir}, flags...)...)
	}
	depJSON := func(t *testing.T, mode, dir string) result {
		return forgeJSON(t, append([]string{"dep", mode, dir}, flags...)...)
	}
	for _, fx := range []string{"exact", "ranges", "prerelease", "alias", "condition-tags", "build-metadata", "http-ranges", "http-mixed", "file-local"} {
		t.Run(fx, func(t *testing.T) {
			dir := copyFixture(t, fx, regHost, repoHost)
			t.Setenv("HELM_FORGE_CACHE", t.TempDir())
			dep(t, "update", dir)

			c, err := chartmeta.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			// Cold budget: manifest + blob per OCI chart, one archive per chart
			// repository chart, plus one token or index round trip per source.
			// file:// charts cost nothing.
			unique := map[string]bool{}
			sources := map[string]bool{}
			limit := 0
			for _, d := range c.Lock.Dependencies {
				key := d.Repository + " " + d.Name + "@" + d.Version
				if unique[key] {
					continue
				}
				unique[key] = true
				switch {
				case strings.HasPrefix(d.Repository, "oci://"):
					limit += 2
					sources[regHost] = true
				case strings.HasPrefix(d.Repository, "http://"):
					limit++
					sources[strings.TrimSuffix(d.Repository, "/")] = true
				}
			}
			limit += len(sources)

			// Cold: fresh cache.
			t.Setenv("HELM_FORGE_CACHE", t.TempDir())
			_ = os.RemoveAll(filepath.Join(dir, "charts"))
			count.Store(0)
			cold := depJSON(t, "build", dir)
			if got := count.Load(); got > int64(limit) {
				t.Errorf("cold build: %d requests, want <= %d", got, limit)
			}
			if got, want := cold.requests(), count.Load(); got != want {
				t.Errorf("cold build: JSON reports %d requests, proxies saw %d (%+v)", got, want, cold.Registries)
			}

			// Warm: same cache, charts/ deleted.
			_ = os.RemoveAll(filepath.Join(dir, "charts"))
			count.Store(0)
			warm := depJSON(t, "build", dir)
			if got := count.Load(); got != 0 {
				t.Errorf("warm build: %d requests, want 0", got)
			}
			if len(warm.Registries) != 0 {
				t.Errorf("warm build: JSON reports registries %+v, want none", warm.Registries)
			}
			for _, d := range warm.Dependencies {
				want := "cached"
				if strings.HasPrefix(d.Repository, "file://") {
					want = "local" // packaged on every run
				}
				if d.Status != want {
					t.Errorf("warm build: %s is %s, want %s", d.Name, d.Status, want)
				}
			}
			if entries, _ := os.ReadDir(filepath.Join(dir, "charts")); len(entries) != len(unique) {
				t.Errorf("charts/ has %d entries, want %d", len(entries), len(unique))
			}
		})
	}
}
