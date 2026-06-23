# Troubleshooting

Each error below follows the pattern: **Error** (what you see) / **Cause** (why it happens) / **Solution** (how to fix it).

---

## No Bench available for OS=X

**Error**

```text
bench.NewSelector: No Bench available for OS=linux
```

**Cause**

No entry in your `[[benches]]` list has `os = "linux"` (or whichever OS is reported). Either the OS name is misspelled, or you have not added a Bench for that OS.

**Solution**

Check `~/.config/gsd-test/config.toml`. Every OS in `defaults.targets` must have at least one matching `[[benches]]` entry. Add the Bench or remove the OS from `targets`.

---

## Pinned Bench not in registry

**Error**

```text
bench.NewSelector: pinned bench "my-bench" not found in registry
  available: [linux-bench-a win-rig-1]
```

**Cause**

The name passed to `--bench` (or set as `defaults.pin`) does not match any `[[benches]].name` in your config.

**Solution**

Check the spelling of the Bench name. Run `gsd-test --probe-benches` to see all known Bench names from your config.

---

## Pin is in the Exclude list

**Error**

```text
bench.NewSelector: pin "lab-rig-1" is also in the exclude list
```

**Cause**

You passed `--bench lab-rig-1` and `--exclude lab-rig-1` in the same invocation (or both are set in config defaults).

**Solution**

Remove the Bench from the exclude list, or do not pin to it.

---

## image-version mismatch {#image-version-mismatch}

**Error**

```text
[linux] check_image_version ✗
  image ghcr.io/open-gsd/gsd-tester-linux:v1.0.0 on bench lab-rig-1:
  expected version "v1.0.0", got "v0.9.0"
```

**Cause**

The Tester Image on the Bench has a different version than what `[versions].linux` in your config specifies. The image on the Bench is stale.

**Solution**

Pull the correct version on the Bench:

```bash
ssh lab-rig-1 docker pull ghcr.io/open-gsd/gsd-tester-linux:v1.0.0
```

Or update `[versions].linux` in your config to match the version already on the Bench. The version in config should match the current release.

---

## Cannot connect to the Docker daemon

**Error**

```text
[linux] check_image_version ✗
  bench lab-rig-1: docker image inspect ... failed:
  Cannot connect to the Docker daemon at unix:///var/run/docker.sock.
  Is the docker daemon running?
```

**Cause**

Either the Docker daemon on the Bench is not running, or `gsd-test` cannot reach the Bench over SSH.

**Solution**

1. Confirm SSH works: `ssh lab-rig-1 echo ok`
2. If SSH works, start the Docker daemon on the Bench: `ssh lab-rig-1 sudo systemctl start docker`
3. If SSH fails, check your `~/.ssh/config` and Bench firewall settings.
4. Run `gsd-test --probe-benches` to get a structured reachability report.

---

## Unable to find image / pull failed

**Error**

```text
EnsurePresent(bench=lab-rig-1, image=ghcr.io/open-gsd/gsd-tester-linux:v1.0.0):
  pull failed: unauthorized: authentication required
```

**Cause**

The Bench is not authenticated with GHCR, or the image does not exist at the specified tag.

**Solution**

Log in to GHCR on the Bench:

```bash
ssh lab-rig-1 docker login ghcr.io
```

Enter your GitHub username and a personal access token with `read:packages` scope. If the image is public, unauthenticated pulls should work — confirm the image name and tag are correct.

---

## merge conflict

**Error**

```text
worktree.Construct: refs.Resolve("HEAD"): ...
```

or a conflict error during worktree construction.

**Cause**

`gsd-test` constructs a PR-merged worktree by running a real `git merge` of your current HEAD into the base branch. If those branches conflict, the merge fails.

**Solution**

Resolve the conflict in your repo first:

```bash
git fetch origin main
git merge origin/main
# resolve conflicts
git commit
```

Then re-run `gsd-test`.

---

## test events file is empty

**Error**

```text
[linux] parse ✗
  parse failed: ...
```

or a parse leg failure with no test events in the output.

**Cause**

The `node --test` process inside the container ran but did not write any events to the JSONL capture file. This usually means the test command did not match any test files, or the reporter path is wrong.

**Solution**

1. Confirm `node --test` finds your test files locally.
2. Check that your project's test files follow the `node --test` discovery pattern (files matching `**/*.test.{js,mjs,cjs}` etc.).
3. Confirm the Tester Image has the Reporter at `/opt/gsd-test/reporter.mjs` — this is baked in, so a version mismatch would surface in `check_image_version` first.

---

## container start failed

**Error**

```text
[linux] start_container ✗
  container start failed for image ghcr.io/open-gsd/gsd-tester-linux:v1.0.0 (exit=125):
  Unable to find image '...' locally
```

