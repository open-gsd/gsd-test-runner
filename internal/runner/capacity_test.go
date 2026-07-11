package runner

import (
	"context"
	"sync"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

func stubNCPU(t *testing.T, fn func(ctx context.Context, b bench.Bench) int) {
	t.Helper()
	orig := queryNCPU
	queryNCPU = fn
	t.Cleanup(func() { queryNCPU = orig })
}

func TestCapacity_ConfiguredWins(t *testing.T) {
	probed := false
	stubNCPU(t, func(context.Context, bench.Bench) int { probed = true; return 99 })

	c := newCapacityResolver(context.Background())
	got := c.capacity(bench.Bench{Name: "b1", OS: "linux", Capacity: 6})
	if got != 6 {
		t.Errorf("capacity = %d, want 6 (configured)", got)
	}
	if probed {
		t.Error("queryNCPU should not be called when capacity is configured")
	}
}

func TestCapacity_FallsBackToNCPU(t *testing.T) {
	stubNCPU(t, func(context.Context, bench.Bench) int { return 8 })

	c := newCapacityResolver(context.Background())
	got := c.capacity(bench.Bench{Name: "b1", OS: "linux"})
	if got != 8 {
		t.Errorf("capacity = %d, want 8 (NCPU)", got)
	}
}

func TestCapacity_ProbeFailureFloorsToOne(t *testing.T) {
	stubNCPU(t, func(context.Context, bench.Bench) int { return 0 })

	c := newCapacityResolver(context.Background())
	got := c.capacity(bench.Bench{Name: "b1", OS: "linux"})
	if got != 1 {
		t.Errorf("capacity = %d, want 1 (floor on probe failure)", got)
	}
}

func TestCapacity_CachesPerBench(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	stubNCPU(t, func(_ context.Context, b bench.Bench) int {
		mu.Lock()
		defer mu.Unlock()
		calls[b.Name]++
		return 4
	})

	c := newCapacityResolver(context.Background())
	b := bench.Bench{Name: "b1", OS: "linux"}
	for i := 0; i < 5; i++ {
		if got := c.capacity(b); got != 4 {
			t.Fatalf("capacity = %d, want 4", got)
		}
	}
	if calls["b1"] != 1 {
		t.Errorf("queryNCPU called %d times for b1, want 1 (cached)", calls["b1"])
	}
}
