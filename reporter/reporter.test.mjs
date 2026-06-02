import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import reporter from './reporter.mjs';

/**
 * Drive the reporter with a fixed array of events and return all parsed
 * JSON records it yields.
 *
 * @param {Array<{type:string, data:object}>} events
 * @returns {Promise<Array<object>>}
 */
async function collectOutput(events) {
  const source = (async function* () {
    for (const e of events) yield e;
  })();

  const records = [];
  for await (const line of reporter(source)) {
    records.push(JSON.parse(line.trim()));
  }
  return records;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Build a minimal Error with a controlled stack for deterministic assertions.
 */
function makeError(message, stack) {
  const err = new Error(message);
  err.stack = stack || `Error: ${message}\n  at x`;
  return err;
}

// ---------------------------------------------------------------------------
// Test 1 (tracer bullet — proves the bug)
// ---------------------------------------------------------------------------
test('test:pass with data.name produces a test_event with non-empty name', async () => {
  const events = [
    {
      type: 'test:pass',
      data: {
        name: 'my passing test',
        file: '/work/foo.test.js',
        details: { duration: 5 },
      },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  const rec = records[0];
  assert.equal(rec.type, 'test_event');
  assert.equal(rec.kind, 'pass');
  assert.equal(rec.name, 'my passing test');
});

// ---------------------------------------------------------------------------
// Test 2: test:fail with data.name produces non-empty name and serializes error
// ---------------------------------------------------------------------------
test('test:fail with data.name produces non-empty name and serializes error', async () => {
  const err = makeError('boom', 'Error: boom\n  at x');
  const events = [
    {
      type: 'test:fail',
      data: {
        name: 'my failing test',
        file: '/work/foo.test.js',
        details: { error: err, duration: 3 },
      },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  const rec = records[0];
  assert.equal(rec.kind, 'fail');
  assert.equal(rec.name, 'my failing test');
  assert.equal(rec.error, 'boom');
  assert.equal(rec.error_class, 'throw');
  assert.ok(rec.stack.startsWith('Error: boom'), `stack should start with 'Error: boom', got: ${rec.stack}`);
});

// ---------------------------------------------------------------------------
// Test 3 (regression guard): hook failure WITH data.context preserves context-walk
// ---------------------------------------------------------------------------
test('hook failure with data.context preserves context-walk and hook error_class', async () => {
  const err = makeError('hook crashed');
  const events = [
    {
      type: 'test:fail',
      data: {
        context: {
          name: 'before each',
          type: 'beforeEach',
          parent: { name: 'my suite' },
        },
        file: '/work/foo.test.js',
        details: { error: err, duration: 1 },
      },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  const rec = records[0];
  assert.equal(rec.name, 'my suite > before each');
  assert.equal(rec.error_class, 'setup');
});

// ---------------------------------------------------------------------------
// Test 4 (counter-test): events other than test:pass/test:fail pass through verbatim
// ---------------------------------------------------------------------------
test('test:diagnostic events pass through verbatim', async () => {
  const events = [
    {
      type: 'test:diagnostic',
      data: { message: 'note', file: '/work/foo.test.js' },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  const rec = records[0];
  assert.equal(rec.type, 'test:diagnostic');
  assert.equal(rec.data.message, 'note');
});

// ---------------------------------------------------------------------------
// Test 5 (negative-space counter-test): no data.name and no data.context → empty name
// ---------------------------------------------------------------------------
test('test:pass with neither data.name nor data.context yields empty name without crashing', async () => {
  const events = [
    {
      type: 'test:pass',
      data: { file: '/work/foo.test.js', details: { duration: 0 } },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  const rec = records[0];
  assert.equal(rec.type, 'test_event');
  assert.equal(rec.name, '');
});

// ---------------------------------------------------------------------------
// Test 6: /work/ container paths are stripped to repo-relative
// ---------------------------------------------------------------------------
test('repoRelative strips /work/ prefix from container paths', async () => {
  const events = [
    {
      type: 'test:pass',
      data: {
        name: 'container path test',
        file: '/work/tests/a.test.cjs',
        details: { duration: 1 },
      },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  assert.equal(records[0].file, 'tests/a.test.cjs');
});

// ---------------------------------------------------------------------------
// Test 7: host-absolute paths under cwd become repo-relative
// ---------------------------------------------------------------------------
test('repoRelative converts host-absolute path under cwd to repo-relative', async () => {
  // Construct a path that is under the current working directory.
  // process.cwd() is the repo root when running `node --test` from the repo.
  // We use path.join here only to build the input value for the reporter.
  const { join } = await import('node:path');
  const absoluteFile = join(process.cwd(), 'tests', 'b.test.cjs');

  const events = [
    {
      type: 'test:pass',
      data: {
        name: 'host path test',
        file: absoluteFile,
        details: { duration: 1 },
      },
    },
  ];

  const records = await collectOutput(events);
  assert.equal(records.length, 1);
  // The reporter must produce a relative path, never the absolute host path.
  assert.equal(records[0].file, 'tests/b.test.cjs',
    `Expected repo-relative 'tests/b.test.cjs', got: ${records[0].file}`);
  assert.ok(!records[0].file.startsWith('/'),
    `file must not be absolute, got: ${records[0].file}`);
});
