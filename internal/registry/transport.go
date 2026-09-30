package registry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"oras.land/oras-go/v2/registry/remote/retry"
)

// Limits caps requests in flight. PerHost counts each host separately, so
// blob redirects (ECR → S3, GHCR → pkg-containers) get their own budget.
type Limits struct {
	Global  int
	PerHost int
}

// limitTransport holds a slot per request until its body is closed, because
// the transfer is still in progress after RoundTrip returns.
type limitTransport struct {
	base    http.RoundTripper
	global  chan struct{}
	perHost int

	mu    sync.Mutex
	hosts map[string]chan struct{}
}

func newLimitTransport(base http.RoundTripper, l Limits) *limitTransport {
	return &limitTransport{
		base:    base,
		global:  make(chan struct{}, l.Global),
		perHost: l.PerHost,
		hosts:   map[string]chan struct{}{},
	}
}

func (t *limitTransport) hostSlots(host string) chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.hosts[host]
	if !ok {
		s = make(chan struct{}, t.perHost)
		t.hosts[host] = s
	}
	return s
}

func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	host := t.hostSlots(req.URL.Host)
	// Take the host slot first so a request waiting on a busy host does not
	// hold a global slot another host could use.
	if err := acquire(ctx, host); err != nil {
		return nil, err
	}
	if err := acquire(ctx, t.global); err != nil {
		<-host
		return nil, err
	}
	release := sync.OnceFunc(func() { <-t.global; <-host })
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	resp.Body = &releasingBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

func acquire(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type releasingBody struct {
	io.ReadCloser
	release func()
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

// retryPolicy retries 408, 429, 5xx and network errors up to 3 times,
// backing off 200 ms × 2ⁿ with jitter, or for Retry-After (max 30 s).
func retryPolicy() retry.Policy {
	return &retry.GenericPolicy{
		Retryable: retryable,
		Backoff:   retry.ExponentialBackoff(200*time.Millisecond, 2, 0.2),
		MinWait:   200 * time.Millisecond,
		MaxWait:   30 * time.Second,
		MaxRetry:  3,
	}
}

func retryable(resp *http.Response, err error) (bool, error) {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return true, nil // connection reset, EOF, dial failure
	}
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return true, nil
	case resp.StatusCode >= 500:
		return true, nil
	}
	return false, nil
}

// newHTTPClient is the one client forge uses for a whole run: HTTP/2 when
// the server offers it, retries above the limiter (so a retry waits for a
// free slot), and a per-request timeout.
func newHTTPClient(l Limits, timeout time.Duration) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ForceAttemptHTTP2 = true
	base.MaxIdleConnsPerHost = l.PerHost
	return &http.Client{
		Transport: &retry.Transport{Base: newLimitTransport(base, l), Policy: retryPolicy},
		Timeout:   timeout,
	}
}
