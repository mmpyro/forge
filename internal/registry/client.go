// Package registry talks to OCI registries for forge: tags, chart manifests
// and blobs, with Helm's credentials and one shared token cache per run.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v4/pkg/helmpath"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

const (
	chartLayerMediaType       = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	legacyChartLayerMediaType = "application/tar+gzip"
	maxManifestBytes          = 4 << 20
)

// Options configures a Client.
type Options struct {
	CredentialsFile string // Helm's registry config.json; a missing file means anonymous
	PlainHTTP       bool
	Limits          Limits
	RequestTimeout  time.Duration
	UserAgent       string
	HTTP            *HTTP // shared clients; nil builds them from Limits and RequestTimeout
}

// ChartManifest is what forge needs from a chart's manifest.
type ChartManifest struct {
	Manifest digest.Digest
	Layer    ocispec.Descriptor
}

// Client is safe for concurrent use. One Client serves a whole run so every
// request shares its connections, limits and tokens.
type Client struct {
	auth      *auth.Client
	plainHTTP bool
	http      *HTTP

	mu    sync.Mutex
	repos map[string]*remote.Repository
}

// DefaultCredentialsFile is where Helm keeps `helm registry login` credentials.
func DefaultCredentialsFile() string {
	if v := os.Getenv("HELM_REGISTRY_CONFIG"); v != "" {
		return v
	}
	return helmpath.ConfigPath("registry/config.json")
}

// New builds a Client.
func New(o Options) (*Client, error) {
	store, err := credentials.NewStore(o.CredentialsFile, credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("load registry credentials %s: %w", o.CredentialsFile, err)
	}
	h := o.HTTP
	if h == nil {
		h = NewHTTP(o.Limits, o.RequestTimeout)
	}
	ac := &auth.Client{
		Client:     h.Client(),
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(store),
	}
	ac.SetUserAgent(o.UserAgent)
	return &Client{auth: ac, plainHTTP: o.PlainHTTP, http: h, repos: map[string]*remote.Repository{}}, nil
}

// Stats reports the requests sent so far through the client's HTTP, per
// host (see HTTP.Stats). When HTTP is shared, that includes chart repositories.
func (c *Client) Stats() []HostStats { return c.http.Stats() }

// RepoRef turns a Chart.yaml repository URL and chart name into an OCI
// repository reference: ("oci://ghcr.io/acme/charts", "redis") → "ghcr.io/acme/charts/redis".
func RepoRef(repository, name string) (string, error) {
	if !strings.HasPrefix(repository, "oci://") {
		return "", fmt.Errorf("repository %q is not an oci:// URL", repository)
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(repository, "oci://"), "/") + "/" + name
	if _, err := registry.ParseReference(ref); err != nil {
		return "", fmt.Errorf("invalid OCI repository %q: %w", repository, err)
	}
	return ref, nil
}

// Host is the registry host of repo, as used for auth scopes and limits.
func Host(repo string) string {
	ref, err := registry.ParseReference(repo)
	if err != nil {
		return repo
	}
	return ref.Host()
}

// WithPullScopes asks for pull access to every repo in the first token
// request per registry, so later requests reuse that one token.
func WithPullScopes(ctx context.Context, repos []string) context.Context {
	byHost := map[string][]string{}
	for _, r := range repos {
		ref, err := registry.ParseReference(r)
		if err != nil {
			continue
		}
		byHost[ref.Host()] = append(byHost[ref.Host()], auth.ScopeRepository(ref.Repository, auth.ActionPull))
	}
	for host, scopes := range byHost {
		ctx = auth.AppendScopesForHost(ctx, host, scopes...)
	}
	return ctx
}

// Tags lists repo's chart versions the way Helm does: only strict semver
// tags ("_" read as "+"), highest first.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	r, err := c.repo(repo)
	if err != nil {
		return nil, err
	}
	var vs []*semver.Version
	err = r.Tags(ctx, "", func(tags []string) error {
		for _, t := range tags {
			if v, err := semver.StrictNewVersion(strings.ReplaceAll(t, "_", "+")); err == nil {
				vs = append(vs, v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(semver.Collection(vs)))
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.String()
	}
	return out, nil
}

// ChartManifest fetches repo:version's manifest and picks its chart layer.
func (c *Client) ChartManifest(ctx context.Context, repo, version string) (ChartManifest, error) {
	r, err := c.repo(repo)
	if err != nil {
		return ChartManifest{}, err
	}
	desc, rc, err := r.FetchReference(ctx, strings.ReplaceAll(version, "+", "_"))
	if err != nil {
		return ChartManifest{}, err
	}
	defer rc.Close()
	var m ocispec.Manifest
	if err := json.NewDecoder(io.LimitReader(rc, maxManifestBytes)).Decode(&m); err != nil {
		return ChartManifest{}, fmt.Errorf("decode manifest %s:%s: %w", repo, version, err)
	}
	types := make([]string, 0, len(m.Layers))
	for _, l := range m.Layers {
		if l.MediaType == chartLayerMediaType || l.MediaType == legacyChartLayerMediaType {
			return ChartManifest{Manifest: desc.Digest, Layer: l}, nil
		}
		types = append(types, l.MediaType)
	}
	return ChartManifest{}, &NotChartError{Ref: repo + ":" + version, MediaTypes: types}
}

// OpenBlob streams a layer. The caller verifies its digest.
func (c *Client) OpenBlob(ctx context.Context, repo string, layer ocispec.Descriptor) (io.ReadCloser, error) {
	r, err := c.repo(repo)
	if err != nil {
		return nil, err
	}
	return r.Blobs().Fetch(ctx, layer)
}

func (c *Client) repo(ref string) (*remote.Repository, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.repos[ref]; ok {
		return r, nil
	}
	r, err := remote.NewRepository(ref)
	if err != nil {
		return nil, err
	}
	r.Client = c.auth
	r.PlainHTTP = c.plainHTTP
	c.repos[ref] = r
	return r, nil
}
