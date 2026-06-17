import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';

import { ActiveTracker, runWithWatchdog } from './watchdog.mjs';

// ── ActiveTracker — feeds kill.lastActiveTest / inFlightTests (ADR-0021 §C) ──

test('ActiveTracker tracks the in-flight test and clears it on completion', () => {
  const tr = new ActiveTracker(() => 1000);
  tr.observe({ type: 'test:start', data: { file: '/work/a.test.js', name: 'alpha' } });
  let snap = tr.snapshot(1000);
  assert.equal(snap.lastActiveTest.name, 'alpha');
  assert.equal(snap.lastActiveTest.file, 'a.test.js'); // /work/ stripped
  assert.equal(snap.inFlightTests.length, 1);

  tr.observe({ type: 'test_event', kind: 'pass', file: 'a.test.js', name: 'suite > alpha', duration_ms: 12 });
  snap = tr.snapshot(1000);
  assert.equal(snap.inFlightTests.length, 0);

  const stats = tr.perTest(1000, false);
  assert.equal(stats.length, 1);
  assert.equal(stats[0].status, 'passed');
  assert.equal(stats[0].durationMs, 12);
  assert.equal(stats[0].exitedClean, true);
});

test('ActiveTracker lastActiveTest is the most recently started in-flight test', () => {
  const tr = new ActiveTracker(() => 0);
  tr.observe({ type: 'test:start', data: { file: 'a.test.js', name: 'alpha' } });
  tr.observe({ type: 'test:start', data: { file: 'b.test.js', name: 'beta' } });
  const snap = tr.snapshot(5000);
  assert.equal(snap.lastActiveTest.name, 'beta');
  assert.equal(snap.inFlightTests.length, 2);
  const beta = snap.inFlightTests.find((t) => t.name === 'beta');
  assert.equal(beta.startedMsAgo, 5000);
});

test('ActiveTracker perTest marks in-flight tests killed when reaped', () => {
  const tr = new ActiveTracker(() => 0);
  tr.observe({ type: 'test:start', data: { file: 'hang.test.js', name: 'wedges' } });
  const stats = tr.perTest(2000, true);
  assert.equal(stats.length, 1);
  assert.equal(stats[0].status, 'killed');
  assert.equal(stats[0].exitedClean, false);
  assert.equal(stats[0].durationMs, 2000);
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
    Buffer.from(JSON.stringify({ type: 'test:start', data: { file: 'db.test.js', name: 'reconnects' } }) + '\n'));
  const res = await p;
  assert.equal(res.outcome, 'reaped');
  assert.equal(res.kill.lastActiveTest.name, 'reconnects');
  // The reaped envelope carries per-test telemetry: the in-flight test is killed.
  assert.ok(res.perTest.some((t) => t.name === 'reconnects' && t.status === 'killed'));
});

// Real-spawn integration: a genuinely hanging node process must be reaped and
// actually leave the process table (orphan guarantee, ADR-0021 Decision 4).
test('reaps a real hanging node child', async () => {
  // deadlineMs must comfortably exceed node *interpreter startup* so the child
  // has installed its SIGTERM handler before the deadline fires — otherwise the
  // watchdog's SIGTERM lands during boot, the default action ends the child
  // before our SIGKILL escalation, and the test flakes. 700ms was too tight
  // under load (node boot spiked past it on a saturated machine); 2500ms gives
  // a wide margin while keeping the test sub-3s.
  const res = await runWithWatchdog({
    command: process.execPath,
    args: ['-e', 'process.on("SIGTERM", () => {}); setInterval(() => {}, 1000);'],
    deadlineMs: 2500,
    graceMs: 300,
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

// ── mergeLeaks — folds leak-probe reports into per-test telemetry ────────────

import { mergeLeaks } from './watchdog.mjs';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test('mergeLeaks marks a completed test whose file leaked as not clean', () => {
  const dir = mkdtempSync(join(tmpdir(), 'gsd-leak-'));
  writeFileSync(join(dir, 'leaky.json'),
    JSON.stringify({ file: '/work/leaky.test.js', leaked: ['Timeout'] }));

  const perTest = [
    { file: 'leaky.test.js', name: 'leaks', status: 'passed', exitedClean: true },
    { file: 'clean.test.js', name: 'ok', status: 'passed', exitedClean: true },
  ];
  const merged = mergeLeaks(perTest, dir);
  assert.equal(merged[0].exitedClean, false, 'leaky test should be marked unclean');
  assert.equal(merged[1].exitedClean, true, 'clean test untouched');
});

test('mergeLeaks is a no-op without a leak dir or reports', () => {
  const perTest = [{ file: 'a.test.js', name: 'x', status: 'passed', exitedClean: true }];
  assert.deepEqual(mergeLeaks(perTest, undefined), perTest);
  assert.deepEqual(mergeLeaks(perTest, '/no/such/dir'), perTest);
});

test('mergeLeaks ignores periodic-sample files sharing the leak dir', () => {
  const dir = mkdtempSync(join(tmpdir(), 'gsd-leak-'));
  // A periodic-sample sidecar (JSONL, no top-level .file) must not be parsed as
  // a leak report — it would otherwise throw or be misread.
  writeFileSync(join(dir, 'a.test.js.samples.jsonl'),
    JSON.stringify({ file: 'a.test.js', atMs: 100, open: 3, leaked: ['Timeout'] }) + '\n');
  const perTest = [{ file: 'a.test.js', name: 'x', status: 'passed', exitedClean: true }];
  assert.deepEqual(mergeLeaks(perTest, dir), perTest, 'sample sidecar must not mark a leak');
});

// ── collectSamples — folds periodic handle samples into the run envelope ──────

import { collectSamples } from './watchdog.mjs';

test('collectSamples groups periodic samples by test file and survives teardown', () => {
  const dir = mkdtempSync(join(tmpdir(), 'gsd-samples-'));
  writeFileSync(join(dir, 'hang.test.mjs.samples.jsonl'),
    JSON.stringify({ file: 'hang.test.mjs', atMs: 1000, open: 2, leaked: [] }) + '\n' +
    JSON.stringify({ file: 'hang.test.mjs', atMs: 2000, open: 4, leaked: ['Timeout', 'Timeout'] }) + '\n');
  // A leak report (.json) in the same dir is NOT a sample file and is skipped.
  writeFileSync(join(dir, 'hang.test.mjs.json'),
    JSON.stringify({ file: 'hang.test.mjs', leaked: ['Timeout'] }));

  const got = collectSamples(dir);
  assert.equal(got.length, 1);
  assert.equal(got[0].file, 'hang.test.mjs');
  assert.equal(got[0].samples.length, 2);
  assert.equal(got[0].samples[1].open, 4);
  assert.deepEqual(got[0].samples[1].leaked, ['Timeout', 'Timeout']);
});

test('collectSamples is empty without a dir, and skips malformed lines', () => {
  assert.deepEqual(collectSamples(undefined), []);
  assert.deepEqual(collectSamples('/no/such/dir'), []);

  const dir = mkdtempSync(join(tmpdir(), 'gsd-samples-'));
  writeFileSync(join(dir, 'x.test.js.samples.jsonl'),
    'not json\n' + JSON.stringify({ file: 'x.test.js', atMs: 5, open: 1, leaked: [] }) + '\n');
  const got = collectSamples(dir);
  assert.equal(got.length, 1);
  assert.equal(got[0].samples.length, 1, 'malformed line skipped, good line kept');
});
