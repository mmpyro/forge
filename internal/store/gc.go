package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
)

// DefaultGCGrace is how recently a blob or ref must have been used for GC
// to keep it regardless of limits: a concurrent run may be between finding
// it in the cache and placing it into charts/.
const DefaultGCGrace = time.Hour

// Sources of a cache entry.
const (
	SourceOCI  = "oci"
	SourceRepo = "repo"
)

// Entry is one cached ref and what it points to.
type Entry struct {
	Key         string // OCI repository ("ghcr.io/acme/charts/redis") or chart repository key ("https/host/path/redis")
	Version     string
	Source      string // SourceOCI or SourceRepo
	Ref         Ref    // zero when the ref file is unreadable
	Valid       bool   // the ref file parsed
	LastUsed    time.Time
	BlobPresent bool
	BlobSize    int64
}

// Name is the chart name: the last element of Key.
func (e Entry) Name() string { return e.Key[strings.LastIndex(e.Key, "/")+1:] }

// Repository is the repository URL the chart came from, as written in
// Chart.yaml: oci://host/path or scheme://host/path.
func (e Entry) Repository() string {
	dir := ""
	if i := strings.LastIndex(e.Key, "/"); i >= 0 {
		dir = e.Key[:i]
	}
	if e.Source == SourceRepo {
		scheme, rest, _ := strings.Cut(dir, "/")
		return scheme + "://" + rest
	}
	return "oci://" + dir
}

