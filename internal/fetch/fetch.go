// Package fetch downloads chart archives into the cache in parallel.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/singleflight"

	"github.com/mmarszalek/helm-forge/internal/chartrepo"
	"github.com/mmarszalek/helm-forge/internal/par"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/store"
)

// Registry is the part of registry.Client that fetching needs.
type Registry interface {
	ChartManifest(ctx context.Context, repo, version string) (registry.ChartManifest, error)
	OpenBlob(ctx context.Context, repo string, layer ocispec.Descriptor) (io.ReadCloser, error)
}

// ChartRepos is the part of chartrepo.Client that fetching needs.
type ChartRepos interface {
	ChartURL(ctx context.Context, repoURL, name, version string) (string, error)
	Open(ctx context.Context, repoURL, chartURL string) (io.ReadCloser, error)
}

// Item is one chart to fetch, as locked in Chart.lock.
type Item struct {
	Name       string
	Version    string
	Repository string // oci:// or http(s):// URL
}

// FileName is the archive name Helm uses in charts/ for OCI charts.
func (i Item) FileName() string { return i.Name + "-" + i.Version + ".tgz" }

// Result is the outcome for one Item.
type Result struct {
	Item     Item
	BlobPath string
	FileName string // archive name in charts/
	Cached   bool   // served from the cache without any request
	Digest   digest.Digest
	Size     int64
	Duration time.Duration // time spent on this chart; aliases share it
	Err      error
}

// Fetcher downloads into Store. A zero Fetcher is not usable; set Store,
// and Registry and Repos for the kinds of repository in use.
type Fetcher struct {
	Registry Registry
	Repos    ChartRepos
	Store    *store.Store
	Refresh  bool // ignore cached tag→digest mappings

	flight singleflight.Group
}

type job struct {
	key     string // cache key: OCI repository, or chartrepo.CacheKey
	oci     bool
	repoURL string // chart repositories
	name    string
	version string
	idx     []int // positions in the input sharing this chart
}

func (j *job) host() string {
	if j.oci {
		return registry.Host(j.key)
	}
	return chartrepo.Host(j.repoURL)
}

// Fetch resolves every item to a cached blob. Results are in input order;
// a failed item does not stop the others.
func (f *Fetcher) Fetch(ctx context.Context, items []Item) []Result {
	results := make([]Result, len(items))
	jobs := map[string]*job{}
	var order []*job
	for i, it := range items {
		results[i].Item = it
		j, err := newJob(it)
		if err != nil {
			results[i].Err = err
			continue
		}
		id := j.key + ":" + it.Version
		if prev, ok := jobs[id]; ok {
			prev.idx = append(prev.idx, i)
			continue
		}
		j.idx = []int{i}
		jobs[id] = j
		order = append(order, j)
	}
	set := func(j *job, r Result) {
		if r.Err == nil && r.FileName == "" {
			r.FileName = j.name + "-" + j.version + ".tgz"
		}
		for _, i := range j.idx {
			r.Item = results[i].Item
			results[i] = r
		}
	}

	var misses []*job
	for _, j := range order {
		if !f.Refresh {
			start := time.Now()
			if ref, ok := f.Store.GetRef(j.key, j.version); ok && f.Store.HasBlob(ref.Layer) && (j.oci || ref.File != "") {
				set(j, Result{BlobPath: f.Store.BlobPath(ref.Layer), FileName: ref.File, Cached: true, Digest: ref.Layer, Size: ref.Size, Duration: time.Since(start)})
				continue
			}
		}
		misses = append(misses, j)
	}
	if len(misses) == 0 {
		return results
	}

	var repos []string
	for _, j := range misses {
		if j.oci {
			repos = append(repos, j.key)
		}
	}
	ctx = registry.WithPullScopes(ctx, repos)
	par.ByHost(ctx, misses, (*job).host, func(ctx context.Context, j *job) {
		start := time.Now()
		var r Result
		if j.oci {
			r = f.fetchOCI(ctx, j.key, j.version)
		} else {
			r = f.fetchRepo(ctx, j)
		}
		r.Duration = time.Since(start)
		set(j, r) // each job owns distinct indices
	})
	return results
}

