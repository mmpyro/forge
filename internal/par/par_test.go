package par_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmarszalek/helm-forge/internal/par"
)

type item struct{ host, name string }

func TestByHostRunsFirstItemPerHostAloneAndLeadersTogether(t *testing.T) {
	items := []item{{"a", "a1"}, {"a", "a2"}, {"a", "a3"}, {"b", "b1"}}
	var mu sync.Mutex
	var events []string
	var leaders sync.WaitGroup
	leaders.Add(2)
	par.ByHost(context.Background(), items, func(i item) string { return i.host }, func(_ context.Context, i item) {
		mu.Lock()
		events = append(events, "start "+i.name)
		mu.Unlock()
		if i.name == "a1" || i.name == "b1" {
			leaders.Done()
			done := make(chan struct{})
			go func() { leaders.Wait(); close(done) }()
			select {
			case <-done: // both leaders are running at once
			case <-time.After(2 * time.Second):
				t.Error("leaders of different hosts did not run concurrently")
			}
		}
		mu.Lock()
		events = append(events, "end "+i.name)
		mu.Unlock()
	})
	endA1 := slices.Index(events, "end a1")
	for _, n := range []string{"start a2", "start a3"} {
		if i := slices.Index(events, n); i < endA1 {
			t.Errorf("%q happened before a1 finished: %v", n, events)
		}
	}
}

func TestByHostRunsEveryItemOnce(t *testing.T) {
	var n atomic.Int32
	items := make([]int, 20)
	par.ByHost(context.Background(), items, func(int) string { return "h" }, func(context.Context, int) { n.Add(1) })
	if n.Load() != 20 {
		t.Fatalf("ran %d, want 20", n.Load())
	}
}
