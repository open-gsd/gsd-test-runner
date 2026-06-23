# Failure-first Output How-to Guides

Task-focused recipes for working with `gsd-test` run output. Each assumes you
already know what you want to do. For the schemas these reference see the
[output reference](failure-first-output-reference.md); for the concepts see
[Failure-first Output](failure-first-output.md).

## How to read a failed run

When a run comes back non-zero, do not scroll the stream. Work from the verdict
down:

1. **Read the verdict** — the last line of stdout. It names the outcome, the
   failure counts, the top failures, and where the rest lives:

   ```bash
   gsd-test | tail -n1 | jq .
   ```

2. **Open `FAILURES.md`** — the human-readable digest, headline first. Its path is
   in the verdict's `artifacts.failures_md`:

   ```bash
   jq -r '.artifacts.failures_md' <(gsd-test | tail -n1)
   ```

   In practice you will usually just `cat` it after a run, or have your agent read
   it directly.

3. **Open `failures.json`** only when a block in `FAILURES.md` was truncated and
   you need the full stack or captured output. The truncation pointer
   (`… full at failures.json#/failures/0/stack`) tells you exactly where to look.

This is one cheap read instead of grepping a multi-megabyte scrollback.

## How to find a run's artifact directory

Every run prints the directory in its verdict line:

```bash
gsd-test | tail -n1 | jq -r '.artifacts.dir'
```

If you missed it, the directory lives under
`$XDG_STATE_HOME/gsd-test/runs/<run-id>/` (falling back to
`~/.local/state/gsd-test/runs/<run-id>/`). List recent runs by modification time:

```bash
ls -t ~/.local/state/gsd-test/runs/
```

## How to control how much streams to your terminal

The default (Normal) shows a per-OS heartbeat, leg events, and loud failures.
Change it when you need to:

- **See the full firehose** — every passing test and all `npm ci` / build output.
  Use this when debugging the harness, not your tests:

  ```bash
  gsd-test --verbose
  # or, e.g. in a script or CI env:
  GSD_TEST_VERBOSE=1 gsd-test
  ```

- **Drop even the heartbeat** — leave only leg events and failures:

  ```bash
  gsd-test --quiet
  ```

- **Get the full machine stream** — every typed event as JSON Lines, regardless
  of verbosity:

  ```bash
  gsd-test --json-events
  ```

The verdict line is always the last line of stdout, in every one of these modes.

## How to script against the verdict line

The verdict is the last line of stdout and is tagged `"type":"verdict"`, so it is
unambiguous to extract:

```bash
verdict=$(gsd-test | tail -n1)
echo "$verdict" | jq -r '.outcome'                       # passed | failed | reaped | infra_error
echo "$verdict" | jq -r '.unique_failures'               # distinct failures
echo "$verdict" | jq -r '.top[] | "\(.file):\(.line) \(.name)"'
```

If other lines may be interleaved (you are not certain it is the last line), grep
for the discriminator instead:

```bash
gsd-test 2>/dev/null | grep '"type":"verdict"' | jq -r .outcome
```

Trust the **exit code** for pass/fail in scripts — `0` passed, `1` failed/reaped,
`2` infra error — and use the verdict for the detail. Artifact write failures
never change the exit code.

## How to feed JUnit XML into CI

Every run writes a `junit.xml` whose path is in the verdict. Point your CI's
JUnit collector at it:

```bash
gsd-test
junit=$(gsd-test --json-events 2>/dev/null | grep '"type":"verdict"' \
  | jq -r '.artifacts.junit_xml')
cp "$junit" "$CI_ARTIFACTS_DIR/gsd-test-junit.xml"
```

`junit.xml` carries one `<testsuite>` per OS, with failures as
`<testcase><failure>` and passing tests reflected in the suite attributes. It is
written on every run, on both the standard and run-and-die paths.

## How to get the full per-test event stream

The standard path persists the raw reporter events per OS as
`test-events-<os>.jsonl` in the artifact directory — useful for custom analysis
that the digest does not cover:

```bash
dir=$(gsd-test | tail -n1 | jq -r '.artifacts.dir')
jq -c 'select(.type=="test:fail")' "$dir"/test-events-linux.jsonl
```

This is the same data the digest is built from, unaggregated. (The run-and-die
path does not write this file — use its result envelope instead.)

## How to tell an infra error from a test failure

Read the `outcome`, not just the exit code:

- `outcome: "failed"` (exit `1`) — your tests failed. Open `FAILURES.md`.
- `outcome: "infra_error"` (exit `2`) — the suite did not run as designed: a leg
  failed, a Bench was skipped, or the run could not start. The verdict's `per_os`
  will be empty or partial. See [Troubleshooting](troubleshooting.md).
- `outcome: "reaped"` (exit `1`) — a run-and-die run exceeded its deadline and was
  killed. See [How to find which test is running away](run-and-die-how-to.md#how-to-find-which-test-is-running-away).

```bash
case "$(gsd-test | tail -n1 | jq -r .outcome)" in
  passed)      echo "safe to push" ;;
  failed)      echo "test failures — read FAILURES.md" ;;
  infra_error) echo "infra problem — see troubleshooting" ;;
esac
```
