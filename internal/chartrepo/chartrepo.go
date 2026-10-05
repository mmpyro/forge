// Package chartrepo talks to classic Helm chart repositories: an index.yaml
// listing every chart version and the URL of its archive.
package chartrepo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"

	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/repoconfig"
)

// StatusError is a non-200 response.
type StatusError struct {
	URL        string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: %d %s", e.URL, e.StatusCode, http.StatusText(e.StatusCode))
}

// NotFoundError means the index has no such chart, or no such version of it.
type NotFoundError struct{ Name, Version, Repository string }

func (e *NotFoundError) Error() string {
	if e.Version == "" {
		return fmt.Sprintf("%s chart not found in repo %s", e.Name, e.Repository)
	}
	return fmt.Sprintf("%s version %s not found in repo %s", e.Name, e.Version, e.Repository)
}

// Client is safe for concurrent use. It downloads each repository's index
// at most once per run.
type Client struct {
	http      *registry.HTTP
	cfg       *repoconfig.Config
	userAgent string

	mu      sync.Mutex
	indexes map[string]*indexCall
	clients map[*repoconfig.Entry]*http.Client
}

type indexCall struct {
	once sync.Once
	idx  *indexFile
	err  error
}

// New builds a Client. cfg supplies credentials and TLS settings for
// repositories Helm knows about; nil means none.
func New(h *registry.HTTP, cfg *repoconfig.Config, userAgent string) *Client {
	if cfg == nil {
		cfg = &repoconfig.Config{}
	}
	return &Client{
		http: h, cfg: cfg, userAgent: userAgent,
		indexes: map[string]*indexCall{},
		clients: map[*repoconfig.Entry]*http.Client{},
	}
}

// IsRepoURL reports whether repository is an http(s) chart repository URL.
func IsRepoURL(repository string) bool {
	u, err := url.ParseRequestURI(repository)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Host is the host of repoURL, for per-host scheduling.
func Host(repoURL string) string {
	if u, err := url.Parse(repoURL); err == nil {
		return u.Host
	}
	return repoURL
}

// CacheKey names repoURL's chart name in the cache: "https/host/path/name".
func CacheKey(repoURL, name string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return ""
	}
	p := strings.Trim(u.Path, "/")
	key := u.Scheme + "/" + u.Host
	if p != "" {
		key += "/" + p
	}
	return key + "/" + name
}

// Versions lists name's versions in repoURL, highest first, skipping
// entries without a download URL, as Helm's resolver does.
func (c *Client) Versions(ctx context.Context, repoURL, name string) ([]string, error) {
	idx, err := c.index(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	vs, ok := idx.Entries[name]
	if !ok {
		return nil, &NotFoundError{Name: name, Repository: repoURL}
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if len(v.URLs) > 0 {
			out = append(out, v.Version)
		}
	}
	return out, nil
}

// ChartURL is the archive URL of name at version, resolved against repoURL.
func (c *Client) ChartURL(ctx context.Context, repoURL, name, version string) (string, error) {
	idx, err := c.index(ctx, repoURL)
	if err != nil {
		return "", err
	}
	vs, ok := idx.Entries[name]
	if !ok {
		return "", &NotFoundError{Name: name, Repository: repoURL}
	}
	for _, v := range vs {
		if len(v.URLs) > 0 && versionEquals(version, v.Version) {
			return resolveReferenceURL(repoURL, v.URLs[0])
		}
	}
	return "", &NotFoundError{Name: name, Version: version, Repository: repoURL}
}

// Open streams the archive at chartURL, sending repoURL's credentials when
// Helm would.
func (c *Client) Open(ctx context.Context, repoURL, chartURL string) (io.ReadCloser, error) {
	return c.get(ctx, repoURL, chartURL)
}

func (c *Client) index(ctx context.Context, repoURL string) (*indexFile, error) {
	key := strings.TrimSuffix(repoURL, "/")
	c.mu.Lock()
	call, ok := c.indexes[key]
	if !ok {
		call = &indexCall{}
		c.indexes[key] = call
	}
	c.mu.Unlock()
	call.once.Do(func() { call.idx, call.err = c.loadIndex(ctx, repoURL) })
	return call.idx, call.err
}

func (c *Client) loadIndex(ctx context.Context, repoURL string) (*indexFile, error) {
	indexURL, err := resolveReferenceURL(repoURL, "index.yaml")
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, repoURL, indexURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", indexURL, err)
	}
	idx, err := parseIndex(data)
	if err != nil {
		return nil, fmt.Errorf("read index of %s: %w", repoURL, err)
	}
	return idx, nil
}

func (c *Client) get(ctx context.Context, repoURL, href string) (io.ReadCloser, error) {
	e := c.cfg.Lookup(repoURL)
	client, err := c.client(e)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return nil, err
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if e != nil && e.Username != "" && e.Password != "" && (e.PassCredentialsAll || sameOrigin(repoURL, href)) {
		req.SetBasicAuth(e.Username, e.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &StatusError{URL: href, StatusCode: resp.StatusCode}
	}
	return resp.Body, nil
}

// client is the shared client, or one with e's TLS settings.
func (c *Client) client(e *repoconfig.Entry) (*http.Client, error) {
	cfg, err := repoconfig.TLSConfig(e)
	if err != nil || cfg == nil {
		return c.http.Client(), err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hc, ok := c.clients[e]; ok {
		return hc, nil
	}
	hc := c.http.WithTLS(cfg)
	c.clients[e] = hc
	return hc, nil
}

// sameOrigin is Helm's rule for sending a repository's credentials: only to
// the same scheme and host:port, unless pass_credentials_all is set.
func sameOrigin(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host
}

func versionEquals(v1, v2 string) bool {
	sv1, err := semver.NewVersion(v1)
	if err != nil {
		return v1 == v2
	}
	sv2, err := semver.NewVersion(v2)
	if err != nil {
		return false
	}
	return sv1.Equal(sv2)
}