**Cause**

The Tester Image is not present on the Bench's Docker daemon. The EnsureImages phase should have pulled it, but the pull may have failed silently or been skipped.

**Solution**

Pull the image manually on the Bench:

```bash
ssh lab-rig-1 docker pull ghcr.io/open-gsd/gsd-tester-linux:v1.0.0
```

Then re-run `gsd-test`.

---

## Exit code 2 with no obvious failure

**Error**

`gsd-test` exits with code 2 but the terminal output is sparse or incomplete.

**Cause**

Exit code 2 means an infrastructure problem — a pipeline did not produce a result because a leg failed or a Bench was skipped. Check `stderr` for the phase that reported the error.

**Solution**

Look at the stderr output for the specific phase:

| stderr prefix | Phase that failed | What to check |
|---------------|-------------------|---------------|
| `config.Load:` | Phase 1 (Load) | Syntax errors or missing fields in `config.toml` |
| `bench.NewSelector:` | Phase 2 (Plan) | Bench name typos, pin/exclude conflicts |
| `plan.Build:` | Phase 2 (Plan) | Missing `[versions]` entry for a targeted OS |
| `EnsurePresent(bench=..., image=...):` | Phase 3 (EnsureImages) | GHCR auth, network, or image not found |
| `[<os>] <leg> ✗` in TTY output | Phase 4 (RunPipelines) | Leg-specific failure — see the error detail and `diagnostics:` path |
| `worktree.Construct:` | Pre-phase (worktree) | Merge conflict or git error |

Run with `--json-events` for structured output that is easier to pipe and grep:

```bash
gsd-test --json-events 2>&1 | grep '"kind":"leg_failure"'
```

---

## A run comes back `"outcome": "reaped"`

**Error**

```json
{ "outcome": "reaped", "kill": { "reason": "estimate_overrun", "last_active_test": { "file": "db.test.js", "name": "reconnects" } } }
```

**Cause**

A `gsd-test submit` run exceeded its deadline and the watchdog killed it. This is a loud, intended result — not a crash. `kill.reason` is `estimate_overrun` (ran past `estimate × overrunFactor`), `hard_cap` (hit the one-hour ceiling), or `external_reaper` (the in-container watchdog wedged and the Engine killed the container).

**Solution**

Read `kill.last_active_test` — that is the test that was running when the deadline fired. Fix the runaway (a leaked timer, socket, or infinite loop) rather than raising the estimate. If the same test reaps across runs, it is on your [runaway leaderboard](run-and-die-how-to.md#how-to-see-your-repeat-offenders). If the run was *not* actually runaway, your estimate is too low — raise `budget.estimateMs`. See the [run-and-die how-to guides](run-and-die-how-to.md).

---

## submit --execute: image version mismatch

**Error**

```text
submit --execute: image ghcr.io/open-gsd/gsd-tester-linux version mismatch: want "v1.4.0", got "v1.3.0"
```

**Cause**

The Tester Image present on the Bench carries a different `sh.gsd-test.image-version` sentinel than the version configured for this target in `[versions]`. The run was stopped before starting so a stale image cannot silently produce wrong results.

**Solution**

Pull or rebuild the Tester Image for that target so its version matches `[versions].<os>` in your config, or update the configured version. See [Configuration Reference](configuration.md) and [Setting up Benches](benches.md).

---

## Run artifacts are gone after `gsd-test wait`

**Error**

After `gsd-test wait <run-id>` returns, `$XDG_STATE_HOME/gsd-test/runs/<run-id>/` no longer exists and the verdict line's `artifacts` object is empty.

**Cause**

This is the default ephemeral behavior. `wait` is the terminal consumer of a run — once it has rendered the result to stdout it releases the run and emits the verdict without artifact paths. stdout is the authoritative record (ADR-0023).

**Solution**

If you need the files to persist, dispatch with `--keep` (`gsd-test run --async --keep …`) or set `keep_artifacts = true` under `[storage]`. See [How to keep a run's artifacts](run-and-die-how-to.md#how-to-keep-a-runs-artifacts).

---

## `--keep` had no effect

**Error**

You passed `--keep` but the artifacts were still released, or the flag seemed to be ignored.

**Cause**

`--keep` applies to the async flow — it instructs the later `gsd-test wait` to preserve the run. A blocking `gsd-test run` has nothing to consume-on-read, so `--keep` has no per-run effect there; those artifacts persist until the retention sweep on a later run reclaims them.

**Solution**

Use `gsd-test run --async --keep` followed by `gsd-test wait`. To retain artifacts from blocking runs as well, set `keep_artifacts = true` under `[storage]`.
