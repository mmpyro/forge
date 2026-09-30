// Package fetch downloads chart archives into the cache in parallel.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/singleflight"

	"github.com/mmarszalek/helm-forge/internal/par"
	"github.com/mmarszalek/helm-forge/internal/registry"
	"github.com/mmarszalek/helm-forge/internal/store"
)

// Registry is the part of registry.Client that fetching needs.
type Registry interface {
	ChartManifest(ctx context.Context, repo, version string) (registry.ChartManifest, error)
	OpenBlob(ctx context.Context, repo string, layer ocispec.Descriptor) (io.ReadCloser, error)
}

// Item is one chart to fetch, as locked in Chart.lock.
type Item struct {
	Name       string
	Version    string
	Repository string // oci:// URL from Chart.yaml
}

// FileName is the archive name Helm uses in charts/.
func (i Item) FileName() string { return i.Name + "-" + i.Version + ".tgz" }

// Result is the outcome for one Item.
type Result struct {
	Item     Item
	BlobPath string
	Cached   bool // served from the cache without any request
	Err      error
}

// Fetcher downloads into Store. A zero Fetcher is not usable; set Registry
// and Store.
type Fetcher struct {
	Registry Registry
	Store    *store.Store
	Refresh  bool // ignore cached tag→digest mappings

	flight singleflight.Group
}

type job struct {
	repo    string
	version string
	idx     []int // positions in the input sharing this chart
}

// Fetch resolves every item to a cached blob. Results are in input order;
// a failed item does not stop the others.
func (f *Fetcher) Fetch(ctx context.Context, items []Item) []Result {
	results := make([]Result, len(items))
	jobs := map[string]*job{}
	var order []*job
	for i, it := range items {
		results[i].Item = it
		repo, err := registry.RepoRef(it.Repository, it.Name)
		if err != nil {
			results[i].Err = err
			continue
		}
		key := repo + ":" + it.Version
		if j, ok := jobs[key]; ok {
			j.idx = append(j.idx, i)
			continue
		}
		j := &job{repo: repo, version: it.Version, idx: []int{i}}
		jobs[key] = j
		order = append(order, j)
	}
	set := func(j *job, path string, cached bool, err error) {
		for _, i := range j.idx {
			results[i].BlobPath, results[i].Cached, results[i].Err = path, cached, err
		}
	}

	var misses []*job
	for _, j := range order {
		if !f.Refresh {
			if ref, ok := f.Store.GetRef(j.repo, j.version); ok && f.Store.HasBlob(ref.Layer) {
				set(j, f.Store.BlobPath(ref.Layer), true, nil)
				continue
			}
		}
		misses = append(misses, j)
	}
	if len(misses) == 0 {
		return results
	}

	repos := make([]string, len(misses))
	for i, j := range misses {
		repos[i] = j.repo
	}
	ctx = registry.WithPullScopes(ctx, repos)
	par.ByHost(ctx, misses, func(j *job) string { return registry.Host(j.repo) }, func(ctx context.Context, j *job) {
		path, err := f.fetchOne(ctx, j.repo, j.version)
		set(j, path, false, err) // each job owns distinct indices
	})
	return results
}

func (f *Fetcher) fetchOne(ctx context.Context, repo, version string) (string, error) {
	cm, err := f.Registry.ChartManifest(ctx, repo, version)
	if err != nil {
		return "", err
	}
	path, err := f.blob(ctx, repo, cm.Layer)
	if err != nil {
		return "", err
	}
	ref := store.Ref{Manifest: cm.Manifest, Layer: cm.Layer.Digest, Size: cm.Layer.Size, FetchedAt: time.Now().UTC()}
	if err := f.Store.PutRef(repo, version, ref); err != nil {
		return "", fmt.Errorf("write cache entry: %w", err)
	}
	return path, nil
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
