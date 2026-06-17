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

// Install the exit-time probe when running inside a test child with a leak dir
// configured. Guarded so importing this module in a unit test is a no-op.
const dir = process.env.GSD_LEAK_DIR;
const testFile = testFileFromArgv(process.argv);
if (dir && testFile && typeof process.getActiveResourcesInfo === 'function') {
  const baseline = process.getActiveResourcesInfo();
  process.on('exit', () => {
    const leaked = leakedTypes(baseline, process.getActiveResourcesInfo());
    if (leaked.length === 0) return;
    try {
      fs.mkdirSync(dir, { recursive: true });
      const safe = testFile.replace(/[^a-zA-Z0-9._-]/g, '_');
      fs.writeFileSync(path.join(dir, safe + '.json'), JSON.stringify({ file: testFile, leaked }));
    } catch {
      /* best-effort: a missing leak report just means no signal, not a failure */
    }
  });
}
