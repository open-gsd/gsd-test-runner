// watchdog.mjs — the Tier-1 in-container watchdog for run-and-die execution
// (issue #60, ADR-0021 Decision 4). It wraps `node --test`, arms a deadline at
// the start of the test run, and on expiry snapshots the in-flight tests, emits
// a kill record, and escalates SIGTERM→grace→SIGKILL of the runner subtree so
// no node child survives. Baked into the Tester Image at /opt/gsd-test/watchdog.mjs.
//
// The module is structured around two testable units — ActiveTracker (what was
// running when the kill fired) and runWithWatchdog (deadline + signal
// escalation) — so the behavior is verified with `node --test` rather than only
// in a live container.

import { spawn } from 'node:child_process';
import { pathToFileURL } from 'node:url';

// Exit code the watchdog returns when it reaped the run, distinct from a test
// failure (1) so the Local Engine can tell "killed" from "failed".
export const EXIT_REAPED = 75;

// normFile strips the contractual /work/ prefix so test:start file paths
// (raw, absolute) match the reporter's repo-relative paths.
function normFile(f) {
  if (typeof f !== 'string') return '';
  return f.startsWith('/work/') ? f.slice('/work/'.length) : f;
}

/**
 * ActiveTracker consumes the reporter's JSONL events (reporter/reporter.mjs):
 *   - test:start  -> { type:'test:start', data:{ name, file } }   (passthrough)
 *   - completion  -> { type:'test_event', kind:'pass'|'fail', file, name, duration_ms }
 * It reports which tests were in flight at a given instant (kill.lastActiveTest
 * / inFlightTests, ADR-0021 §C) and accumulates per-test telemetry (§F). Under
 * process isolation attribution is precise; under isolation "none" the caller
 * marks it best-effort (Decision 5). In-flight matching is FIFO by file.
 */
export class ActiveTracker {
  constructor(now = Date.now) {
    this._now = now;
    this._inFlight = [];  // { file, name, startedAt }, insertion-ordered
    this._completed = []; // { file, name, durationMs, status, exitedClean }
  }

  observe(event) {
    if (!event || typeof event.type !== 'string') return;
    if (event.type === 'test:start') {
      const d = event.data || {};
      this._inFlight.push({ file: normFile(d.file), name: d.name || '', startedAt: this._now() });
    } else if (event.type === 'test_event') {
      const file = normFile(event.file);
      const idx = this._inFlight.findIndex((t) => t.file === file);
      if (idx >= 0) this._inFlight.splice(idx, 1);
      this._completed.push({
        file,
        name: event.name || '',
        durationMs: typeof event.duration_ms === 'number' ? event.duration_ms : 0,
        status: event.kind === 'fail' ? 'failed' : 'passed',
        exitedClean: true,
      });
    }
  }

  snapshot(nowMs) {
    const inFlightTests = this._inFlight.map((t) => ({
      file: t.file,
      name: t.name,
      startedMsAgo: nowMs - t.startedAt,
    }));
    const last = this._inFlight[this._inFlight.length - 1];
    return {
      lastActiveTest: last ? { file: last.file, name: last.name } : null,
      inFlightTests,
    };
  }

  /**
   * perTest returns per-test telemetry. Completed tests carry their duration
   * and pass/fail status; when the run was killed, tests still in flight are
   * recorded as status 'killed' with exitedClean:false — a leading indicator
   * for the runaway leaderboard that raising the estimate cannot hide.
   */
  perTest(nowMs, killed) {
    const out = this._completed.map((t) => ({ ...t }));
    if (killed) {
      for (const t of this._inFlight) {
        out.push({ file: t.file, name: t.name, durationMs: nowMs - t.startedAt, status: 'killed', exitedClean: false });
      }
    }
    return out;
  }
}

/**
 * runWithWatchdog enforces a deadline on a child process. Resolves with
 * { outcome: 'completed'|'reaped', exitCode, kill? }. On deadline expiry it
 * snapshots active tests, sends SIGTERM, then after graceMs SIGKILL — the whole
 * subtree dies so no node child is orphaned.
 *
 * The child is injectable for testing; production callers omit it and pass
 * command/args, which are spawned in their own process group (detached) so the
 * signal reaches the runner's children too.
 */
