package fakeregistry_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mmarszalek/helm-forge/internal/testutil/fakeregistry"
)

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestServesPushedChart(t *testing.T) {
	r := fakeregistry.New(t)
	d := r.AddChart("charts/dep-a", "1.0.0+b.1", []byte("chart-bytes"))

	if p := r.ManifestPath("charts/dep-a", "1.0.0+b.1"); !strings.HasSuffix(p, "/manifests/1.0.0_b.1") {
		t.Fatalf("manifest path = %s", p)
	}
	resp, _ := get(t, "http://"+r.Host()+r.ManifestPath("charts/dep-a", "1.0.0+b.1"))
	if resp.StatusCode != 200 {
		t.Fatalf("manifest status = %d", resp.StatusCode)
	}
	_, body := get(t, "http://"+r.Host()+r.BlobPath("charts/dep-a", d))
	if body != "chart-bytes" {
		t.Fatalf("blob = %q", body)
	}
	if r.Count("/blobs/") != 1 || r.Count("/manifests/") != 1 {
		t.Fatalf("requests = %v", r.Requests())
	}
}

func TestFaultsAreServedInOrderThenCleared(t *testing.T) {
	r := fakeregistry.New(t)
	r.AddChart("charts/dep-a", "1.0.0", []byte("x"))
	path := r.ManifestPath("charts/dep-a", "1.0.0")
	r.Fail(path, fakeregistry.Fault{Status: 503}, fakeregistry.Fault{Status: 429, RetryAfter: "1"})

	if resp, _ := get(t, "http://"+r.Host()+path); resp.StatusCode != 503 {
		t.Fatalf("1st = %d", resp.StatusCode)
	}
	resp, _ := get(t, "http://"+r.Host()+path)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("2nd = %d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp, _ := get(t, "http://"+r.Host()+path); resp.StatusCode != 200 {
		t.Fatalf("3rd = %d", resp.StatusCode)
	}
}

func TestBearerAuthChallenges(t *testing.T) {
	r := fakeregistry.New(t, fakeregistry.WithBearerAuth())
	r.AddChart("charts/dep-a", "1.0.0", []byte("x"))
	resp, _ := get(t, "http://"+r.Host()+r.ManifestPath("charts/dep-a", "1.0.0"))
	h := resp.Header.Get("WWW-Authenticate")
	if resp.StatusCode != 401 || !strings.Contains(h, `realm="http://`) || !strings.Contains(h, `scope="repository:charts/dep-a:pull"`) {
		t.Fatalf("status %d, WWW-Authenticate %q", resp.StatusCode, h)
	}
}
