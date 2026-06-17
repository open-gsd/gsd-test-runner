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

const TEST_START = 'test:start';
const TEST_DONE = new Set(['test:pass', 'test:fail', 'test:complete']);

/**
 * ActiveTracker observes reporter JSONL events and reports which tests were
 * in flight at a given instant. Feeds kill.lastActiveTest / kill.inFlightTests
 * (ADR-0021 §C). Under process isolation these are precise; under isolation
 * "none" the caller marks them best-effort (Decision 5).
 */
export class ActiveTracker {
  constructor(now = Date.now) {
    this._now = now;
    this._inFlight = []; // { file, name, startedAt }, insertion-ordered
  }

  observe(event) {
    if (!event || typeof event.type !== 'string') return;
    if (event.type === TEST_START) {
      this._inFlight.push({ file: event.file, name: event.name, startedAt: this._now() });
    } else if (TEST_DONE.has(event.type)) {
      this._inFlight = this._inFlight.filter(
        (t) => !(t.name === event.name && t.file === event.file),
      );
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
    if (spawnedDetached && typeof child.pid === 'number') {
      try {
        process.kill(-child.pid, sig);
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
      if (kill) {
        kill.elapsedMs = elapsed();
        resolve({ outcome: 'reaped', exitCode: code, kill });
      } else {
        resolve({ outcome: 'completed', exitCode: code ?? 0 });
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
