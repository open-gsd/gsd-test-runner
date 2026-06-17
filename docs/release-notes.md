# Release Notes

This page summarizes the recent `v1.3.x` releases so you can quickly decide what to adopt.

## At a glance

- `v1.3.0`: configurable test command in the `run_tests` leg
- `v1.3.1`: shell-aware parsing for string commands + explicit argv command arrays
- `v1.3.2`: per-bench container platform pinning (`linux/amd64`, `linux/arm64`, etc.)
- _Unreleased_: run-and-die — `gsd-test submit` for coding agents, with an estimate-aware watchdog and two-tier reaping

## Unreleased

### Added

- `gsd-test submit` — a run-spec front door so coding agents run tests in a disposable container instead of spawning a local `node --test` that can leak orphaned processes (issue #60, ADR-0021).
- An estimate-aware in-container watchdog and an external reaper that kill runaway suites and report exactly which test ran away (`outcome: "reaped"`, result `schema_version: 2`).
- Per-repo telemetry with a runaway leaderboard, and Claude Code / Codex integration that routes `node --test` to the front door.

### Why it matters

A hanging test run by an agent no longer risks taking down the workstation: execution moves into a container that dies when the run ends, and a reaped run is a loud, structured result pointing at the bugged test — not a silent hang.

### Learn more

Start with [Run-and-die Execution](run-and-die.md) and the [tutorial](run-and-die-tutorial.md).

## v1.3.2

### Added

- `[[benches]].platform` (optional) in `config.toml`
- `docker run --platform <value>` passthrough in the pipeline when `platform` is set

### Why it matters

On mixed hardware benches (for example, Apple Silicon local Docker + x86 Linux host), default container platform selection can vary by host. `platform` gives you deterministic architecture selection across benches.

### Example

```toml
[[benches]]
name = "linux-host"
host = "lab-rig-1"
os = "linux"
platform = "linux/amd64"

[[benches]]
name = "mac-local"
host = "local"
os = "macos"
platform = "linux/amd64"
```

Use the same platform value across benches when you want architecture parity.

## v1.3.1

### Added

- Shell-quote-aware parsing for string-form `testing.command`
- Explicit argv form for `testing.command` via string arrays

### Why it matters

Multi-step commands like `bash -c 'cmd1 && cmd2'` now parse correctly. You can also avoid quoting complexity entirely by using argv arrays.

### Examples

String form:

```toml
[testing]
command = "bash -c 'npm run pretest && node --test tests/*.test.cjs'"
```

Array form:

```toml
[testing]
command = ["bash", "-c", "npm run pretest && node --test tests/*.test.cjs"]
```

## v1.3.0

### Added

- Configurable `run_tests` command through `[testing].command`
- Reporter placeholder substitution:
  - `{{REPORTER_PATH}}`
  - `{{REPORTER_DEST}}`

### Why it matters

Projects are no longer locked to the built-in `node --test ...` command path. You can run your own test orchestration while still integrating with the gsd-test reporter output contract.

### Example

```toml
[testing]
command = [
  "npm",
  "test",
  "--",
  "--test-reporter={{REPORTER_PATH}}",
  "--test-reporter-destination={{REPORTER_DEST}}"
]
```

## Upgrade guide (v1.2.x -> v1.3.x)

1. Upgrade to the latest release binary.
2. Keep your existing config as-is if you do not need custom test orchestration.
3. Add `[testing].command` if your test entrypoint is not the default.
4. Add `[[benches]].platform` if benches run on mixed CPU architectures and you need deterministic parity.

## See also

- [Configuration Reference](configuration.md)
- [Troubleshooting](troubleshooting.md)
