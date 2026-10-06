// Package fakerepo is an in-memory classic Helm chart repository for tests:
// an index.yaml plus chart archives under one path. It records every request.
package fakerepo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"sigs.k8s.io/yaml"
)

// Path is where the repository lives on the server.
const Path = "/charts"

// Repo is a fake chart repository.
type Repo struct {
	srv        *httptest.Server
	user, pass string

	mu       sync.Mutex
	entries  map[string][]map[string]any
	files    map[string][]byte // URL path -> content
	faults   map[string][]int  // URL path -> statuses to serve, in order
	requests []string
}

// Option configures a Repo.
type Option func(*Repo)

// WithBasicAuth requires these credentials on every request.
func WithBasicAuth(user, pass string) Option {
	return func(r *Repo) { r.user, r.pass = user, pass }
}

// New starts a repository that is shut down when the test ends.
func New(t testing.TB, opts ...Option) *Repo {
	r := &Repo{entries: map[string][]map[string]any{}, files: map[string][]byte{}, faults: map[string][]int{}}
	for _, o := range opts {
		o(r)
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

// URL is the Chart.yaml repository URL.
func (r *Repo) URL() string { return r.srv.URL + Path }

// Host is the server's host:port.
func (r *Repo) Host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// AddChart serves tgz as <name>-<version>.tgz and lists it in the index
// with a relative URL, as `helm repo index` does.
func (r *Repo) AddChart(name, version string, tgz []byte) {
	file := fmt.Sprintf("%s-%s.tgz", name, version)
	r.Serve(Path+"/"+file, tgz)
	r.AddEntry(name, version, file)
}

// AddEntry lists name at version in the index with the given download URL
// (relative to the repository, or absolute). An empty url lists no URLs.
func (r *Repo) AddEntry(name, version, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := map[string]any{"apiVersion": "v2", "name": name, "version": version}
	if url != "" {
		e["urls"] = []string{url}
	}
	r.entries[name] = append(r.entries[name], e)
}

// Serve makes the server return data at path.
func (r *Repo) Serve(path string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = data
}

// Fail queues statuses to serve for path, one per request, before serving it normally.
func (r *Repo) Fail(path string, statuses ...int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.faults[path] = append(r.faults[path], statuses...)
}

// Requests lists "GET /path" for every request so far, in arrival order.
// Requests that carried basic-auth credentials are suffixed " (auth)".
func (r *Repo) Requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// Reset forgets recorded requests.
func (r *Repo) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

func (r *Repo) serve(w http.ResponseWriter, req *http.Request) {
	user, pass, hasAuth := req.BasicAuth()
	r.mu.Lock()
	line := req.Method + " " + req.URL.Path
	if hasAuth {
		line += " (auth)"
	}
	r.requests = append(r.requests, line)
	var fault int
	if q := r.faults[req.URL.Path]; len(q) > 0 {
		fault, r.faults[req.URL.Path] = q[0], q[1:]
	}
	data, ok := r.files[req.URL.Path]
	var index []byte
	if req.URL.Path == Path+"/index.yaml" {
		index, ok = r.index(), true
		data = index
	}
	r.mu.Unlock()

	switch {
	case r.user != "" && (!hasAuth || user != r.user || pass != r.pass):
		w.Header().Set("WWW-Authenticate", `Basic realm="fakerepo"`)
		w.WriteHeader(http.StatusUnauthorized)
	case fault != 0:
		w.WriteHeader(fault)
	case !ok:
		w.WriteHeader(http.StatusNotFound)
	default:
		_, _ = w.Write(data)
	}
}

// index renders index.yaml. Callers hold r.mu.
func (r *Repo) index() []byte {
	names := make([]string, 0, len(r.entries))
	for n := range r.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	entries := map[string]any{}
	for _, n := range names {
		entries[n] = r.entries[n]
	}
	b, err := yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"entries":    entries,
		"generated":  "2026-01-01T00:00:00Z",
	})
	if err != nil {
		panic(err)
	}
	return b
}
