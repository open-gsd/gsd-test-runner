package runner

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
)

// capacityResolver resolves a Bench's per-run container concurrency: the
// configured capacity when set (> 0), else the Bench's own CPU count (probed
// via `docker info -f {{.NCPU}}` against that Bench's daemon), else 1. Results
// are cached per Bench name so the NCPU probe runs at most once per Bench per
// invocation (enhancement #108). Safe for concurrent use by the scheduler.
type capacityResolver struct {
	ctx   context.Context
	mu    sync.Mutex
	cache map[string]int
}

func newCapacityResolver(ctx context.Context) *capacityResolver {
	return &capacityResolver{ctx: ctx, cache: make(map[string]int)}
}

// capacity returns the max concurrent Tester containers to run on b. Configured
// capacity wins; otherwise the Bench's NCPU (auto-parallel), floored at 1.
func (c *capacityResolver) capacity(b bench.Bench) int {
	if b.Capacity > 0 {
		return b.Capacity
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.cache[b.Name]; ok {
		return v
	}
	n := queryNCPU(c.ctx, b)
	if n < 1 {
		n = 1
	}
	c.cache[b.Name] = n
	return n
}

// queryNCPU returns the Bench daemon's CPU count via `docker info -f {{.NCPU}}`.
// Returns 0 on any error (the caller floors to 1) — a probe failure must never
// abort the run, only fall back to serial for that Bench. Package var so tests
// can stub it without a Docker daemon.
var queryNCPU = func(ctx context.Context, b bench.Bench) int {
	out, err := dockerexec.Run(ctx, b, []string{"info", "-f", "{{.NCPU}}"})
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return n
}
