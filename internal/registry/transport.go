package registry

import (
	"context"
	"crypto/tls"
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

// limiter hands out request slots from one global pool and one pool per
// host. Transports sharing a limiter share its budget.
type limiter struct {
	global  chan struct{}
	perHost int

	mu    sync.Mutex
	hosts map[string]chan struct{}
}

func newLimiter(l Limits) *limiter {
	return &limiter{global: make(chan struct{}, l.Global), perHost: l.PerHost, hosts: map[string]chan struct{}{}}
}

func (l *limiter) hostSlots(host string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.hosts[host]
	if !ok {
		s = make(chan struct{}, l.perHost)
		l.hosts[host] = s
	}
	return s
}

// limitTransport holds a slot per request until its body is closed, because
// the transfer is still in progress after RoundTrip returns.
type limitTransport struct {
	base http.RoundTripper
	lim  *limiter
}

func newLimitTransport(base http.RoundTripper, l Limits) *limitTransport {
	return &limitTransport{base: base, lim: newLimiter(l)}
}

func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	host := t.lim.hostSlots(req.URL.Host)
	// Take the host slot first so a request waiting on a busy host does not
	// hold a global slot another host could use.
	if err := acquire(ctx, host); err != nil {
		return nil, err
	}
	if err := acquire(ctx, t.lim.global); err != nil {
		<-host
		return nil, err
	}
	release := sync.OnceFunc(func() { <-t.lim.global; <-host })
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

// HTTP builds the HTTP clients of one run: HTTP/2 when the server offers
// it, retries above the limiter (so a retry waits for a free slot), and a
// per-request timeout. All its clients share one request budget, so OCI
// registries and chart repositories together respect the Limits.
type HTTP struct {
	lim     *limiter
	perHost int
	timeout time.Duration
	def     *http.Client
}

// NewHTTP builds the shared clients for one run.
func NewHTTP(l Limits, timeout time.Duration) *HTTP {
	h := &HTTP{lim: newLimiter(l), perHost: l.PerHost, timeout: timeout}
	h.def = h.client(nil)
	return h
}

// Client is the default client.
func (h *HTTP) Client() *http.Client { return h.def }

// WithTLS returns a client using cfg, e.g. for a chart repository with its
// own CA or client certificate. Each call opens a new connection pool, so
// callers keep the result.
func (h *HTTP) WithTLS(cfg *tls.Config) *http.Client { return h.client(cfg) }

func (h *HTTP) client(cfg *tls.Config) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ForceAttemptHTTP2 = true
	base.MaxIdleConnsPerHost = h.perHost
	if cfg != nil {
		base.TLSClientConfig = cfg
	}
	return &http.Client{
		Transport: &retry.Transport{Base: &limitTransport{base: base, lim: h.lim}, Policy: retryPolicy},
		Timeout:   h.timeout,
	}
}

func newHTTPClient(l Limits, timeout time.Duration) *http.Client {
	return NewHTTP(l, timeout).Client()
}
