package registry

import (
	"net/http"
	"sort"
	"sync"
)

// HostStats is what one run sent to one host. Requests counts every HTTP
// attempt, retries included; AuthRounds counts 401 challenges answered.
type HostStats struct {
	Host       string
	Requests   int
	Retries    int
	AuthRounds int
}

type hostCount struct{ attempts, logical, challenges int }

// stats counts requests per host. It is fed by two countingTransports: one
// below the retry layer (every attempt) and one above it (logical requests),
// so retries = attempts − logical.
type stats struct {
	mu    sync.Mutex
	hosts map[string]*hostCount
}

func newStats() *stats { return &stats{hosts: map[string]*hostCount{}} }

func (s *stats) add(host string, f func(*hostCount)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.hosts[host]
	if !ok {
		c = &hostCount{}
		s.hosts[host] = c
	}
	f(c)
}

func (s *stats) snapshot() []HostStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HostStats, 0, len(s.hosts))
	for h, c := range s.hosts {
		out = append(out, HostStats{Host: h, Requests: c.attempts, Retries: max(0, c.attempts-c.logical), AuthRounds: c.challenges})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

type countingTransport struct {
	base    http.RoundTripper
	stats   *stats
	attempt bool // below the retry layer
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	resp, err := t.base.RoundTrip(req)
	t.stats.add(host, func(c *hostCount) {
		if !t.attempt {
			c.logical++
			return
		}
		c.attempts++
		if err == nil && resp.StatusCode == http.StatusUnauthorized {
			c.challenges++
		}
	})
	return resp, err
}
