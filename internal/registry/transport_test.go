package registry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type peak struct{ cur, max atomic.Int32 }

func (p *peak) handler(w http.ResponseWriter, _ *http.Request) {
	n := p.cur.Add(1)
	defer p.cur.Add(-1)
	for {
		m := p.max.Load()
		if n <= m || p.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
}

func getAll(t *testing.T, c *http.Client, urls ...string) {
	t.Helper()
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get(u)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestLimitTransportCapsInFlightPerHost(t *testing.T) {
	var p peak
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	defer srv.Close()
	c := &http.Client{Transport: newLimitTransport(http.DefaultTransport, Limits{Global: 16, PerHost: 2})}
	getAll(t, c, repeat(srv.URL, 10)...)
	if got := p.max.Load(); got != 2 {
		t.Fatalf("peak in-flight = %d, want 2", got)
	}
}

func TestLimitTransportCapsInFlightGlobally(t *testing.T) {
	var p peak
	a := httptest.NewServer(http.HandlerFunc(p.handler))
	b := httptest.NewServer(http.HandlerFunc(p.handler))
	defer a.Close()
	defer b.Close()
	c := &http.Client{Transport: newLimitTransport(http.DefaultTransport, Limits{Global: 3, PerHost: 8})}
	getAll(t, c, append(repeat(a.URL, 6), repeat(b.URL, 6)...)...)
	if got := p.max.Load(); got != 3 {
		t.Fatalf("peak in-flight = %d, want 3", got)
	}
}

func TestLimitTransportHoldsSlotUntilBodyClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	c := &http.Client{Transport: newLimitTransport(http.DefaultTransport, Limits{Global: 16, PerHost: 1})}
	first, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		if resp, err := c.Get(srv.URL); err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("second request ran while the first body was still open")
	case <-time.After(100 * time.Millisecond):
	}
	first.Body.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second request never ran")
	}
}

func scripted(t *testing.T, steps ...http.HandlerFunc) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := min(int(hits.Add(1))-1, len(steps)-1)
		steps[i](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

func status(code int, kv ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(kv); i += 2 {
			w.Header().Set(kv[i], kv[i+1])
		}
		w.WriteHeader(code)
	}
}

func reset(w http.ResponseWriter, _ *http.Request) {
	conn, _, _ := w.(http.Hijacker).Hijack()
	conn.Close()
}

func testClient() *http.Client {
	return newHTTPClient(Limits{Global: 16, PerHost: 8}, 10*time.Second, newStats())
}

func TestStatsCountAttemptsRetriesAndChallenges(t *testing.T) {
	url, _ := scripted(t, status(503), status(401), status(200))
	st := newStats()
	c := newHTTPClient(Limits{Global: 4, PerHost: 4}, 10*time.Second, st)
	for range 2 { // 503 → retried → 401 (not retried); then 200
		resp, err := c.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	got := st.snapshot()
	if len(got) != 1 {
		t.Fatalf("hosts = %+v", got)
	}
	if g := got[0]; g.Requests != 3 || g.Retries != 1 || g.AuthRounds != 1 {
		t.Fatalf("stats = %+v, want 3 requests, 1 retry, 1 auth round", g)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := testClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestRetriesAfter429HonouringRetryAfter(t *testing.T) {
	url, hits := scripted(t, status(429, "Retry-After", "1"), status(200))
	start := time.Now()
	if resp := mustGet(t, url); resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if time.Since(start) < time.Second {
		t.Fatal("Retry-After: 1 was not honoured")
	}
}

func TestRetriesAfterConnectionReset(t *testing.T) {
	url, hits := scripted(t, reset, status(200))
	if resp := mustGet(t, url); resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestGivesUpAfterThreeRetries(t *testing.T) {
	url, hits := scripted(t, status(503))
	if resp := mustGet(t, url); resp.StatusCode != 503 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if hits.Load() != 4 {
		t.Fatalf("hits = %d, want 4 (1 + 3 retries)", hits.Load())
	}
}

func TestDoesNotRetry404(t *testing.T) {
	url, hits := scripted(t, status(404))
	mustGet(t, url)
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestDoesNotRetryCanceledRequest(t *testing.T) {
	url, _ := scripted(t, status(200))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if _, err := testClient().Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
