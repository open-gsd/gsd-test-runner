import { test } from 'node:test';
import assert from 'node:assert/strict';

import { leakedTypes, testFileFromArgv } from './leak-probe.mjs';

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
