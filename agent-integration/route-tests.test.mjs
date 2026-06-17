// route-tests.test.mjs — unit tests for the pure exported functions in
// route-tests.mjs. Run with:
//   node --test agent-integration/route-tests.test.mjs

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isTestCommand, rewrite } from './route-tests.mjs';

// ---------------------------------------------------------------------------
// isTestCommand — should match
// ---------------------------------------------------------------------------

test('matches: node --test', () => {
  assert.equal(isTestCommand('node --test'), true);
});

test('matches: node --test with file args', () => {
  assert.equal(isTestCommand('node --test src/foo.test.mjs'), true);
});

test('matches: node --test with glob', () => {
  assert.equal(isTestCommand('node --test "**/*.test.mjs"'), true);
});

test('matches: node --test-timeout flag', () => {
  assert.equal(isTestCommand('node --test-timeout=5000 --test'), true);
});

test('matches: node --test-force-exit', () => {
  assert.equal(isTestCommand('node --test --test-force-exit'), true);
});

test('matches: npm test', () => {
  assert.equal(isTestCommand('npm test'), true);
});

test('matches: npm t (shorthand)', () => {
  assert.equal(isTestCommand('npm t'), true);
});

test('matches: npm run test', () => {
  assert.equal(isTestCommand('npm run test'), true);
});

test('matches: CI=1 npm test (leading env var)', () => {
  assert.equal(isTestCommand('CI=1 npm test'), true);
});

test('matches: CI=1 NODE_ENV=test npm test (multiple env vars)', () => {
  assert.equal(isTestCommand('CI=1 NODE_ENV=test npm test'), true);
});

test('matches: CI=1 node --test (env var before node --test)', () => {
  assert.equal(isTestCommand('CI=1 node --test'), true);
});

test('matches: node --test with extra whitespace', () => {
  assert.equal(isTestCommand('  node  --test  '), true);
});

test('matches: npm run test with trailing args', () => {
  assert.equal(isTestCommand('npm run test -- --reporter=spec'), true);
});

// ---------------------------------------------------------------------------
// isTestCommand — should NOT match
// ---------------------------------------------------------------------------

test('rejects: node build.js', () => {
  assert.equal(isTestCommand('node build.js'), false);
});

test('rejects: node index.mjs', () => {
  assert.equal(isTestCommand('node index.mjs'), false);
});

test('rejects: node --version', () => {
  assert.equal(isTestCommand('node --version'), false);
});

test('rejects: npm install', () => {
  assert.equal(isTestCommand('npm install'), false);
});

test('rejects: npm ci', () => {
  assert.equal(isTestCommand('npm ci'), false);
});

test('rejects: npm run lint', () => {
  assert.equal(isTestCommand('npm run lint'), false);
});

test('rejects: npm run build', () => {
  assert.equal(isTestCommand('npm run build'), false);
});

test('rejects: eslint .', () => {
  assert.equal(isTestCommand('eslint .'), false);
});

test('rejects: eslint src/', () => {
  assert.equal(isTestCommand('eslint src/'), false);
});

test('rejects: npx jest', () => {
  assert.equal(isTestCommand('npx jest'), false);
});

test('rejects: empty string', () => {
  assert.equal(isTestCommand(''), false);
});

test('rejects: non-string (number)', () => {
  assert.equal(isTestCommand(/** @type {any} */ (42)), false);
});

test('rejects: npm run testing (prefix match guard)', () => {
  // "npm run testing" should NOT match — only "npm run test" exactly
  assert.equal(isTestCommand('npm run testing'), false);
});

// ---------------------------------------------------------------------------
// rewrite
// ---------------------------------------------------------------------------

test('rewrite returns the gsd-test run executor for node --test', () => {
  assert.equal(rewrite('node --test'), 'gsd-test run');
});

test('rewrite returns the gsd-test run executor for npm test', () => {
  assert.equal(rewrite('npm test'), 'gsd-test run');
});

test('rewrite returns same string regardless of input', () => {
  assert.equal(rewrite('CI=1 npm run test -- --reporter=tap'), 'gsd-test run');
});
