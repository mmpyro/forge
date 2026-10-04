// Package store is forge's content-addressed cache.
//
// Layout under the root:
//
//	blobs/sha256/<hex>               chart archives, read-only
//	refs/<registry>/<repo>/<version> JSON Ref: what a tag pointed to
//	refs/<scheme>/<host>/<path>/<chart>/<version>  same for chart repositories
//	tmp/                             in-flight writes
//
// Every write lands in tmp/ and is renamed into place, so concurrent forge
// processes can share one cache without locks.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
)

// ErrDigestMismatch means downloaded content did not match its digest.
var ErrDigestMismatch = errors.New("digest mismatch")

// Ref records what a tag pointed to when forge last fetched it.
type Ref struct {
	Manifest  digest.Digest `json:"manifest,omitempty"` // OCI only
	Layer     digest.Digest `json:"layer"`
	Size      int64         `json:"size"`
	File      string        `json:"file,omitempty"` // chart repositories: archive name from the chart URL
	FetchedAt time.Time     `json:"fetched_at"`
}

// Store is a cache rooted at one directory.
type Store struct{ root string }

// DefaultRoot is $HELM_FORGE_CACHE, else ~/.cache/helm-forge.
func DefaultRoot() (string, error) {
	if v := os.Getenv("HELM_FORGE_CACHE"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find cache directory (set HELM_FORGE_CACHE): %w", err)
	}
	return filepath.Join(home, ".cache", "helm-forge"), nil
}

// Open creates the cache layout under root if needed and removes temp files
// older than an hour, left behind by crashed runs.
func Open(root string) (*Store, error) {
	for _, d := range []string{filepath.Join("blobs", "sha256"), "refs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, fmt.Errorf("create cache directory: %w", err)
		}
	}
	s := &Store{root: root}
	s.sweepTmp(time.Hour)
	return s, nil
}

// Clean deletes the cache at root.
func Clean(root string) error { return os.RemoveAll(root) }

// Root is the cache directory.
func (s *Store) Root() string { return s.root }

// BlobPath is where the blob with digest d lives (whether or not it exists).
func (s *Store) BlobPath(d digest.Digest) string {
	return filepath.Join(s.root, "blobs", string(d.Algorithm()), d.Encoded())
}

// HasBlob reports whether the blob with digest d is cached.
func (s *Store) HasBlob(d digest.Digest) bool {
	if d.Validate() != nil {
		return false
	}
	_, err := os.Stat(s.BlobPath(d))
	return err == nil
}

// PutBlob streams r into the cache, verifying it against want, and returns
// the blob's path. Content that does not match want is discarded and
// ErrDigestMismatch returned.
func (s *Store) PutBlob(want digest.Digest, r io.Reader) (string, error) {
	if err := want.Validate(); err != nil {
		return "", err
	}
	if want.Algorithm() != digest.SHA256 {
		return "", fmt.Errorf("unsupported digest algorithm %s", want.Algorithm())
	}
	dst := s.BlobPath(want)
	if s.HasBlob(want) {
		return dst, nil
	}
	tmp, err := s.writeTemp(func(w io.Writer) error {
		v := want.Verifier()
		if _, err := io.Copy(io.MultiWriter(w, v), r); err != nil {
			return err
		}
		if !v.Verified() {
			return fmt.Errorf("%w: expected %s", ErrDigestMismatch, want)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o444); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

// PutBlobStream streams r into the cache when its digest is not known up
// front, and returns the digest, size and path of the stored blob.
func (s *Store) PutBlobStream(r io.Reader) (digest.Digest, int64, string, error) {
	var (
		d digest.Digest
		n int64
	)
	tmp, err := s.writeTemp(func(w io.Writer) error {
		dg := digest.SHA256.Digester()
		var err error
		n, err = io.Copy(io.MultiWriter(w, dg.Hash()), r)
		d = dg.Digest()
		return err
	})
	if err != nil {
		return "", 0, "", err
	}
	dst := s.BlobPath(d)
	if s.HasBlob(d) {
		os.Remove(tmp)
		return d, n, dst, nil
	}
	if err := os.Chmod(tmp, 0o444); err != nil {
		os.Remove(tmp)
		return "", 0, "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", 0, "", err
	}
	return d, n, dst, nil
}

// TempDir is the cache's scratch directory, on the same file system as the
// blobs. Callers remove what they create there.
func (s *Store) TempDir() string { return filepath.Join(s.root, "tmp") }

// GetRef returns the cached ref for repo at version. Missing or unreadable
// entries report false.
func (s *Store) GetRef(repo, version string) (Ref, bool) {
	p, err := s.refPath(repo, version)
	if err != nil {
		return Ref{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Ref{}, false
	}
	var ref Ref
	if json.Unmarshal(b, &ref) != nil || ref.Layer.Validate() != nil {
		return Ref{}, false
	}
	return ref, true
}

// PutRef records ref for repo at version.
func (s *Store) PutRef(repo, version string, ref Ref) error {
	p, err := s.refPath(repo, version)
	if err != nil {
		return err
	}
	b, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := s.writeTemp(func(w io.Writer) error {
		_, err := w.Write(b)
		return err
	})
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) refPath(repo, version string) (string, error) {
	if repo == "" || version == "" || version == "." || version == ".." ||
		strings.Contains(repo, "..") || strings.ContainsAny(version, `/\`) {
		return "", fmt.Errorf("invalid cache key %q %q", repo, version)
	}
	return filepath.Join(s.root, "refs", filepath.FromSlash(repo), version), nil
}

// writeTemp creates a file in tmp/, fills it, and returns its path. On error
// the file is removed.
func (s *Store) writeTemp(fill func(io.Writer) error) (string, error) {
	f, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "part-*")
	if err != nil {
		return "", err
	}
	err = fill(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (s *Store) sweepTmp(maxAge time.Duration) {
	dir := filepath.Join(s.root, "tmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}
