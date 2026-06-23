# Run-and-die How-to Guides

Task-focused recipes for working with run-and-die. Each assumes you already know what you want to do. For the schemas and flags these reference, see the [reference](run-and-die-reference.md); for the concepts, see [Run-and-die Execution](run-and-die.md).

## How to stop an agent from running tests locally

The whole point is that an agent never spawns a local `node --test`. Run the installer once per project:

```bash
gsd-test install-agent-hooks
```

This installs both the Claude Code hook and the Codex shim in one step. It is idempotent — re-running converges without duplicating anything.

**Claude Code.** The installer merges a `PreToolUse` Bash guard into `.claude/settings.json` and installs the `run-and-die` skill into `.claude/skills/`. The hook denies any `node --test` / `npm test` command and routes the agent to `gsd-test run`. The skill teaches the agent what `gsd-test run` returns and how to read a reaped result. Unrelated commands pass through untouched.

**Codex.** The installer writes a `codex-bin/` directory with `node` and `npm` PATH shims. Put that directory first on Codex's exec PATH in `~/.codex/config.toml`:

```toml
[shell_environment_policy.set]
PATH = "<abs>/.gsd-test/codex-bin:${PATH}"
```

(The exact path is printed by the installer.) When Codex runs `node --test` or `npm test`, the shim redirects it to `gsd-test run`. Every other command is passed through to the real binary. Your interactive shell is unaffected.

To install only one runtime use `--claude` or `--codex`. To install globally (into `$HOME`) add `--global`.