func newJob(it Item) (*job, error) {
	if strings.HasPrefix(it.Repository, "oci://") {
		repo, err := registry.RepoRef(it.Repository, it.Name)
		if err != nil {
			return nil, err
		}
		return &job{key: repo, oci: true, name: it.Name, version: it.Version}, nil
	}
	if !chartrepo.IsRepoURL(it.Repository) {
		return nil, fmt.Errorf("repository %q is neither an oci:// nor an http(s):// URL", it.Repository)
	}
	return &job{key: chartrepo.CacheKey(it.Repository, it.Name), repoURL: it.Repository, name: it.Name, version: it.Version}, nil
}

func (f *Fetcher) fetchOCI(ctx context.Context, repo, version string) Result {
	cm, err := f.Registry.ChartManifest(ctx, repo, version)
	if err != nil {
		return Result{Err: err}
	}
	path, err := f.blob(ctx, repo, cm.Layer)
	if err != nil {
		return Result{Err: err}
	}
	ref := store.Ref{Manifest: cm.Manifest, Layer: cm.Layer.Digest, Size: cm.Layer.Size, FetchedAt: time.Now().UTC()}
	if err := f.Store.PutRef(repo, version, ref); err != nil {
		return Result{Err: fmt.Errorf("write cache entry: %w", err)}
	}
	return Result{BlobPath: path, Digest: cm.Layer.Digest, Size: cm.Layer.Size}
}

// fetchRepo downloads a chart repository archive. The index gives no
// trustworthy digest up front, so the archive is hashed as it is stored.
func (f *Fetcher) fetchRepo(ctx context.Context, j *job) Result {
	chartURL, err := f.Repos.ChartURL(ctx, j.repoURL, j.name, j.version)
	if err != nil {
		return Result{Err: err}
	}
	file, err := archiveName(chartURL)
	if err != nil {
		return Result{Err: err}
	}
	v, err, _ := f.flight.Do("url:"+chartURL, func() (any, error) {
		rc, err := f.Repos.Open(ctx, j.repoURL, chartURL)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		d, n, _, err := f.Store.PutBlobStream(rc)
		if err != nil {
			return nil, err
		}
		return store.Ref{Layer: d, Size: n, File: file, FetchedAt: time.Now().UTC()}, nil
	})
	if err != nil {
		return Result{Err: err}
	}
	ref := v.(store.Ref)
	if err := f.Store.PutRef(j.key, j.version, ref); err != nil {
		return Result{Err: fmt.Errorf("write cache entry: %w", err)}
	}
	return Result{BlobPath: f.Store.BlobPath(ref.Layer), FileName: file, Digest: ref.Layer, Size: ref.Size}
}

// archiveName is the file name Helm saves chartURL under: its last path
// element.
func archiveName(chartURL string) (string, error) {
	u, err := url.Parse(chartURL)
	if err != nil {
		return "", err
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" || name == ".." {
		return "", fmt.Errorf("chart URL %s has no file name", chartURL)
	}
	return name, nil
}

// blob returns layer's cached path, downloading it at most once even when
// several charts share it. A download that fails verification is retried once.
func (f *Fetcher) blob(ctx context.Context, repo string, layer ocispec.Descriptor) (string, error) {
	if f.Store.HasBlob(layer.Digest) {
		return f.Store.BlobPath(layer.Digest), nil
	}
	v, err, _ := f.flight.Do(layer.Digest.String(), func() (any, error) {
		var err error
		for range 2 {
			var path string
			path, err = f.download(ctx, repo, layer)
			if !errors.Is(err, store.ErrDigestMismatch) {
				return path, err
			}
		}
		return "", err
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (f *Fetcher) download(ctx context.Context, repo string, layer ocispec.Descriptor) (string, error) {
	rc, err := f.Registry.OpenBlob(ctx, repo, layer)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return f.Store.PutBlob(layer.Digest, rc)
}
