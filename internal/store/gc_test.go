package store_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/mmarszalek/helm-forge/internal/store"
)

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// put caches data under key@version with the blob written and the ref last
// used age before now.
func put(t *testing.T, s *store.Store, key, version, data string, age time.Duration) digest.Digest {
	t.Helper()
	d := digest.FromString(data)
	p, err := s.PutBlob(d, bytes.NewReader([]byte(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRef(key, version, store.Ref{Layer: d, Size: int64(len(data)), FetchedAt: now.Add(-age)}); err != nil {
		t.Fatal(err)
	}
	when := now.Add(-age)
	refFile := filepath.Join(s.Root(), "refs", filepath.FromSlash(key), version)
	if err := os.Chtimes(refFile, when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return d
}

func refKeys(es []store.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Key + "@" + e.Version
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEntriesDescribesRefs(t *testing.T) {
	s := newStore(t)
	put(t, s, "ghcr.io/acme/charts/redis", "18.1.0", "redis", time.Hour)
	put(t, s, "https/charts.example.com/stable/nginx", "1.2.3+build.1", "nginx!", 2*time.Hour)
	if err := s.PutRef("ghcr.io/acme/charts/gone", "1.0.0", store.Ref{Layer: digest.FromString("nope")}); err != nil {
		t.Fatal(err)
	}

	es, err := s.Entries()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ghcr.io/acme/charts/gone@1.0.0", "ghcr.io/acme/charts/redis@18.1.0", "https/charts.example.com/stable/nginx@1.2.3+build.1"}
	if got := refKeys(es); !equal(got, want) {
		t.Fatalf("entries = %v", got)
	}
	gone, redis, nginx := es[0], es[1], es[2]
	if gone.BlobPresent || !gone.Valid {
		t.Fatalf("gone = %+v", gone)
	}
	if redis.Name() != "redis" || redis.Repository() != "oci://ghcr.io/acme/charts" || redis.Source != store.SourceOCI ||
		!redis.BlobPresent || redis.BlobSize != 5 || !redis.LastUsed.Equal(now.Add(-time.Hour)) {
		t.Fatalf("redis = %+v (%s, %s)", redis, redis.Name(), redis.Repository())
	}
	if nginx.Name() != "nginx" || nginx.Repository() != "https://charts.example.com/stable" || nginx.Source != store.SourceRepo {
		t.Fatalf("nginx = %+v (%s, %s)", nginx, nginx.Name(), nginx.Repository())
	}
}

func TestEntriesOfMissingCacheIsEmpty(t *testing.T) {
	s := newStore(t)
	_ = os.RemoveAll(filepath.Join(s.Root(), "refs"))
	if es, err := s.Entries(); err != nil || len(es) != 0 {
		t.Fatalf("entries = %v, %v", es, err)
	}
}

func TestTouchAdvancesLastUsed(t *testing.T) {
	s := newStore(t)
	put(t, s, "host/charts/a", "1.0.0", "a", 48*time.Hour)
	before := time.Now().Add(-time.Second)
	s.Touch("host/charts/a", "1.0.0")
	s.Touch("host/charts/missing", "1.0.0") // no ref: nothing happens
	es, _ := s.Entries()
	if len(es) != 1 || es[0].LastUsed.Before(before) {
		t.Fatalf("entries = %+v", es)
	}
}

func TestGCOlderThan(t *testing.T) {
	s := newStore(t)
	old := put(t, s, "host/charts/old", "1.0.0", "old", 40*24*time.Hour)
	fresh := put(t, s, "host/charts/fresh", "1.0.0", "fresh", 24*time.Hour)

	res, err := s.GC(store.GCOptions{OlderThan: 30 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := refKeys(res.Removed); !equal(got, []string{"host/charts/old@1.0.0"}) {
		t.Fatalf("removed = %v", got)
	}
	if res.BlobsRemoved != 1 || res.BytesFreed != 3 || res.BlobsKept != 1 || res.BytesKept != 5 {
		t.Fatalf("res = %+v", res)
	}
	if s.HasBlob(old) || !s.HasBlob(fresh) {
		t.Fatal("wrong blob removed")
	}
	if _, ok := s.GetRef("host/charts/old", "1.0.0"); ok {
		t.Fatal("old ref kept")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "refs", "host", "charts", "old")); !os.IsNotExist(err) {
		t.Fatal("empty ref directory not pruned")
	}
}

func TestGCKeepsSharedBlobUntilEveryRefExpires(t *testing.T) {
	s := newStore(t)
	d := put(t, s, "host/a/chart", "1.0.0", "same", 40*24*time.Hour)
	put(t, s, "host/b/chart", "1.0.0", "same", time.Hour*2)

	res, err := s.GC(store.GCOptions{OlderThan: 30 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.BlobsRemoved != 0 || !s.HasBlob(d) {
		t.Fatalf("res = %+v, blob present = %v", res, s.HasBlob(d))
	}
}

func TestGCMaxSizeEvictsLeastRecentlyUsed(t *testing.T) {
	s := newStore(t)
	put(t, s, "host/charts/a", "1.0.0", "aaaaaaaaaa", 5*time.Hour) // 10 bytes, oldest
	put(t, s, "host/charts/b", "1.0.0", "bbbbbbbbbb", 4*time.Hour)
	put(t, s, "host/charts/c", "1.0.0", "cccccccccc", 3*time.Hour)

	res, err := s.GC(store.GCOptions{MaxSize: 20, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := refKeys(res.Removed); !equal(got, []string{"host/charts/a@1.0.0"}) {
		t.Fatalf("removed = %v", got)
	}
	if res.BytesKept != 20 || res.BytesFreed != 10 {
		t.Fatalf("res = %+v", res)
	}
}

func TestGCGraceProtectsRecentUse(t *testing.T) {
	s := newStore(t)
	put(t, s, "host/charts/a", "1.0.0", "aaaaaaaaaa", 2*time.Hour)
	put(t, s, "host/charts/b", "1.0.0", "bbbbbbbbbb", 10*time.Minute)

	res, err := s.GC(store.GCOptions{MaxSize: 1, OlderThan: time.Second, Grace: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := refKeys(res.Removed); !equal(got, []string{"host/charts/a@1.0.0"}) {
		t.Fatalf("removed = %v", got)
	}
	if res.BytesKept != 10 {
		t.Fatalf("res = %+v", res)
	}
}

func TestGCRemovesOrphans(t *testing.T) {
	s := newStore(t)
	// ref without blob, corrupt ref, blob without ref (old and fresh).
	_ = s.PutRef("host/charts/noblob", "1.0.0", store.Ref{Layer: digest.FromString("missing")})
	corrupt := filepath.Join(s.Root(), "refs", "host", "charts", "corrupt", "1.0.0")
	_ = os.MkdirAll(filepath.Dir(corrupt), 0o755)
	_ = os.WriteFile(corrupt, []byte("{"), 0o644)
	oldBlob := digest.FromString("old orphan")
	p, _ := s.PutBlob(oldBlob, bytes.NewReader([]byte("old orphan")))
	past := now.Add(-2 * time.Hour)
	_ = os.Chtimes(p, past, past)
	freshBlob := digest.FromString("fresh orphan")
	p, _ = s.PutBlob(freshBlob, bytes.NewReader([]byte("fresh orphan")))
	_ = os.Chtimes(p, now, now)

	res, err := s.GC(store.GCOptions{Grace: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := refKeys(res.Removed); !equal(got, []string{"host/charts/corrupt@1.0.0", "host/charts/noblob@1.0.0"}) {
		t.Fatalf("removed = %v", got)
	}
	if s.HasBlob(oldBlob) || !s.HasBlob(freshBlob) {
		t.Fatal("orphan blobs: want old removed, fresh (within grace) kept")
	}
}

func TestGCDryRunRemovesNothing(t *testing.T) {
	s := newStore(t)
	d := put(t, s, "host/charts/old", "1.0.0", "old", 40*24*time.Hour)
	res, err := s.GC(store.GCOptions{OlderThan: time.Hour, DryRun: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.BlobsRemoved != 1 {
		t.Fatalf("res = %+v", res)
	}
	if _, ok := s.GetRef("host/charts/old", "1.0.0"); !ok || !s.HasBlob(d) {
		t.Fatal("dry run removed something")
	}
}

func TestGCConcurrentWithWrites(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.GC(store.GCOptions{MaxSize: 1, Now: time.Now().Add(time.Hour)}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := range 200 {
		data := []byte{byte(i), byte(i >> 8)}
		d := digest.FromBytes(data)
		if _, err := s.PutBlob(d, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRef("host/charts/c"+string(rune('a'+i%26)), "1.0.0", store.Ref{Layer: d, Size: 2}); err != nil {
			t.Fatalf("PutRef while GC prunes: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}
