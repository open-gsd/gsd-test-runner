// leak-probe.mjs — preloaded into each `node --test` child process via
// NODE_OPTIONS=--import (issue #60, ADR-0021 §F). Under process isolation each
// test file runs in its own child; this module records a baseline of open OS
// resources at load and, at process exit, reports anything still open beyond
// it. A test that leaks a handle (timer, socket, …) but still *completes* is
// force-exited by `--test-force-exit`, so the leak surfaces here even though the
// test "passed". The watchdog reads these reports and marks the test
// exited_clean:false — the independent, estimate-proof signal for the runaway
// leaderboard (Goodhart). Best-effort and file-level: it cannot attribute a
// leak to a specific test within a multi-test file, and is a no-op under
// isolation=none (no per-file child / no test file in argv).

import fs from 'node:fs';
import path from 'node:path';
import asyncHooks from 'node:async_hooks';

// leakedTypes returns the resource-type strings open in `current` beyond what
// was open in `baseline` (by count, so two leaked Timeouts both show).
export function leakedTypes(baseline, current) {
  const tally = (arr) => {
    const m = new Map();
    for (const t of arr) m.set(t, (m.get(t) || 0) + 1);
    return m;
  };
  const b = tally(baseline);
  const out = [];
  for (const [t, n] of tally(current)) {
    for (let i = 0; i < n - (b.get(t) || 0); i++) out.push(t);
  }
  return out;
}

// testFileFromArgv returns the first argument that looks like a test file, or
// "" when none is present (e.g. the watchdog's own process, or isolation=none).
export function testFileFromArgv(argv) {
  for (const a of argv) {
    if (/\.test\.(mjs|cjs|js)$/.test(a)) return a;
  }
  return '';
}

// parseSampleConfig reads the *periodic* in-flight sampling knobs the dispatch
// layer forwards from the run spec's telemetry field (sampleHandlesMs /
// captureStacks, ADR-0021 §A). Sampling is opt-in: a non-positive or unparseable
// interval disables it. captureStacks is only enabled by the literal "1"/"true".
export function parseSampleConfig(env = {}) {
  const raw = Number(env.GSD_SAMPLE_HANDLES_MS);
  const intervalMs = Number.isFinite(raw) && raw > 0 ? Math.floor(raw) : 0;
  const captureStacks = env.GSD_CAPTURE_STACKS === '1' || env.GSD_CAPTURE_STACKS === 'true';
  return { intervalMs, captureStacks };
}

// StackRegistry tracks the creation stacks of still-live async resources, keyed
// by asyncId, so a periodic sample can report WHERE a leaking handle was opened
// (telemetry.captureStacks). Per-type capacity is bounded so a runaway test
// cannot exhaust memory before the watchdog reaps it. Best-effort: promises are
// GC-destroyed so their `destroy` may lag, and stacks are creation-site only.
export class StackRegistry {
  constructor(perTypeCap = 20) {
    this._perTypeCap = perTypeCap;
    this._live = new Map(); // asyncId -> { type, stack }
  }

  record(asyncId, type, stack) {
    this._live.set(asyncId, { type, stack });
  }

  delete(asyncId) {
    this._live.delete(asyncId);
  }

  // byType groups the live stacks by resource type, capped per type.
  byType() {
    const out = {};
    for (const { type, stack } of this._live.values()) {
      const arr = (out[type] ||= []);
      if (arr.length < this._perTypeCap) arr.push(stack);
    }
    return out;
  }
}

// buildSnapshot assembles one periodic sample: the elapsed time, the total open
// handle count, the resource types open beyond the load-time baseline (reusing
// the same leak semantics as the exit-time probe), and — when stacks were
// captured — creation stacks grouped by async resource type. Pure, so it is
// unit-testable without timers or async_hooks.
export function buildSnapshot({ atMs, baseline, current, stacks }) {
  const snap = { atMs, open: current.length, leaked: leakedTypes(baseline, current) };
  if (stacks) snap.stacks = stacks;
  return snap;
}

// Install the exit-time probe (and, when configured, the periodic sampler) when
// running inside a test child with a leak dir configured. Guarded so importing
// this module in a unit test is a no-op.
const dir = process.env.GSD_LEAK_DIR;
const testFile = testFileFromArgv(process.argv);
if (dir && testFile && typeof process.getActiveResourcesInfo === 'function') {
  const baseline = process.getActiveResourcesInfo();
  const safe = testFile.replace(/[^a-zA-Z0-9._-]/g, '_');
  const { intervalMs, captureStacks } = parseSampleConfig(process.env);

  // Periodic in-flight sampling (ADR-0021 telemetry knobs). Unlike the exit-time
  // probe, this fires WHILE the test runs, so even a reaped (killed-before-exit)
  // test leaves a trail of how its open handles accumulated. Samples are flushed
  // synchronously per tick so a SIGKILL cannot lose them.
  let sampleTimer;
  let registry;
  if (intervalMs > 0) {
    const samplesPath = path.join(dir, safe + '.samples.jsonl');
    if (captureStacks) {
      registry = new StackRegistry();
      // async_hooks callbacks are themselves exempt from triggering hooks, so
      // capturing a stack here does not recurse. Trim the frames inside node's
      // own hook machinery to keep the creation site readable.
      const hook = asyncHooks.createHook({
        init(asyncId, type) {
          const stack = (new Error().stack || '').split('\n').slice(2, 8).join('\n');
          registry.record(asyncId, type, stack);
        },
        destroy(asyncId) {
          registry.delete(asyncId);
        },
      });
      hook.enable();
    }

    // The sampler's own interval is itself an active Timeout; mask it by adding
    // one Timeout to the baseline used WHILE the timer is live, so it is not
    // reported as a leak. At exit the timer is cleared first (below), so the
    // exit-time diff uses the original, unmasked baseline.
    const sampleBaseline = baseline.concat('Timeout');
    let elapsed = 0;
    sampleTimer = setInterval(() => {
      elapsed += intervalMs;
      const snap = buildSnapshot({
        atMs: elapsed,
        baseline: sampleBaseline,
        current: process.getActiveResourcesInfo(),
        stacks: registry ? registry.byType() : undefined,
      });
      try {
        fs.mkdirSync(dir, { recursive: true });
        fs.appendFileSync(samplesPath, JSON.stringify({ file: testFile, ...snap }) + '\n');
      } catch {
        /* best-effort: a dropped sample is missing data, not a run failure */
      }
    }, intervalMs);
    sampleTimer.unref(); // never keep the test process alive just to sample it
  }

  process.on('exit', () => {
    // Stop the sampler before the leak diff so its own Timeout is gone from the
    // current snapshot and the exit-time leak count stays exact.
    if (sampleTimer) clearInterval(sampleTimer);
    const leaked = leakedTypes(baseline, process.getActiveResourcesInfo());
    if (leaked.length === 0) return;
    try {
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, safe + '.json'), JSON.stringify({ file: testFile, leaked }));
    } catch {
      /* best-effort: a missing leak report just means no signal, not a failure */
    }
  });
}
