package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/mmarszalek/helm-forge/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func assertEmptyTmp(t *testing.T, s *store.Store) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.Root(), "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("tmp/ not empty: %v", entries)
	}
}

func TestPutBlobStoresVerifiedReadOnlyFile(t *testing.T) {
	s := newStore(t)
	data := []byte("chart-bytes")
	d := digest.FromBytes(data)
	p, err := s.PutBlob(d, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if p != s.BlobPath(d) || !strings.HasSuffix(p, filepath.Join("blobs", "sha256", d.Encoded())) {
		t.Fatalf("path = %s", p)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q", got)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %v, want 0444", info.Mode().Perm())
	}
	if !s.HasBlob(d) {
		t.Fatal("HasBlob = false")
	}
	assertEmptyTmp(t, s)
}

func TestPutBlobRejectsDigestMismatch(t *testing.T) {
	s := newStore(t)
	d := digest.FromBytes([]byte("expected"))
	_, err := s.PutBlob(d, strings.NewReader("something else"))
	if !errors.Is(err, store.ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", err)
	}
	if s.HasBlob(d) {
		t.Fatal("mismatched blob must not be stored")
	}
	assertEmptyTmp(t, s)
}

func TestPutBlobReaderErrorLeavesNoTempFile(t *testing.T) {
	s := newStore(t)
	d := digest.FromBytes([]byte("x"))
	if _, err := s.PutBlob(d, iotest.ErrReader(errors.New("connection reset"))); err == nil {
		t.Fatal("want error")
	}
	assertEmptyTmp(t, s)
}

func TestPutBlobConcurrentWritersSameDigest(t *testing.T) {
	s := newStore(t)
	data := bytes.Repeat([]byte("x"), 1<<20)
	d := digest.FromBytes(data)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutBlob(d, bytes.NewReader(data)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Root(), "blobs", "sha256"))
	if len(entries) != 1 {
		t.Fatalf("blobs = %d, want 1", len(entries))
	}
	got, _ := os.ReadFile(s.BlobPath(d))
	if !bytes.Equal(got, data) {
		t.Fatal("blob content corrupted")
	}
	assertEmptyTmp(t, s)
}

func TestPutBlobStreamHashesWhileStoring(t *testing.T) {
	s := newStore(t)
	data := []byte("chart archive bytes")
	for range 2 { // the second write finds the blob already there
		d, n, p, err := s.PutBlobStream(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if d != digest.FromBytes(data) || n != int64(len(data)) || p != s.BlobPath(d) {
			t.Fatalf("got %s %d %s", d, n, p)
		}
		if got, _ := os.ReadFile(p); !bytes.Equal(got, data) {
			t.Fatal("content differs")
		}
	}
	assertEmptyTmp(t, s)
	if _, _, _, err := s.PutBlobStream(iotest.ErrReader(errors.New("boom"))); err == nil {
		t.Fatal("want reader error")
	}
	assertEmptyTmp(t, s)
}

func TestRefsRoundTrip(t *testing.T) {
	s := newStore(t)
	want := store.Ref{
		Manifest:  digest.FromString("m"),
		Layer:     digest.FromString("l"),
		Size:      42,
		FetchedAt: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
	}
	if err := s.PutRef("localhost:5001/charts/dep-a", "1.0.0+build.1", want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetRef("localhost:5001/charts/dep-a", "1.0.0+build.1")
	if !ok || got.Manifest != want.Manifest || got.Layer != want.Layer || got.Size != 42 || !got.FetchedAt.Equal(want.FetchedAt) {
		t.Fatalf("got %+v, %v", got, ok)
	}
	if _, ok := s.GetRef("localhost:5001/charts/dep-a", "2.0.0"); ok {
		t.Fatal("missing ref reported as present")
	}
	assertEmptyTmp(t, s)
}

func TestGetRefIgnoresCorruptEntry(t *testing.T) {
	s := newStore(t)
	p := filepath.Join(s.Root(), "refs", "host", "charts", "dep-a", "1.0.0")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("{not json"), 0o644)
	if _, ok := s.GetRef("host/charts/dep-a", "1.0.0"); ok {
		t.Fatal("corrupt ref reported as present")
	}
}

func TestRefRejectsPathTraversal(t *testing.T) {
	s := newStore(t)
	if err := s.PutRef("../../etc", "passwd", store.Ref{Layer: digest.FromString("l")}); err == nil {
		t.Fatal("want error for path traversal")
	}
}

func TestOpenSweepsOnlyOldTempFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Open(root); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "tmp", "old")
	fresh := filepath.Join(root, "tmp", "fresh")
	_ = os.WriteFile(old, nil, 0o644)
	_ = os.WriteFile(fresh, nil, 0o644)
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, past, past)
	if _, err := store.Open(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old temp file not swept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh temp file was swept")
	}
}

func TestDefaultRootHonoursEnv(t *testing.T) {
	t.Setenv("HELM_FORGE_CACHE", "/somewhere/else")
	if got, _ := store.DefaultRoot(); got != "/somewhere/else" {
		t.Fatalf("got %s", got)
	}
	home := t.TempDir()
	t.Setenv("HELM_FORGE_CACHE", "")
	t.Setenv("HOME", home)
	if got, _ := store.DefaultRoot(); got != filepath.Join(home, ".cache", "helm-forge") {
		t.Fatalf("got %s", got)
	}
}

func TestDefaultRootAsHelmPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HELM_FORGE_CACHE", "")
	t.Setenv("HELM_CACHE_HOME", "/helm/cache")

	// HELM_CACHE_HOME alone (set in the user's shell) does not move the cache.
	t.Setenv("HELM_PLUGIN_DIR", "")
	if got, _ := store.DefaultRoot(); got != filepath.Join(home, ".cache", "helm-forge") {
		t.Fatalf("outside plugin: got %s", got)
	}

	t.Setenv("HELM_PLUGIN_DIR", "/helm/plugins/forge")
	if got, _ := store.DefaultRoot(); got != filepath.Join("/helm/cache", "forge") {
		t.Fatalf("as plugin: got %s", got)
	}

	t.Setenv("HELM_FORGE_CACHE", "/somewhere/else")
	if got, _ := store.DefaultRoot(); got != "/somewhere/else" {
		t.Fatalf("HELM_FORGE_CACHE must win: got %s", got)
	}
}

func TestCleanRemovesReadOnlyBlobs(t *testing.T) {
	root := t.TempDir()
	s, _ := store.Open(root)
	data := []byte("x")
	if _, err := s.PutBlob(digest.FromBytes(data), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Clean(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cache root still exists")
	}
}
