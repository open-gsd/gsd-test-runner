import { test } from 'node:test';
import assert from 'node:assert/strict';

import { spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  leakedTypes,
  testFileFromArgv,
  parseSampleConfig,
  StackRegistry,
  buildSnapshot,
} from './leak-probe.mjs';

const here = dirname(fileURLToPath(import.meta.url));

test('leakedTypes returns resources opened beyond the baseline', () => {
  assert.deepEqual(leakedTypes(['Timeout'], ['Timeout', 'TCPSocketWrap']), ['TCPSocketWrap']);
});

test('leakedTypes counts duplicates and ignores resources that closed', () => {
  assert.deepEqual(leakedTypes(['Timeout'], ['Timeout', 'Timeout', 'Timeout']), ['Timeout', 'Timeout']);
  assert.deepEqual(leakedTypes(['Timeout', 'Timeout'], ['Timeout']), []);
});

test('testFileFromArgv finds the test file, else empty', () => {
  assert.equal(testFileFromArgv(['node', '--test', '/work/foo.test.mjs']), '/work/foo.test.mjs');
  assert.equal(testFileFromArgv(['node', '/work/bar.test.cjs']), '/work/bar.test.cjs');
  assert.equal(testFileFromArgv(['node', 'build.js', '--flag']), '');
});

// ── Periodic in-flight handle sampling (issue #60, ADR-0021 telemetry knobs) ──

test('parseSampleConfig is disabled by default and on bad input', () => {
  assert.deepEqual(parseSampleConfig({}), { intervalMs: 0, captureStacks: false });
  assert.deepEqual(parseSampleConfig({ GSD_SAMPLE_HANDLES_MS: '0' }), { intervalMs: 0, captureStacks: false });
  assert.deepEqual(parseSampleConfig({ GSD_SAMPLE_HANDLES_MS: '-5' }), { intervalMs: 0, captureStacks: false });
  assert.deepEqual(parseSampleConfig({ GSD_SAMPLE_HANDLES_MS: 'nope' }), { intervalMs: 0, captureStacks: false });
});

test('parseSampleConfig reads the interval and the captureStacks flag', () => {
  assert.deepEqual(parseSampleConfig({ GSD_SAMPLE_HANDLES_MS: '5000', GSD_CAPTURE_STACKS: '1' }), {
    intervalMs: 5000,
    captureStacks: true,
  });
  assert.deepEqual(parseSampleConfig({ GSD_SAMPLE_HANDLES_MS: '250.7', GSD_CAPTURE_STACKS: 'true' }), {
    intervalMs: 250,
    captureStacks: true,
  });
  // Any value other than 1/true leaves stacks off.
  assert.equal(parseSampleConfig({ GSD_SAMPLE_HANDLES_MS: '100', GSD_CAPTURE_STACKS: '0' }).captureStacks, false);
});

test('StackRegistry tracks live resources by type and prunes on delete', () => {
  const reg = new StackRegistry(2);
  reg.record(1, 'Timeout', 'stackA');
  reg.record(2, 'Timeout', 'stackB');
  reg.record(3, 'Timeout', 'stackC'); // exceeds per-type cap of 2
  reg.record(4, 'TCPWRAP', 'stackD');
  const byType = reg.byType();
  assert.equal(byType.Timeout.length, 2, 'per-type cap bounds memory');
  assert.deepEqual(byType.TCPWRAP, ['stackD']);

  reg.delete(4); // resource closed → drops out of the live set
  assert.equal(reg.byType().TCPWRAP, undefined);
});

test('buildSnapshot reports elapsed time, open count, leak delta, and optional stacks', () => {
  const snap = buildSnapshot({
    atMs: 3000,
    baseline: ['Timeout'],
    current: ['Timeout', 'TCPSocketWrap', 'TCPSocketWrap'],
  });
  assert.deepEqual(snap, { atMs: 3000, open: 3, leaked: ['TCPSocketWrap', 'TCPSocketWrap'] });

  const withStacks = buildSnapshot({
    atMs: 1000,
    baseline: [],
    current: ['Timeout'],
    stacks: { Timeout: ['at foo'] },
  });
  assert.deepEqual(withStacks.stacks, { Timeout: ['at foo'] });
});

// Integration: preloading the probe into a real `node --test` child with the
// sampling knobs set must write a `.samples.jsonl` sidecar with periodic, in-
// flight snapshots — and must NOT mask a genuine leak in the exit-time report
// (the sampler's own timer is accounted for). This exercises the env-guarded
// installer, not just the pure helpers.
test('preloaded probe writes periodic samples and still reports a real leak', () => {
  const dir = mkdtempSync(join(tmpdir(), 'gsd-probe-int-'));
  const testFile = join(dir, 'leaky.test.mjs');
  // Runs ~250ms (long enough for several 40ms samples) and leaks one timer so
  // --test-force-exit triggers the exit-time probe.
  writeFileSync(testFile,
    "import { test } from 'node:test';\n" +
    "test('slow and leaky', async () => {\n" +
    "  setInterval(() => {}, 1000);\n" +
    "  await new Promise((r) => setTimeout(r, 250));\n" +
    "});\n");

  // Preload via NODE_OPTIONS (as run-and-die.sh does) so the probe is inherited
  // by the per-file child process where the test actually runs under isolation.
  // Strip NODE_TEST_CONTEXT so the nested `node --test` does not detect a
  // recursive test run and skip executing the file (this suite runs under
  // `node --test` itself).
  const childEnv = { ...process.env };
  delete childEnv.NODE_TEST_CONTEXT;
  const res = spawnSync(process.execPath,
    ['--test', '--test-force-exit', testFile],
    {
      env: {
        ...childEnv,
        NODE_OPTIONS: `--import ${join(here, 'leak-probe.mjs')}`,
        GSD_LEAK_DIR: dir,
        GSD_SAMPLE_HANDLES_MS: '40',
      },
      encoding: 'utf8',
    });
  assert.equal(res.status, 0, `node --test failed: ${res.stderr}`);

  // Sidecars are named by the sanitized (full) test-file path, so locate them
  // by suffix rather than guessing the exact name.
  const names = readdirSync(dir);
  const samplesName = names.find((n) => n.endsWith('.samples.jsonl'));
  assert.ok(samplesName, `expected a .samples.jsonl sidecar; dir had ${names}`);
  const lines = readFileSync(join(dir, samplesName), 'utf8').trim().split('\n').filter(Boolean);
  assert.ok(lines.length >= 1, 'at least one periodic sample written during the run');
  const snap = JSON.parse(lines[0]);
  assert.equal(snap.file, testFile);
  assert.equal(typeof snap.atMs, 'number');
  assert.ok(Array.isArray(snap.leaked), 'each sample carries a leak delta');

  // The exit-time leak report must still fire: the sampler timer must not have
  // masked the test's own leaked interval.
  const leakName = names.find((n) => n.endsWith('.json') && !n.endsWith('.samples.jsonl'));
  assert.ok(leakName, `expected an exit-time leak report; dir had ${names}`);
  const leakReport = JSON.parse(readFileSync(join(dir, leakName), 'utf8'));
  assert.ok(leakReport.leaked.includes('Timeout'), 'real leaked timer still reported at exit');
});
