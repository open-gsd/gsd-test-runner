---
name: run-and-die-tests
description: Run a project's Node tests without leaking orphaned processes onto this machine. Use whenever you would run `node --test`, `npm test`, or `npm run test` — call `gsd-test run` instead of spawning the test process locally. Also use when a run comes back reaped, or when a previous local test run left hung `node` processes behind.
---

# Run-and-die tests via `gsd-test run`

Never run `node --test` / `npm test` directly. A hanging test leaks orphaned
`node` processes onto this workstation and can take it down. Instead run
`gsd-test run`: it executes the suite in a disposable container on a Bench under
a watchdog that kills runaways and then dies, and prints the same `node --test`
verdict you would expect — so you use it exactly like `node --test`.

## Running tests

Swap `node --test` (or `npm test`) for `gsd-test run`:

```bash
gsd-test run                      # run the whole suite
gsd-test run src/foo.test.mjs     # run specific test files (path patterns)
gsd-test run --target windows     # pick the OS (linux | windows | macos-container)
gsd-test run --estimate-ms 120000 # hint the suite's normal duration in ms
```

`gsd-test run` infers the repo from the current directory. `--estimate-ms` lets
the watchdog kill a runaway early instead of waiting out the hour; give it
whenever you know the suite's normal duration.

## Reading the result

`gsd-test run` prints a `node --test`-style summary and sets its exit code:

- `0` — all tests passed.
- `1` — a test failed **or** the run was reaped (a runaway killed by the watchdog).
- `2` — the run could not start (read stderr).

The **last line of stdout is a machine verdict** — one compact JSON object
(`{"type":"verdict","outcome":…,"unique_failures":…,"top":[…],"artifacts":{…}}`).
For the fastest read of a failure, parse that line (or grep `"type":"verdict"`),
then open the artifacts it points at: `FAILURES.md` (one bounded block per unique
failure, with a `file:line`, class, and evidence) and `failures.json` (the same,
full and untruncated) under `$XDG_STATE_HOME/gsd-test/runs/<run-id>/`. That is
one read instead of scrolling the stream — and `junit.xml` is there too for CI
tooling.

A **REAPED** block means the run exceeded its deadline and was killed — a real
signal, not a flake. It names the runaway (the last active / in-flight test).
Fix that test (a leaked timer, socket, or infinite loop); do **not** just raise
the estimate. If the runaway is unattributed, the test wedged the runner
synchronously — check the last `✔` line; the culprit is usually the next test.

A `⚠ left a handle open at exit` note on a passing test flags a leak (a dangling
timer/socket/child) even though the test passed — worth fixing.

## Do not

- Do not fall back to running `node --test` locally if `gsd-test run` fails —
  fix the invocation or the Bench instead.
- Do not retry a reaped run unchanged hoping it passes — diagnose the runaway.

## Advanced

For programmatic use or a raw JSON envelope, the lower-level front door is
`gsd-test submit --execute --spec-file -` (pipe a JSON run spec). `gsd-test run`
is the wrapper over it and is what you want for everyday test runs.

See the project docs: `docs/run-and-die.md` (concepts),
`docs/run-and-die-how-to.md` (tasks), `docs/run-and-die-reference.md` (every
field).
