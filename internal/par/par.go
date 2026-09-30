// Package par runs registry work in parallel without stampeding auth.
package par

import (
	"context"
	"sync"
)

// ByHost runs fn for every item. The first item of each host runs before the
// others of that host start, so the host's auth challenge is answered once
// and the resulting token is cached for the rest. Leaders of different hosts
// run concurrently, and so do all remaining items.
func ByHost[T any](ctx context.Context, items []T, host func(T) string, fn func(context.Context, T)) {
	seen := map[string]bool{}
	var leaders, rest []T
	for _, it := range items {
		if h := host(it); !seen[h] {
			seen[h] = true
			leaders = append(leaders, it)
		} else {
			rest = append(rest, it)
		}
	}
	run := func(batch []T) {
		var wg sync.WaitGroup
		for _, it := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				fn(ctx, it)
			}()
		}
		wg.Wait()
	}
	run(leaders)
	run(rest)
}
