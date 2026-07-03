// Package schedule runs a set of OS-routed work Units across a pool of
// Benches, honoring a per-Bench concurrency cap. Assignment is pull-based
// (work-stealing): each Bench contributes `capacity` worker goroutines that
// pull the next Unit for that Bench's OS whenever a slot frees, so faster /
// higher-capacity Benches naturally take more work (least-loaded emerges
// without explicit load tracking). Pure: no I/O; the real work is the
// caller-supplied `work` func. Extends the ADR-0016 selection model from
// one-Bench-per-OS to capacity-aware fan-out (enhancement #108).
package schedule

import (
	"context"
	"errors"
	"sync"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// ErrNoBench is the Result.Value for a Unit whose OS had no candidate Bench.
var ErrNoBench = errors.New("schedule: no bench available for OS")

// Unit is one schedulable job. OS routes it to Benches whose OS matches.
// Payload is opaque caller data (e.g. image ref, node major, events sink).
type Unit struct {
	OS      string
	Payload any
}

// Result pairs a completed Unit with the Bench that ran it and the value the
// work func returned.
type Result struct {
	Unit  Unit
	Bench bench.Bench
	Value any
}

// Run executes every Unit exactly once. benchesByOS maps an OS to the candidate
// Benches for it (already filtered for pin/exclude by the caller). capacity
// returns the max concurrent Units a given Bench may run at once (callers pass
// the resolved per-Bench cap; values < 1 are treated as 1). work performs one
// Unit on one Bench and returns an opaque value (the caller stores a report +
// error there). Run blocks until all Units complete or ctx is cancelled, then
// returns one Result per Unit (order not guaranteed).
//
// A Unit whose OS has no candidate Bench in benchesByOS is returned as a Result
// with a zero Bench and Value == ErrNoBench (callers detect and surface it);
// it is NOT dropped silently.
func Run(
	ctx context.Context,
	units []Unit,
	benchesByOS map[string][]bench.Bench,
	capacity func(bench.Bench) int,
	work func(ctx context.Context, b bench.Bench, u Unit) any,
) []Result {
	results := make([]Result, 0, len(units))
	var mu sync.Mutex
	var wg sync.WaitGroup

	appendResult := func(r Result) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}

	// Group units by OS, preserving order within each OS group.
	unitsByOS := make(map[string][]Unit)
	for _, u := range units {
		unitsByOS[u.OS] = append(unitsByOS[u.OS], u)
	}

	for os, osUnits := range unitsByOS {
		benches := benchesByOS[os]
		if len(benches) == 0 {
			for _, u := range osUnits {
				appendResult(Result{Unit: u, Value: ErrNoBench})
			}
			continue
		}

		osChan := make(chan Unit, len(osUnits))
		for _, u := range osUnits {
			osChan <- u
		}
		close(osChan)

		for _, b := range benches {
			n := capacity(b)
			if n < 1 {
				n = 1
			}
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(b bench.Bench) {
					defer wg.Done()
					for u := range osChan {
						if err := ctx.Err(); err != nil {
							appendResult(Result{Unit: u, Value: err})
							continue
						}
						v := work(ctx, b, u)
						appendResult(Result{Unit: u, Bench: b, Value: v})
					}
				}(b)
			}
		}
	}

	wg.Wait()
	return results
}
