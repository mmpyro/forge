package fetch_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/mmarszalek/helm-forge/internal/fetch"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/store"
	"github.com/mmarszalek/helm-forge/internal/testutil"
	"github.com/mmarszalek/helm-forge/internal/testutil/fakeregistry"
)

type env struct {
	reg *fakeregistry.Registry
	st  *store.Store
	f   *fetch.Fetcher
}

func newEnv(t *testing.T, opts ...fakeregistry.Option) *env {
	t.Helper()
	reg := fakeregistry.New(t, opts...)
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
	return &env{reg: reg, st: st, f: &fetch.Fetcher{Registry: client, Store: st}}
}

func (e *env) push(t *testing.T, name, version string) digest.Digest {
	return e.reg.AddChart("charts/"+name, version, testutil.ChartTgz(t, name, version))
}

func (e *env) item(name, version string) fetch.Item {
	return fetch.Item{Name: name, Version: version, Repository: e.reg.Repository()}
}

func mustOK(t *testing.T, results []fetch.Result) {
	t.Helper()
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s %s: %v", r.Item.Name, r.Item.Version, r.Err)
		}
	}
}

func fileDigest(t *testing.T, p string) digest.Digest {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return digest.FromBytes(b)
}

func TestColdFetchDownloadsEachChartOnce(t *testing.T) {
	e := newEnv(t)
	layers := map[string]digest.Digest{}
	var items []fetch.Item
	for _, n := range []string{"dep-a", "dep-b", "dep-c"} {
		layers[n] = e.push(t, n, "1.0.0")
		items = append(items, e.item(n, "1.0.0"))
	}
	results := e.f.Fetch(context.Background(), items)
	mustOK(t, results)
	for _, r := range results {
		if r.Cached || fileDigest(t, r.BlobPath) != layers[r.Item.Name] {
			t.Errorf("%s: cached=%v path=%s", r.Item.Name, r.Cached, r.BlobPath)
		}
		if fi, _ := os.Stat(r.BlobPath); r.Digest != layers[r.Item.Name] || r.Size != fi.Size() || r.Duration <= 0 {
			t.Errorf("%s: digest=%s size=%d duration=%s", r.Item.Name, r.Digest, r.Size, r.Duration)
		}
	}
	if m, b := e.reg.Count("/manifests/"), e.reg.Count("/blobs/"); m != 3 || b != 3 {
		t.Fatalf("manifests=%d blobs=%d, want 3 and 3", m, b)
	}
}

func TestWarmFetchMakesNoRequests(t *testing.T) {
	e := newEnv(t)
	e.push(t, "dep-a", "1.0.0")
	items := []fetch.Item{e.item("dep-a", "1.0.0")}
	mustOK(t, e.f.Fetch(context.Background(), items))
	e.reg.Reset()
	results := e.f.Fetch(context.Background(), items)
	mustOK(t, results)
	if !results[0].Cached {
		t.Fatal("want cached result")
	}
	if r := results[0]; r.Digest != fileDigest(t, r.BlobPath) || r.Size == 0 {
		t.Fatalf("cached result lacks digest/size: %+v", r)
	}
	if reqs := e.reg.Requests(); len(reqs) != 0 {
		t.Fatalf("warm fetch made requests: %v", reqs)
	}
}

func TestRefreshRechecksManifestsButReusesBlobs(t *testing.T) {
	e := newEnv(t)
	e.push(t, "dep-a", "1.0.0")
	items := []fetch.Item{e.item("dep-a", "1.0.0")}
	mustOK(t, e.f.Fetch(context.Background(), items))
	e.reg.Reset()
	e.f.Refresh = true
	mustOK(t, e.f.Fetch(context.Background(), items))
	if m, b := e.reg.Count("/manifests/"), e.reg.Count("/blobs/"); m != 1 || b != 0 {
		t.Fatalf("manifests=%d blobs=%d, want 1 and 0", m, b)
	}
}