See [`agent-integration/README.md`](../agent-integration/README.md) and the [reference](run-and-die-reference.md#gsd-test-install-agent-hooks) for full flag details.

## How to verify the agent handoff is installed

The fastest check is to run `gsd-test run` directly from the project root:

```bash
gsd-test run
```

The stderr banner `↪ gsd-test: handed off to Docker (run-id=..., target=linux) — do not re-run locally` confirms the handoff is active and the run has been dispatched.

For Claude Code specifically, trigger any `node --test` command during an agent session. The hook should deny it with a message naming `gsd-test run` — the agent will then call `gsd-test run` instead.

For Codex, check that `codex-bin/` appears first in the PATH Codex sees, then trigger a test run — the shim should redirect it transparently and the banner will appear on stderr.

To reverse the install at any time:

```bash
gsd-test install-agent-hooks --uninstall
```

See the [reference](run-and-die-reference.md#gsd-test-install-agent-hooks) for all flags.

## How to dispatch tests without blocking

Use `--async` when the agent can continue working while the suite runs and only needs the verdict later:

```bash
gsd-test run --async
```

The command prints a dispatched-notice with the run-id and returns immediately. The run continues in a detached worker.

To check progress without waiting:

```bash
gsd-test status <run-id>
```

To block until the run completes and collect the full verdict:

```bash
gsd-test wait <run-id>
```

`wait` exits with the same codes as blocking `run` (`0` passed, `1` failed or reaped, `2` infra error). It never returns a partial result. The 90-minute absolute backstop means it cannot hang indefinitely.

`--async` is unix-only. Blocking `gsd-test run` (the default) works everywhere and is the right choice when the agent needs the verdict before continuing. See the [reference](run-and-die-reference.md#gsd-test-run---async-wait-and-status) for flag details. By default `wait` releases the run's artifacts once it has printed the result; pass `--keep` (see [How to keep a run's artifacts](#how-to-keep-a-runs-artifacts)) if you need them to persist.

## How to keep a run's artifacts

By default a run's on-disk artifacts are released once you collect the result (see [ephemeral mode](run-and-die.md#artifact-lifecycle-and-ephemeral-mode)). To keep them for one run:

```bash
gsd-test run --async --keep --target linux < spec.json
gsd-test wait <run-id>          # renders the result but does NOT delete the run
```

`--keep` requires `--async`: it tells the later `wait` to preserve the run. The files stay under `$XDG_STATE_HOME/gsd-test/runs/<run-id>/`.

To keep artifacts for every run, set it in config instead:

```toml
[storage]
keep_artifacts = true
```

A blocking `gsd-test run` (no `--async`) always leaves its artifacts on disk for you to read immediately; they are reclaimed by the retention sweep on a later run unless `keep_artifacts` is set.

## How to set an artifact retention policy

When you keep artifacts, bound how many accumulate with `[storage]`:

```toml
[storage]
artifact_ttl = "72h"     # delete runs older than three days
keep_last_runs = 25      # ...and never keep more than the 25 newest
```

The sweep runs at the start of each `gsd-test run`. Set `keep_artifacts = true` to disable it entirely, or `artifact_ttl = "0"` to drop the age bound. Full field definitions are in the [configuration reference](configuration.md#storage).

## How to test a PR-merged tree

To run the suite against a branch merged onto its base — without checking out and merging yourself — give `base` and `prBranch` instead of pointing `repo` at a pre-merged checkout:

```bash
echo '{"repo":"'"$PWD"'","target":"linux","base":"main","prBranch":"feat/x"}' \
  | gsd-test submit --execute --spec-file -
```

The Engine resolves both refs in `repo`, constructs a PR-merged worktree (a real `git merge`; conflicts surface as a failure before any container starts), and runs that. Set both fields or neither — supplying only one is rejected.

## How to submit a run from your own tooling

Pipe a spec to `gsd-test submit` on stdin and parse the JSON it returns:

```bash
echo '{"repo":"'"$PWD"'","target":"linux","budget":{"estimateMs":120000}}' \
  | gsd-test submit --execute --spec-file -
```

`--spec-file -` reads stdin. Without `--execute`, `submit` only validates and normalises the spec (assigning a `runId`) and prints it back — use that to check a spec before running it.

The exit code tells you the outcome at a glance: `0` passed, `1` failed or reaped, `2` the spec was invalid or the run could not start. The full result is the [result envelope](run-and-die-reference.md#result-envelope) on stdout.

## How to find which test is running away

When a run comes back `"outcome": "reaped"`, the `kill` block tells you where it died:

```bash
gsd-test submit --execute --spec-file spec.json | jq '.kill.last_active_test, .kill.in_flight_tests'
```

- `kill.last_active_test` is the test that was running when the deadline fired — your prime suspect.
- `kill.in_flight_tests` lists everything still executing, with `started_ms_ago` so you can see what had been running longest.

If `kill.granularity` is `"process"`, the run used `isolation: "none"` and these fields are best-effort — re-run with the default process isolation to get exact per-test attribution.

If `kill.last_active_test` is empty, the runaway most likely wedged the runner with a synchronous CPU loop, which blocks the reporter from emitting events. Look at `per_test` for the last test with `status: "passed"` — the culprit is usually the next one to start — or add focused logging to the suspect file and re-run.

For the pattern *across* runs rather than a single one, read the leaderboard (below).

## How to see your repeat offenders

Telemetry accumulates per-repo on your workstation. Each run appends a record to:

```
$XDG_STATE_HOME/gsd-test/<repo>/telemetry.jsonl
```

(falling back to `~/.local/state/gsd-test/...`). A test that trips the reaper across several runs is a bugged test, not a slow one — fix it rather than raising its estimate. Inspect the raw log with `jq`:

```bash
jq -r 'select(.reaped) | .reap_reason + "\t" + .run_id' \
  ~/.local/state/gsd-test/<repo>/telemetry.jsonl
```

See the [telemetry record reference](run-and-die-reference.md#telemetry-record) for the fields.

## How to tune the deadline

The watchdog kills at `min(estimateMs × overrunFactor, hardCapMs)`, floored at 30 seconds and timed from the start of the test run (not the container start). Adjust the `budget` in your spec:

- **Give an estimate.** Set `budget.estimateMs` to how long the suite normally takes. This is the single most useful knob — it lets the reaper kill a runaway early instead of waiting out the hour.
- **If a suite legitimately varies a lot,** raise `budget.overrunFactor` (default `1.5`) rather than inflating the estimate.
- **If you omit `estimateMs` entirely,** the deadline falls back to the median of recent passing runs, then to `budget.hardCapMs` (default one hour) when there is no history.
- **To cap the absolute worst case,** lower `budget.hardCapMs`.

## How to choose an isolation mode

- **Keep the default `"process"`** for any suite you do not fully trust. A wedged test is a contained child the watchdog reaps with exact attribution, and `node --test`'s own per-test timeout can catch many hangs before the watchdog needs to.
- **Use `"none"` only for a known-clean, fast suite** where you want to skip the per-file process spawn. Be aware that a single hang then wedges the whole runner — only the watchdog can recover it, and the kill record's test attribution becomes best-effort.

## How to run the same spec against more than one OS

A run spec targets one OS. To cover Linux and Windows, submit two specs:

```bash
for os in linux windows; do
  jq --arg t "$os" '.target = $t' base-spec.json \
    | gsd-test submit --execute --spec-file -
done
```

Each run picks its own Bench and Tester Image for that target and returns its own report. There is no cross-OS comparison — you read one report per OS, as with the ordinary `gsd-test` flow.
