// Package fakeregistry is an in-memory OCI registry for tests. It serves the
// subset of the distribution API that forge uses and records every request.
package fakeregistry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const fakeToken = "fake-token"

// Fault is one scripted misbehaviour, served instead of the normal response.
type Fault struct {
	Status     int    // respond with this status (e.g. 429, 500)
	RetryAfter string // Retry-After header for Status responses
	Reset      bool   // close the connection without responding
	Corrupt    bool   // serve the blob with its first byte flipped
}

// TokenRequest is one call to the token endpoint.
type TokenRequest struct {
	Query         url.Values
	Authorization string
}

// Registry is a fake OCI registry backed by memory.
type Registry struct {
	srv      *httptest.Server
	tokenSrv *httptest.Server
	bearer   bool

	mu        sync.Mutex
	manifests map[string][]byte // "<repo>:<tag>" -> manifest JSON
	tags      map[string][]string
	blobs     map[digest.Digest][]byte
	faults    map[string][]Fault
	requests  []string
	tokens    []TokenRequest
}

// Option configures a Registry.
type Option func(*Registry)

// WithBearerAuth requires a bearer token from a separate token server, like
// Harbor or GHCR. Tokens are issued to anyone, with or without credentials.
func WithBearerAuth() Option { return func(r *Registry) { r.bearer = true } }

// New starts a registry that is shut down when the test ends.
func New(t testing.TB, opts ...Option) *Registry {
	r := &Registry{
		manifests: map[string][]byte{},
		tags:      map[string][]string{},
		blobs:     map[digest.Digest][]byte{},
		faults:    map[string][]Fault{},
	}
	for _, o := range opts {
		o(r)
	}
	r.tokenSrv = httptest.NewServer(http.HandlerFunc(r.serveToken))
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(func() { r.srv.Close(); r.tokenSrv.Close() })
	return r
}

// Host is the registry's host:port.
func (r *Registry) Host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// Repository is the Chart.yaml repository URL for charts added under "charts/".
func (r *Registry) Repository() string { return "oci://" + r.Host() + "/charts" }

// AddChart stores tgz as a Helm chart at repo:version ("+" becomes "_" in the
// tag, as Helm does) and returns the chart layer's digest.
func (r *Registry) AddChart(repo, version string, tgz []byte) digest.Digest {
	layer := digest.FromBytes(tgz)
	config := []byte("{}")
	m := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: "application/vnd.cncf.helm.config.v1+json",
			Digest:    digest.FromBytes(config),
			Size:      int64(len(config)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: "application/vnd.cncf.helm.chart.content.v1.tar+gzip",
			Digest:    layer,
			Size:      int64(len(tgz)),
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blobs[layer] = tgz
	r.blobs[digest.FromBytes(config)] = config
	r.putManifest(repo, tagFor(version), b)
	return layer
}

// AddManifest stores raw manifest JSON at repo:tag, for non-chart artifacts.
func (r *Registry) AddManifest(repo, tag string, manifest []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.putManifest(repo, tag, manifest)
}

func (r *Registry) putManifest(repo, tag string, b []byte) {
	key := repo + ":" + tag
	if _, ok := r.manifests[key]; !ok {
		r.tags[repo] = append(r.tags[repo], tag)
	}
	r.manifests[key] = b
}

// Fail queues faults for requests to path; each request consumes one.
func (r *Registry) Fail(path string, faults ...Fault) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.faults[path] = append(r.faults[path], faults...)
}

// ManifestPath is the request path for repo's manifest at version.
func (r *Registry) ManifestPath(repo, version string) string {
	return "/v2/" + repo + "/manifests/" + tagFor(version)
}

// BlobPath is the request path for a blob in repo.
func (r *Registry) BlobPath(repo string, d digest.Digest) string {
	return "/v2/" + repo + "/blobs/" + d.String()
}

// Requests returns every request as "METHOD /path", in arrival order.
func (r *Registry) Requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// Count returns how many requests contain substr.
func (r *Registry) Count(substr string) int {
	n := 0
	for _, req := range r.Requests() {
		if strings.Contains(req, substr) {
			n++
		}
	}
	return n
}

// TokenRequests returns every call to the token endpoint.
func (r *Registry) TokenRequests() []TokenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TokenRequest(nil), r.tokens...)
}

// Reset forgets recorded requests and token requests.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests, r.tokens = nil, nil
}

func (r *Registry) serveToken(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.tokens = append(r.tokens, TokenRequest{Query: req.URL.Query(), Authorization: req.Header.Get("Authorization")})
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": fakeToken, "access_token": fakeToken})
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
	var f *Fault
	if q := r.faults[req.URL.Path]; len(q) > 0 {
		f = &q[0]
		r.faults[req.URL.Path] = q[1:]
	}
	r.mu.Unlock()

	if f != nil && f.Reset {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	if f != nil && f.Status != 0 {
		if f.RetryAfter != "" {
			w.Header().Set("Retry-After", f.RetryAfter)
		}
		http.Error(w, http.StatusText(f.Status), f.Status)
		return
	}
	if r.bearer && req.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="%s"`, r.tokenSrv.URL, scopeFor(req.URL.Path)))
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}

	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case path == "":
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(path, "/tags/list"):
		repo := strings.TrimSuffix(path, "/tags/list")
		r.mu.Lock()
		tags, ok := r.tags[repo]
		tags = append([]string(nil), tags...)
		r.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "NAME_UNKNOWN")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
	case strings.Contains(path, "/manifests/"):
		i := strings.LastIndex(path, "/manifests/")
		repo, tag := path[:i], path[i+len("/manifests/"):]
		r.mu.Lock()
		b, ok := r.manifests[repo+":"+tag]
		r.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
			return
		}
		var m struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", m.MediaType)
		w.Header().Set("Docker-Content-Digest", digest.FromBytes(b).String())
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if req.Method != http.MethodHead {
			_, _ = w.Write(b)
		}
	case strings.Contains(path, "/blobs/"):
		d := digest.Digest(path[strings.LastIndex(path, "/blobs/")+len("/blobs/"):])
		r.mu.Lock()
		b, ok := r.blobs[d]
		r.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "BLOB_UNKNOWN")
			return
		}
		if f != nil && f.Corrupt {
			b = append([]byte(nil), b...)
			b[0] ^= 0xff
		}
		w.Header().Set("Docker-Content-Digest", d.String())
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND")
	}
}

func tagFor(version string) string { return strings.ReplaceAll(version, "+", "_") }

func scopeFor(path string) string {
	p := strings.TrimPrefix(path, "/v2/")
	for _, sep := range []string{"/manifests/", "/blobs/", "/tags/"} {
		if i := strings.Index(p, sep); i >= 0 {
			return "repository:" + p[:i] + ":pull"
		}
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": code}}})
}