export function runWithWatchdog(opts) {
  const {
    deadlineMs,
    graceMs = 5000,
    reason = 'estimate_overrun',
    granularity = '',
  } = opts;

  const spawnedDetached = opts.child == null;
  const child = opts.child ?? spawn(opts.command, opts.args ?? [], {
    stdio: ['ignore', 'pipe', 'inherit'],
    detached: true, // new process group so we can signal the whole subtree
  });

  // sendSignal targets the child's process group when we spawned it detached,
  // so a SIGKILL reaps the runner AND every test child it forked — the orphan
  // guarantee. Falls back to a direct child.kill (injected test doubles, or if
  // the group is already gone).
  const sendSignal = (sig) => {
    // Windows has no POSIX process groups; signalling the parent does not reap
    // node --test's children. Use taskkill /T (whole tree), adding /F for the
    // hard kill (ADR-0021 Decision 4). Proven by the Windows Bench orphan gate,
    // not the local suite. The container-level reaper is the backstop.
    if (process.platform === 'win32' && typeof child.pid === 'number') {
      const args = ['/PID', String(child.pid), '/T'];
      if (sig === 'SIGKILL') args.push('/F');
      try {
        spawn('taskkill', args);
        return;
      } catch {
        /* fall through to direct kill */
      }
    }
    if (spawnedDetached && typeof child.pid === 'number') {
      try {
        process.kill(-child.pid, sig); // negative pid → whole process group
        return;
      } catch {
        /* group already gone; fall through */
      }
    }
    child.kill(sig);
  };

  const tracker = new ActiveTracker();
  const t0 = Date.now();
  const elapsed = () => Date.now() - t0;

  // Parse reporter JSONL off the child's stdout to know what is in flight.
  let buf = '';
  if (child.stdout) {
    child.stdout.on('data', (chunk) => {
      buf += chunk.toString();
      let nl;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        try {
          tracker.observe(JSON.parse(line));
        } catch {
          /* not a reporter event; ignore */
        }
      }
    });
  }

  return new Promise((resolve) => {
    let kill;
    let killTimer;

    const deadlineTimer = setTimeout(() => {
      const snap = tracker.snapshot(Date.now());
      kill = {
        reason,
        reapedBy: 'in_container',
        effectiveDeadlineMs: deadlineMs,
        elapsedMs: elapsed(),
        lastActiveTest: snap.lastActiveTest,
        inFlightTests: snap.inFlightTests,
        signalChain: [],
      };
      if (granularity) kill.granularity = granularity;

      sendSignal('SIGTERM');
      kill.signalChain.push(`SIGTERM@${elapsed()}`);
      killTimer = setTimeout(() => {
        sendSignal('SIGKILL');
        kill.signalChain.push(`SIGKILL@${elapsed()}`);
      }, graceMs);
    }, deadlineMs);

    child.on('exit', (code) => {
      clearTimeout(deadlineTimer);
      clearTimeout(killTimer);
      const perTest = tracker.perTest(Date.now(), !!kill);
      if (kill) {
        kill.elapsedMs = elapsed();
        resolve({ outcome: 'reaped', exitCode: code, kill, perTest });
      } else {
        resolve({ outcome: 'completed', exitCode: code ?? 0, perTest });
      }
    });
  });
}

/**
 * parseWatchdogArgs splits the watchdog's own flags from the wrapped command.
 * Form: --deadline-ms N [--grace-ms N] [--reason R] [--granularity G] -- CMD ARGS...
 * Everything after the literal "--" is the command to run under the watchdog.
 */
export function parseWatchdogArgs(argv) {
  const out = { deadlineMs: 0, graceMs: 5000, reason: 'estimate_overrun', granularity: '', command: '', args: [] };
  let i = 0;
  for (; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--') { i++; break; }
    else if (a === '--deadline-ms') out.deadlineMs = Number(argv[++i]);
    else if (a === '--grace-ms') out.graceMs = Number(argv[++i]);
    else if (a === '--reason') out.reason = argv[++i];
    else if (a === '--granularity') out.granularity = argv[++i];
  }
  out.command = argv[i] ?? '';
  out.args = argv.slice(i + 1);
  return out;
}

/**
 * main is the container entrypoint: parse args, run the wrapped command under
 * the watchdog, print the result envelope as JSON on stdout, and exit with
 * EXIT_REAPED when reaped (distinct from a test failure).
 */
export async function main(argv) {
  const { deadlineMs, graceMs, reason, granularity, command, args } = parseWatchdogArgs(argv);
  const res = await runWithWatchdog({ command, args, deadlineMs, graceMs, reason, granularity });
  process.stdout.write(JSON.stringify(res) + '\n');
  if (res.outcome === 'reaped') return EXIT_REAPED;
  return res.exitCode ?? 0;
}

// Run as a CLI when invoked directly (node watchdog.mjs ...), not when imported.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).then((code) => process.exit(code));
}
