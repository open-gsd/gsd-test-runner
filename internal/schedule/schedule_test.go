package schedule

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// makeUnits builds n Units of a given OS, with distinct Payload indices so
// each unit can be told apart in assertions.
func makeUnits(osName string, n int) []Unit {
	units := make([]Unit, n)
	for i := 0; i < n; i++ {
		units[i] = Unit{OS: osName, Payload: i}
	}
	return units
}

// TestRun_AllUnitsProcessedExactlyOnce asserts len(results)==len(units) and
// every unit payload appears exactly once in the results.
func TestRun_AllUnitsProcessedExactlyOnce(t *testing.T) {
	b := bench.Bench{Name: "linux-1", OS: "linux", Capacity: 2}
	units := makeUnits("linux", 25)
	benchesByOS := map[string][]bench.Bench{"linux": {b}}

	results := Run(context.Background(), units, benchesByOS,
		func(b bench.Bench) int { return b.Capacity },
		func(ctx context.Context, b bench.Bench, u Unit) any {
			return u.Payload
		},
	)

	if len(results) != len(units) {
		t.Fatalf("len(results)=%d, want %d", len(results), len(units))
	}

	seen := make(map[int]int)
	for _, r := range results {
		idx, ok := r.Unit.Payload.(int)
		if !ok {
			t.Fatalf("unexpected payload type: %T", r.Unit.Payload)
		}
		seen[idx]++
	}
	for i := 0; i < len(units); i++ {
		if seen[i] != 1 {
			t.Errorf("unit %d appeared %d times, want 1", i, seen[i])
		}
	}
}

// TestRun_PerBenchCapRespected checks that the observed concurrency on a
// single Bench never exceeds its declared capacity, for capacity 1 and 3.
func TestRun_PerBenchCapRespected(t *testing.T) {
	for _, cap := range []int{1, 3} {
		cap := cap
		t.Run(fmt.Sprintf("capacity=%d", cap), func(t *testing.T) {
			b := bench.Bench{Name: "linux-1", OS: "linux", Capacity: cap}
			units := makeUnits("linux", 20)
			benchesByOS := map[string][]bench.Bench{"linux": {b}}

			var inFlight int64
			var maxSeen int64

			results := Run(context.Background(), units, benchesByOS,
				func(b bench.Bench) int { return b.Capacity },
				func(ctx context.Context, b bench.Bench, u Unit) any {
					cur := atomic.AddInt64(&inFlight, 1)
					for {
						prev := atomic.LoadInt64(&maxSeen)
						if cur <= prev || atomic.CompareAndSwapInt64(&maxSeen, prev, cur) {
							break
						}
					}
					time.Sleep(2 * time.Millisecond)
					atomic.AddInt64(&inFlight, -1)
					return nil
				},
			)

			if len(results) != len(units) {
				t.Fatalf("len(results)=%d, want %d", len(results), len(units))
			}
			if got := atomic.LoadInt64(&maxSeen); got > int64(cap) {
				t.Errorf("observed max concurrency %d exceeds capacity %d", got, cap)
			}
		})
	}
}

// TestRun_LeastLoadedWorkStealing verifies that with two same-OS Benches of
// differing capacity, the higher-capacity Bench processes strictly more
// units (pull-based sharing naturally load-balances).
func TestRun_LeastLoadedWorkStealing(t *testing.T) {
	lowCap := bench.Bench{Name: "linux-low", OS: "linux", Capacity: 1}
	highCap := bench.Bench{Name: "linux-high", OS: "linux", Capacity: 4}
	units := makeUnits("linux", 100)
	benchesByOS := map[string][]bench.Bench{"linux": {lowCap, highCap}}

	var mu sync.Mutex
	counts := make(map[string]int)

	results := Run(context.Background(), units, benchesByOS,
		func(b bench.Bench) int { return b.Capacity },
		func(ctx context.Context, b bench.Bench, u Unit) any {
			time.Sleep(1 * time.Millisecond)
			mu.Lock()
			counts[b.Name]++
			mu.Unlock()
			return nil
		},
	)

	if len(results) != len(units) {
		t.Fatalf("len(results)=%d, want %d", len(results), len(units))
	}

	if counts[highCap.Name] <= counts[lowCap.Name] {
		t.Errorf("expected high-capacity bench to process strictly more units: high=%d low=%d",
			counts[highCap.Name], counts[lowCap.Name])
	}
}

