# Plan: Make `scripts/run-tests.cjs` reporter-configurable

**Audience:** Claude Code, working inside `gsd-build/get-shit-done`.
**Type:** Enhancement (`approved-enhancement`).
**Effort:** 30–60 minutes including tests + changeset.

## Problem

`scripts/run-tests.cjs` always invokes `node --test` with the default reporter
(human spec output). External tooling — specifically the dockerized `gsd-test`
runner — needs the JSON Lines reporter for machine-readable results.

Today the only way to get JSON is to bypass `npm test` entirely and call
`node --test --test-reporter=json tests/*.test.cjs` directly. That also
bypasses the `pretest` chain (`npm run build:sdk && npm run lint:skill-deps`),
forcing every caller to replicate it by hand. If new pretest steps land,
external callers silently miss them.

## Proposed change

In `scripts/run-tests.cjs`, accept an optional `TEST_REPORTER` env var
(mirroring the existing `TEST_CONCURRENCY` pattern). When set, pass
`--test-reporter=<value>` through to the `node --test` child process.

Default behavior (no env var) is unchanged: spec reporter, all existing
behavior preserved.

### Diff sketch

Before (around lines 22–30):

    const concurrency = process.env.TEST_CONCURRENCY
      ? `--test-concurrency=${process.env.TEST_CONCURRENCY}`
      : '--test-concurrency=4';

    try {
      execFileSync(process.execPath, ['--test', concurrency, ...files], {
        stdio: 'inherit',
        env: { ...process.env },
      });
    } catch (err) {
      process.exit(err.status || 1);
    }

After:

    const concurrency = process.env.TEST_CONCURRENCY
      ? `--test-concurrency=${process.env.TEST_CONCURRENCY}`
      : '--test-concurrency=4';

    // Allow opt-in reporter override (e.g. TEST_REPORTER=json for JSON Lines).
    // Validate to give a clear error on garbage values; execFileSync is already
    // injection-safe but a typo'd value would fail with a confusing message.
    const REPORTER_NAME_RE = /^[a-zA-Z0-9_\-./@]+$/;
    if (process.env.TEST_REPORTER && !REPORTER_NAME_RE.test(process.env.TEST_REPORTER)) {
      console.error(`Invalid TEST_REPORTER value: ${process.env.TEST_REPORTER}`);
      console.error('Allowed characters: letters, digits, _ - . / @');
      process.exit(2);
    }
    const reporterArgs = process.env.TEST_REPORTER
      ? [`--test-reporter=${process.env.TEST_REPORTER}`]
      : [];

    const args = ['--test', concurrency, ...reporterArgs, ...files];

    try {
      execFileSync(process.execPath, args, {
        stdio: 'inherit',
        env: { ...process.env },
      });
    } catch (err) {
      process.exit(err.status || 1);
    }

About six net new lines of production code.

## Tests

Add `tests/run-tests-cjs.test.cjs` (follow existing conventions: `node:test`,
`node:assert/strict`, `execFile` to spawn the script as a subprocess).

Cases:
1. `npm test` without `TEST_REPORTER` works exactly as today (regression).
2. `TEST_REPORTER=json npm test` produces JSON Lines on stdout — first line
   parses as JSON with a recognized `type` field.
3. `TEST_REPORTER='json; rm'` exits 2 with "Invalid TEST_REPORTER value" on stderr.
4. `TEST_CONCURRENCY=1` still respected (regression).

For test isolation, either point the cases at a tiny fixture test file under
`tests/fixtures/run-tests-cjs/`, or refactor `run-tests.cjs` into a small
library function (`buildArgs({env})`) and unit-test that directly.

## Docs

Add a short paragraph wherever test-running is documented (likely `CONTEXT.md`
or a `docs/agents/*.md` file). One paragraph:

> By default, `npm test` uses Node's spec reporter. To get JSON Lines output
> (for CI integrations or remote runners), set the `TEST_REPORTER` env var:
> `TEST_REPORTER=json npm test`. Any reporter name supported by
> `node --test --test-reporter=<name>` is accepted.

## Changeset

    npm run changeset -- --type Added --pr <PR_NUM> --body "Support TEST_REPORTER env var in \`npm test\` for machine-readable output (e.g. \`TEST_REPORTER=json\`)."

## Acceptance criteria

- `npm test` with no env vars behaves identically to before.
- `TEST_REPORTER=json npm test` produces JSON Lines parseable line-by-line by `jq -c .`.
- `TEST_REPORTER=tap npm test` produces TAP (free bonus check).
- `TEST_CONCURRENCY=2 npm test` still works.
- `TEST_REPORTER='json; rm'` exits 2 with a clear error.
- New tests added; existing tests still pass.
- `npm run lint:tests`, `npm run lint:descriptions` pass.
- `.changeset/<name>.md` present.
- Docs updated in exactly one location.

## Non-goals

- CLI flag (env var is enough; matches existing `TEST_CONCURRENCY` pattern).
- Multiple simultaneous reporters.
- Changing the default reporter.
- Anything related to `c8` / coverage reporting.

## Filing as a GitHub issue

    gh --repo gsd-build/get-shit-done issue create \
      --title "Support TEST_REPORTER env var in npm test" \
      --label approved-enhancement \
      --body-file ~/projects/dev-tools/get-shit-done/project-plan-reporter-flag.md