// Touch marks the ref for key at version as used now. It is best effort:
// a read-only cache (e.g. restored by CI) still serves hits.
func (s *Store) Touch(key, version string) {
	p, err := s.refPath(key, version)
	if err != nil {
		return
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
}

// Entries lists every ref in the cache, sorted by key and version. A ref's
// LastUsed is its file's modification time: set when it is written and
// advanced by Touch on every cache hit.
func (s *Store) Entries() ([]Entry, error) {
	refs := filepath.Join(s.root, "refs")
	var out []Entry
	err := filepath.WalkDir(refs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) { // removed by a concurrent GC
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(refs, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		i := strings.LastIndex(rel, "/")
		if i < 0 {
			return nil
		}
		out = append(out, s.entry(rel[:i], rel[i+1:], p, info.ModTime()))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// Lookup returns the entry for key at version, if its ref file exists.
func (s *Store) Lookup(key, version string) (Entry, bool) {
	p, err := s.refPath(key, version)
	if err != nil {
		return Entry{}, false
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return Entry{}, false
	}
	return s.entry(key, version, p, info.ModTime()), true
}

func (s *Store) entry(key, version, refFile string, modTime time.Time) Entry {
	e := Entry{Key: key, Version: version, Source: SourceOCI, LastUsed: modTime}
	if first, _, _ := strings.Cut(key, "/"); first == "http" || first == "https" {
		e.Source = SourceRepo
	}
	if b, err := os.ReadFile(refFile); err == nil && json.Unmarshal(b, &e.Ref) == nil && e.Ref.Layer.Validate() == nil {
		e.Valid = true
		if bi, err := os.Stat(s.BlobPath(e.Ref.Layer)); err == nil {
			e.BlobPresent, e.BlobSize = true, bi.Size()
		}
	} else {
		e.Ref = Ref{}
	}
	return e
}

// GCOptions limits what GC keeps. At least one of MaxSize and OlderThan
// should be set; with neither, GC only removes orphans.
type GCOptions struct {
	MaxSize   int64         // keep at most this many blob bytes; 0 means no limit
	OlderThan time.Duration // remove refs not used for this long; 0 means no limit
	Grace     time.Duration // never remove what was used or written this recently
	DryRun    bool          // report what would be removed, remove nothing
	Now       time.Time     // zero means time.Now()
}

// GCResult is what GC removed (or, with DryRun, would remove).
type GCResult struct {
	Removed      []Entry // refs, sorted by key and version
	BlobsRemoved int
	BytesFreed   int64
	BlobsKept    int
	BytesKept    int64
}

type gcBlob struct {
	path     string
	size     int64
	modTime  time.Time
	lastUsed time.Time // newest LastUsed of the refs kept for it
	refs     []int     // kept refs pointing at it
	remove   bool
}

// GC removes refs that are invalid, point at a missing blob or were not
// used within OlderThan, then blobs no kept ref points at, then the least
// recently used blobs (and their refs) until at most MaxSize bytes remain.
// Anything used or written within Grace is kept. Like the rest of the store
// it takes no locks: a run that loses a blob to GC sees a cache miss.
func (s *Store) GC(o GCOptions) (GCResult, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	graceCutoff := now.Add(-o.Grace)
	entries, err := s.Entries()
	if err != nil {
		return GCResult{}, err
	}
	blobs, err := s.blobs()
	if err != nil {
		return GCResult{}, err
	}

	removeRef := make([]bool, len(entries))
	for i, e := range entries {
		switch {
		case !e.Valid || blobs[e.Ref.Layer] == nil:
			removeRef[i] = true
		case o.OlderThan > 0 && e.LastUsed.Before(now.Add(-o.OlderThan)) && e.LastUsed.Before(graceCutoff):
			removeRef[i] = true
		default:
			b := blobs[e.Ref.Layer]
			b.refs = append(b.refs, i)
			if e.LastUsed.After(b.lastUsed) {
				b.lastUsed = e.LastUsed
			}
		}
	}

	var kept []*gcBlob
	var keptBytes int64
	for _, b := range blobs {
		if len(b.refs) == 0 && b.modTime.Before(graceCutoff) {
			b.remove = true
			continue
		}
		kept = append(kept, b)
		keptBytes += b.size
	}
	if o.MaxSize > 0 && keptBytes > o.MaxSize {
		sort.Slice(kept, func(i, j int) bool {
			a, b := kept[i], kept[j]
			if !a.lastUsed.Equal(b.lastUsed) {
				return a.lastUsed.Before(b.lastUsed)
			}
			if a.size != b.size {
				return a.size > b.size
			}
			return a.path < b.path
		})
		for _, b := range kept {
			if keptBytes <= o.MaxSize {
				break
			}
			if !b.lastUsed.Before(graceCutoff) || !b.modTime.Before(graceCutoff) {
				continue
			}
			b.remove = true
			keptBytes -= b.size
			for _, i := range b.refs {
				removeRef[i] = true
			}
		}
	}

	var res GCResult
	for i, e := range entries {
		if removeRef[i] {
			res.Removed = append(res.Removed, e)
		}
	}
	for _, b := range blobs {
		if b.remove {
			res.BlobsRemoved++
			res.BytesFreed += b.size
		} else {
			res.BlobsKept++
			res.BytesKept += b.size
		}
	}
	if o.DryRun {
		return res, nil
	}

	for _, e := range res.Removed {
		p, err := s.refPath(e.Key, e.Version)
		if err != nil {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, err
		}
	}
	for _, b := range blobs {
		if !b.remove {
			continue
		}
		if runtime.GOOS == "windows" { // read-only files can't be removed there
			_ = os.Chmod(b.path, 0o644)
		}
		if err := os.Remove(b.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, err
		}
	}
	s.pruneRefDirs()
	return res, nil
}

func (s *Store) blobs() (map[digest.Digest]*gcBlob, error) {
	dir := filepath.Join(s.root, "blobs", string(digest.SHA256))
	des, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out := map[digest.Digest]*gcBlob{}
	for _, de := range des {
		d := digest.NewDigestFromEncoded(digest.SHA256, de.Name())
		if d.Validate() != nil || !de.Type().IsRegular() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out[d] = &gcBlob{path: filepath.Join(dir, de.Name()), size: info.Size(), modTime: info.ModTime()}
	}
	return out, nil
}

// pruneRefDirs removes empty directories under refs/, deepest first.
// PutRef recreates a directory it loses to this.
func (s *Store) pruneRefDirs() {
	refs := filepath.Join(s.root, "refs")
	var dirs []string
	_ = filepath.WalkDir(refs, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != refs {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d) // fails, as intended, when not empty
	}
}