// TestRun_ErrNoBench asserts a unit with no candidate Bench for its OS gets
// exactly one Result with Value==ErrNoBench and a zero Bench.
func TestRun_ErrNoBench(t *testing.T) {
	units := []Unit{{OS: "windows", Payload: "only-unit"}}
	benchesByOS := map[string][]bench.Bench{
		"linux": {{Name: "linux-1", OS: "linux", Capacity: 1}},
	}

	results := Run(context.Background(), units, benchesByOS,
		func(b bench.Bench) int { return b.Capacity },
		func(ctx context.Context, b bench.Bench, u Unit) any {
			t.Fatalf("work should not be called for a unit with no candidate bench")
			return nil
		},
	)

	if len(results) != 1 {
		t.Fatalf("len(results)=%d, want 1", len(results))
	}
	r := results[0]
	if r.Value != ErrNoBench {
		t.Errorf("Value = %v, want ErrNoBench", r.Value)
	}
	if r.Bench != (bench.Bench{}) {
		t.Errorf("Bench = %+v, want zero value", r.Bench)
	}
}

// TestRun_CapacityLessThanOneTreatedAsOne verifies a Bench with Capacity==0
// still processes its units (serially, as if capacity were 1).
func TestRun_CapacityLessThanOneTreatedAsOne(t *testing.T) {
	b := bench.Bench{Name: "linux-1", OS: "linux", Capacity: 0}
	units := makeUnits("linux", 5)
	benchesByOS := map[string][]bench.Bench{"linux": {b}}

	var inFlight int64
	var maxSeen int64

	results := Run(context.Background(), units, benchesByOS,
		func(b bench.Bench) int { return b.Capacity },
		func(ctx context.Context, b bench.Bench, u Unit) any {
			cur := atomic.AddInt64(&inFlight, 1)
			for {
				prev := atomic.LoadInt64(&maxSeen)
				if cur <= prev || atomic.CompareAndSwapInt64(&maxSeen, prev, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			return nil
		},
	)

	if len(results) != len(units) {
		t.Fatalf("len(results)=%d, want %d", len(results), len(units))
	}
	if got := atomic.LoadInt64(&maxSeen); got > 1 {
		t.Errorf("observed max concurrency %d for capacity=0 bench, want <=1", got)
	}
}

// TestRun_MultipleOSesRoutedCorrectly ensures linux units only ever run on
// linux benches and windows units only ever run on windows benches.
func TestRun_MultipleOSesRoutedCorrectly(t *testing.T) {
	linuxBench := bench.Bench{Name: "linux-1", OS: "linux", Capacity: 2}
	windowsBench := bench.Bench{Name: "windows-1", OS: "windows", Capacity: 2}

	units := append(makeUnits("linux", 15), makeUnits("windows", 15)...)
	benchesByOS := map[string][]bench.Bench{
		"linux":   {linuxBench},
		"windows": {windowsBench},
	}

	results := Run(context.Background(), units, benchesByOS,
		func(b bench.Bench) int { return b.Capacity },
		func(ctx context.Context, b bench.Bench, u Unit) any {
			return nil
		},
	)

	if len(results) != len(units) {
		t.Fatalf("len(results)=%d, want %d", len(results), len(units))
	}
	for _, r := range results {
		if r.Value == ErrNoBench {
			t.Fatalf("unexpected ErrNoBench for unit OS=%s", r.Unit.OS)
		}
		if r.Bench.OS != r.Unit.OS {
			t.Errorf("unit OS=%s ran on bench OS=%s (bench=%s)", r.Unit.OS, r.Bench.OS, r.Bench.Name)
		}
	}
}

// TestRun_ContextCancelled asserts that with a pre-cancelled ctx, every unit
// still returns a Result (with ctx.Err() as Value) and work is never invoked.
func TestRun_ContextCancelled(t *testing.T) {
	b := bench.Bench{Name: "linux-1", OS: "linux", Capacity: 2}
	units := makeUnits("linux", 10)
	benchesByOS := map[string][]bench.Bench{"linux": {b}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := Run(ctx, units, benchesByOS,
		func(b bench.Bench) int { return b.Capacity },
		func(ctx context.Context, b bench.Bench, u Unit) any {
			t.Fatalf("work should not be invoked after ctx cancellation")
			return nil
		},
	)

	if len(results) != len(units) {
		t.Fatalf("len(results)=%d, want %d", len(results), len(units))
	}
	for _, r := range results {
		if r.Value != context.Canceled {
			t.Errorf("Value = %v, want context.Canceled", r.Value)
		}
	}
}
