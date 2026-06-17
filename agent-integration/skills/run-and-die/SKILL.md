---
name: run-and-die-tests
description: Run a project's Node tests without leaking orphaned processes onto this machine. Use whenever you would run `node --test`, `npm test`, or `npm run test` — submit a run spec to `gsd-test submit` instead of spawning the test process locally. Also use when reading a run result that came back reaped, or when a previous local test run left hung `node` processes behind.
---

# Run-and-die tests via `gsd-test submit`

Never run `node --test` / `npm test` directly. A hanging test leaks orphaned
`node` processes onto this workstation and can take it down. Instead submit a
**run spec** to the Local Engine, which runs the suite in a disposable container
on a Bench under a watchdog that kills runaways and then dies.

## Submitting a run

Pipe a JSON run spec to `gsd-test submit --execute --spec-file -`:

```bash
echo '{"repo":"'"$PWD"'","target":"linux","budget":{"estimateMs":120000}}' \
  | gsd-test submit --execute --spec-file -
```

Minimum fields: `repo` (absolute source path) and `target` (`linux` | `windows`
| `macos-container`). Give `budget.estimateMs` (the suite's normal duration in
ms) whenever you can — it lets the watchdog kill a runaway early instead of
waiting out the hour. To test a PR-merged tree, add `base` and `prBranch`
together instead of pointing `repo` at a pre-merged checkout.

## Reading the result

The command prints one JSON result envelope and sets its exit code:

- `0` — `outcome: "passed"`.
- `1` — `outcome: "failed"` or `"reaped"`.
- `2` — the spec was invalid or the run could not start (read stderr).

When `outcome` is **`reaped`**, the run was killed for exceeding its deadline —
this is a real signal, not a flake. Read `kill.last_active_test` and
`kill.in_flight_tests`: they name the runaway. Fix that test (a leaked timer,
socket, or infinite loop); do **not** just raise the estimate. If
`kill.last_active_test` is empty, the test wedged the runner synchronously
(blocking even the reporter) — check `per_test` for the last `passed` entry; the
culprit is usually the next test to start.

## Do not

- Do not fall back to running `node --test` locally if a submit fails — fix the
  spec or the Bench instead.
- Do not retry a `reaped` run unchanged hoping it passes — diagnose the runaway.

See the project docs: `docs/run-and-die.md` (concepts),
`docs/run-and-die-how-to.md` (tasks), `docs/run-and-die-reference.md` (every
field).
