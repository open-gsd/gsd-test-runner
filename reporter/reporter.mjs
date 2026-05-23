// Custom Node test reporter: emits one JSON line per test runner event.
// For test pass/fail events, emits a structured test_event record per
// ADR-0013 (schema_version=1). All other events are emitted as raw
// {type, data} lines for the drain/parse legs.
//
// Handles Error objects so message/stack survive JSON.stringify.

/**
 * Build the fully-qualified test name by walking the context chain.
 * Node's test runner attaches .context.name and .context.parent.
 * We accumulate from the root down, joining with " > ".
 */
function buildTestName(context) {
  const parts = [];
  let cur = context;
  while (cur) {
    if (cur.name) parts.unshift(cur.name);
    cur = cur.parent || null;
  }
  return parts.join(' > ');
}

/**
 * Strip the /work/ container prefix from file paths so the stored path is
 * repo-relative. Leaves non-/work/ paths untouched (handles non-container
 * runs).
 */
function repoRelative(file) {
  if (typeof file === 'string' && file.startsWith('/work/')) {
    return file.slice('/work/'.length);
  }
  return file || '';
}

/**
 * Classify an error object into one of the six ADR-0013 ErrorClass values.
 *   assertion  — AssertionError or ERR_ASSERTION code
 *   timeout    — error message contains "timed out"
 *   setup      — error thrown inside a before/beforeEach hook
 *   teardown   — error thrown inside an after/afterEach hook
 *   throw      — any other unhandled throw inside a test body
 *   unknown    — fallback
 */
function classifyError(err, hookType) {
  if (!err) return 'unknown';
  // Hook-sourced failures take priority over the error type.
  if (hookType === 'before' || hookType === 'beforeEach') return 'setup';
  if (hookType === 'after' || hookType === 'afterEach') return 'teardown';
  if (err.name === 'AssertionError' || err.code === 'ERR_ASSERTION') return 'assertion';
  if (typeof err.message === 'string' && err.message.includes('timed out')) return 'timeout';
  if (err instanceof Error || err.stack) return 'throw';
  return 'unknown';
}

export default async function* (source) {
  for await (const e of source) {
    const type = e.type;
    const data = e.data;

    if (type === 'test:pass' || type === 'test:fail') {
      const ctx = data && data.details;
      const err = ctx && ctx.error;
      const isPassing = type === 'test:pass';

      // Determine hook type if this failure originated inside a lifecycle hook.
      // Node surfaces hook failures as test:fail on the hook's synthetic test
      // node; its context.type may be "before", "after", "beforeEach", or
      // "afterEach".
      const hookType = (data && data.context && data.context.type) || null;

      const name = buildTestName(data && data.context);
      const file = repoRelative(data && data.file);
      const durationMs = (ctx && typeof ctx.duration === 'number') ? ctx.duration : 0;
      const retryCount = (data && typeof data.currentAttempt === 'number')
        ? Math.max(0, data.currentAttempt - 1)
        : 0;

      const record = {
        type: 'test_event',
        kind: isPassing ? 'pass' : 'fail',
        file,
        name,
        duration_ms: durationMs,
        retry_count: retryCount,
      };

      if (!isPassing && err) {
        const errMsg = (typeof err.message === 'string') ? err.message.split('\n')[0] : String(err);
        record.error = errMsg;
        record.error_class = classifyError(err, hookType);
        record.stack = (err && err.stack) ? err.stack : '';
        // Captured output lives on data.details.output in newer Node versions.
        record.output = (ctx && typeof ctx.output === 'string') ? ctx.output : '';
      }

      yield JSON.stringify(record) + '\n';
    } else {
      // All other event types (test:diagnostic, test:plan, test:start, etc.)
      // are passed through verbatim for forward compatibility.
      yield JSON.stringify({ type, data }, (k, v) =>
        v instanceof Error
          ? { name: v.name, message: v.message, stack: v.stack, code: v.code, ...v }
          : v
      ) + '\n';
    }
  }
}