func TestAliasesOfSameChartDownloadOnce(t *testing.T) {
	e := newEnv(t)
	e.push(t, "dep-a", "1.2.0")
	results := e.f.Fetch(context.Background(), []fetch.Item{e.item("dep-a", "1.2.0"), e.item("dep-a", "1.2.0")})
	mustOK(t, results)
	if results[0].BlobPath != results[1].BlobPath {
		t.Fatal("aliases got different blobs")
	}
	if m, b := e.reg.Count("/manifests/"), e.reg.Count("/blobs/"); m != 1 || b != 1 {
		t.Fatalf("manifests=%d blobs=%d, want 1 and 1", m, b)
	}
}

func TestPartialFailureKeepsSuccessfulBlobs(t *testing.T) {
	e := newEnv(t)
	layer := e.push(t, "dep-a", "1.0.0")
	results := e.f.Fetch(context.Background(), []fetch.Item{e.item("dep-a", "1.0.0"), e.item("dep-missing", "1.0.0")})
	if results[0].Err != nil || results[1].Err == nil {
		t.Fatalf("errs = %v, %v", results[0].Err, results[1].Err)
	}
	if !e.st.HasBlob(layer) {
		t.Fatal("successful blob not cached")
	}
}

func TestCorruptBlobIsRetriedOnce(t *testing.T) {
	e := newEnv(t)
	layer := e.push(t, "dep-a", "1.0.0")
	e.reg.Fail(e.reg.BlobPath("charts/dep-a", layer), fakeregistry.Fault{Corrupt: true})
	mustOK(t, e.f.Fetch(context.Background(), []fetch.Item{e.item("dep-a", "1.0.0")}))
	if b := e.reg.Count("/blobs/"); b != 2 {
		t.Fatalf("blob requests = %d, want 2", b)
	}
}

func TestCorruptBlobTwiceFails(t *testing.T) {
	e := newEnv(t)
	layer := e.push(t, "dep-a", "1.0.0")
	e.reg.Fail(e.reg.BlobPath("charts/dep-a", layer), fakeregistry.Fault{Corrupt: true}, fakeregistry.Fault{Corrupt: true})
	results := e.f.Fetch(context.Background(), []fetch.Item{e.item("dep-a", "1.0.0")})
	if !errors.Is(results[0].Err, store.ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", results[0].Err)
	}
}

func TestBuildMetadataVersionUsesUnderscoreTag(t *testing.T) {
	e := newEnv(t)
	e.push(t, "dep-c", "1.0.0+build.1")
	it := e.item("dep-c", "1.0.0+build.1")
	mustOK(t, e.f.Fetch(context.Background(), []fetch.Item{it}))
	if e.reg.Count("/manifests/1.0.0_build.1") != 1 {
		t.Fatalf("requests = %v", e.reg.Requests())
	}
	if it.FileName() != "dep-c-1.0.0+build.1.tgz" {
		t.Fatalf("file name = %s", it.FileName())
	}
}

func TestOneTokenRequestPerRegistry(t *testing.T) {
	e := newEnv(t, fakeregistry.WithBearerAuth())
	var items []fetch.Item
	for i := range 5 {
		name := fmt.Sprintf("dep-%d", i)
		e.push(t, name, "1.0.0")
		items = append(items, e.item(name, "1.0.0"))
	}
	mustOK(t, e.f.Fetch(context.Background(), items))
	toks := e.reg.TokenRequests()
	if len(toks) != 1 {
		t.Fatalf("token requests = %d, want 1", len(toks))
	}
	scopes := strings.Join(toks[0].Query["scope"], " ")
	for i := range 5 {
		if want := fmt.Sprintf("repository:charts/dep-%d:pull", i); !strings.Contains(scopes, want) {
			t.Errorf("scopes %q missing %q", scopes, want)
		}
	}
	if n, limit := len(e.reg.Requests()), 2*5+1; n > limit {
		t.Fatalf("registry requests = %d, want <= %d: %v", n, limit, e.reg.Requests())
	}
}

func TestInvalidRepositoryIsReportedPerItem(t *testing.T) {
	e := newEnv(t)
	results := e.f.Fetch(context.Background(), []fetch.Item{{Name: "x", Version: "1.0.0", Repository: "s3://bucket/charts"}})
	if results[0].Err == nil {
		t.Fatal("want error")
	}
}
