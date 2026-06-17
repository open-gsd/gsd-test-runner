import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';

import { ActiveTracker, runWithWatchdog } from './watchdog.mjs';

// ── ActiveTracker — feeds kill.lastActiveTest / inFlightTests (ADR-0021 §C) ──

test('ActiveTracker tracks the in-flight test and clears it on completion', () => {
  const tr = new ActiveTracker(() => 1000);
  tr.observe({ type: 'test:start', file: 'a.test.js', name: 'alpha' });
  let snap = tr.snapshot(1000);
  assert.equal(snap.lastActiveTest.name, 'alpha');
  assert.equal(snap.inFlightTests.length, 1);

  tr.observe({ type: 'test:pass', file: 'a.test.js', name: 'alpha' });
  snap = tr.snapshot(1000);
  assert.equal(snap.inFlightTests.length, 0);
});

test('ActiveTracker lastActiveTest is the most recently started in-flight test', () => {
  const tr = new ActiveTracker(() => 0);
  tr.observe({ type: 'test:start', file: 'a.test.js', name: 'alpha' });
  tr.observe({ type: 'test:start', file: 'b.test.js', name: 'beta' });
  const snap = tr.snapshot(5000);
  assert.equal(snap.lastActiveTest.name, 'beta');
  assert.equal(snap.inFlightTests.length, 2);
  // startedMsAgo is computed from the snapshot clock.
  const beta = snap.inFlightTests.find((t) => t.name === 'beta');
  assert.equal(beta.startedMsAgo, 5000);
});

// ── runWithWatchdog — deadline + escalating SIGTERM→SIGKILL (ADR-0021 D4) ──

// fakeChild is an EventEmitter with a recording .kill(); on SIGKILL it exits.
function fakeChild({ ignoreTerm = true } = {}) {
  const child = new EventEmitter();
  child.signals = [];
  child.kill = (sig) => {
    child.signals.push(sig);
    if (sig === 'SIGKILL' || (!ignoreTerm && sig === 'SIGTERM')) {
      setImmediate(() => child.emit('exit', null, sig));
    }
  };
  child.stdout = new EventEmitter();
  return child;
}

test('child that finishes before the deadline is not reaped', async () => {
  const child = fakeChild();
  const p = runWithWatchdog({ child, deadlineMs: 10_000, graceMs: 50 });
  setImmediate(() => child.emit('exit', 0, null));
  const res = await p;
  assert.equal(res.outcome, 'completed');
  assert.equal(res.exitCode, 0);
  assert.equal(res.kill, undefined);
  assert.deepEqual(child.signals, []);
});

test('child past the deadline is reaped with SIGTERM then SIGKILL', async () => {
  const child = fakeChild({ ignoreTerm: true });
  const res = await runWithWatchdog({ child, deadlineMs: 20, graceMs: 20 });
  assert.equal(res.outcome, 'reaped');
  assert.equal(res.kill.reason, 'estimate_overrun');
  assert.equal(res.kill.reapedBy, 'in_container');
  assert.deepEqual(child.signals, ['SIGTERM', 'SIGKILL']);
  assert.ok(res.kill.signalChain.some((s) => s.startsWith('SIGTERM')));
  assert.ok(res.kill.signalChain.some((s) => s.startsWith('SIGKILL')));
});

test('reaped kill record carries the last active test', async () => {
  const child = fakeChild({ ignoreTerm: true });
  const p = runWithWatchdog({ child, deadlineMs: 40, graceMs: 20 });
  // Emit after the watchdog has attached its stdout handler.
  child.stdout.emit('data',
    Buffer.from(JSON.stringify({ type: 'test:start', file: 'db.test.js', name: 'reconnects' }) + '\n'));
  const res = await p;
  assert.equal(res.outcome, 'reaped');
  assert.equal(res.kill.lastActiveTest.name, 'reconnects');
});

// Real-spawn integration: a genuinely hanging node process must be reaped and
// actually leave the process table (orphan guarantee, ADR-0021 Decision 4).
test('reaps a real hanging node child', async () => {
  const res = await runWithWatchdog({
    command: process.execPath,
    args: ['-e', 'process.on("SIGTERM", () => {}); setInterval(() => {}, 1000);'],
    deadlineMs: 100,
    graceMs: 100,
  });
  assert.equal(res.outcome, 'reaped');
  assert.ok(res.kill.signalChain.some((s) => s.startsWith('SIGKILL')),
    'a SIGTERM-ignoring child must escalate to SIGKILL');
});

// ── CLI argument parsing (pure) ──────────────────────────────────────────────

import { parseWatchdogArgs } from './watchdog.mjs';

test('parseWatchdogArgs splits flags from the wrapped command', () => {
  const got = parseWatchdogArgs(
    ['--deadline-ms', '180000', '--grace-ms', '5000', '--reason', 'hard_cap',
     '--granularity', 'process', '--', 'node', '--test', 'a.test.js']);
  assert.equal(got.deadlineMs, 180000);
  assert.equal(got.graceMs, 5000);
  assert.equal(got.reason, 'hard_cap');
  assert.equal(got.granularity, 'process');
  assert.equal(got.command, 'node');
  assert.deepEqual(got.args, ['--test', 'a.test.js']);
});

test('parseWatchdogArgs applies defaults when flags omitted', () => {
  const got = parseWatchdogArgs(['--deadline-ms', '50', '--', 'sleep', '1']);
  assert.equal(got.deadlineMs, 50);
  assert.equal(got.graceMs, 5000); // default
  assert.equal(got.command, 'sleep');
});
