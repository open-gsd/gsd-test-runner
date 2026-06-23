# Configuration Reference

`gsd-test` reads a single TOML file at startup. CLI flags override individual config values; flags always win.

## File location

`gsd-test` looks for the config file in this order:

1. The path passed with **`--config <path>`** (explicit override).
2. `$XDG_CONFIG_HOME/gsd-test/config.toml` (if `$XDG_CONFIG_HOME` is set).
3. `~/.config/gsd-test/config.toml` (default).

Create the directory if it does not exist:

```bash
mkdir -p ~/.config/gsd-test
```

## Top-level sections

| Section | TOML syntax | Purpose |
|---------|-------------|---------|
| `[defaults]` | Table | Default values for CLI flags |
| `[[benches]]` | Table-array | One entry per Bench |
| `[versions]` | Table | OS-to-image-version mapping |
| `[testing]` | Table | Optional test command override for RunTests leg |
| `[storage]` | Table | Run-artifact retention policy |

## `[defaults]`

Default values applied when the corresponding CLI flag is not passed.

| Field | Type | Required | Default | CLI override |
|-------|------|----------|---------|--------------|
| `targets` | `[]string` | No | `[]` | `--targets <os,...>` |
| `pin` | `string` | No | `""` | `--bench <name>` |
| `exclude` | `[]string` | No | `[]` | `--exclude <name,...>` |

### `targets`

List of OS names to run tests on. Each entry must match the `os` field of a `[[benches]]` entry.

```toml
[defaults]
targets = ["linux", "windows"]
```

If `targets` is empty and `--targets` is not passed, `gsd-test` exits with an error.

### `pin`

Name of the Bench to always use, bypassing round-robin selection. Useful when you have multiple Benches for one OS and want to pin to a specific one.

```toml
[defaults]
pin = "lab-rig-1"
```

### `exclude`

List of Bench names to exclude from selection. Useful for temporarily removing a Bench without deleting its entry.

```toml
[defaults]
exclude = ["win-rig-2"]
```

## `[[benches]]`

Each `[[benches]]` block declares one Bench. All fields in a single block describe one machine.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Unique Bench identifier. Used by `--bench` and `--exclude`. |
| `host` | `string` | No | `""` | SSH host alias from `~/.ssh/config`. Set to `"local"` or leave empty to use the Dev Workstation's own Docker daemon. |
| `os` | `string` | **Yes** | — | Target OS this Bench provides. One of `"linux"`, `"windows"`, `"macos"`. |
| `runtime` | `string` | No | `"docker"` | Container runtime. `"docker"` is the default for all Benches today (Linux, Windows, and macOS). `"container"` is reserved for future Apple Containers support (requires macOS 26; not usable today — see ADR-0020). |
| `platform` | `string` | No | `""` | Optional OCI platform pin for `docker run`, e.g. `"linux/amd64"` or `"linux/arm64"`. |

### `name`

Must be unique across all `[[benches]]` entries. Appears in event streams, error messages, and `--bench`/`--exclude` arguments.

```toml
[[benches]]
name = "linux-bench-a"
```

### `host`

An SSH host alias matching a `Host` block in `~/.ssh/config`. `gsd-test` connects via `DOCKER_HOST=ssh://<host>`.

```toml
[[benches]]
name = "lab-rig-1"
host = "lab-rig-1"   # resolves via ~/.ssh/config
os   = "linux"
```

Leave empty or set to `"local"` to run against the Dev Workstation's local Docker daemon. This is uncommon — the Local Engine is designed to offload container work to Benches, not run it locally.

### `os`

The OS family this Bench targets. `gsd-test` uses this to select the right Tester Image and to route `--targets` entries to the correct Bench.

```toml
[[benches]]
name = "win-rig-1"
host = "win-rig-1"
os   = "windows"
```

### `platform`

Optional platform pin passed as `docker run --platform <value>`.

Use this when you need deterministic architecture behavior across benches (for example, forcing both a remote Linux bench and a local macOS Docker bench to run `linux/amd64`).

```toml
[[benches]]
name = "mac-local"
host = "local"
os   = "macos"
platform = "linux/amd64"
```

## `[versions]`

A map from OS name to expected Tester Image version tag. `gsd-test` verifies this version against the `sh.gsd-test.image-version` OCI label on the Tester Image during the `check_image_version` leg. A mismatch causes an immediate fail-loud error before any tests run.

| Key | Type | Required | Example |
|-----|------|----------|---------|
| `<os>` | `string` | Yes, for each targeted OS | `linux = "v1.0.0"` |

```toml
[versions]
linux   = "v1.0.0"
windows = "v1.0.0"
```

You must add a `[versions]` entry for every OS in `defaults.targets`. If an OS has no version entry, `plan.Build` skips it with reason `no_image_version`.

## `[testing]`

Optional test command template for the `run_tests` leg.

| Field | Type | Required | Default |
|-------|------|----------|---------|
| `command` | `string` or `[]string` | No | `["node","--test","--test-reporter={{REPORTER_PATH}}","--test-reporter-destination={{REPORTER_DEST}}"]` |

If omitted, behavior is unchanged from current releases.

Placeholder tokens supported in `testing.command`:

- `{{REPORTER_PATH}}` → `/opt/gsd-test/reporter.mjs`
- `{{REPORTER_DEST}}` → `/work/test-events.jsonl`

`command` string values are split with shell-style quote handling.
`command` array values are passed directly as argv (recommended for multi-step `bash -c` style commands).

Examples:

```toml
[testing]
command = "npm test -- --test-reporter={{REPORTER_PATH}} --test-reporter-destination={{REPORTER_DEST}}"
```

```toml
[testing]
command = ["bash", "-c", "npm run pretest && node --test tests/*.test.cjs"]
```

## `[storage]`

Controls run-artifact retention. By default, `gsd-test` operates in **ephemeral mode**: after `gsd-test wait` renders the result, it releases (deletes) the run's state file and artifact directory, and emits the verdict without artifact paths (stdout is the authoritative record). Use `[storage]` or the per-invocation `--keep` flag to opt out.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `keep_artifacts` | `bool` | `false` | When `true`, opt all runs out of ephemeral auto-release globally. Equivalent to passing `--keep` on every `gsd-test run --async` invocation. |
| `artifact_ttl` | `string` | `""` | Duration string (e.g. `"72h"`, `"7d"`). Runs older than this are pruned at the start of each `gsd-test run`. Ignored when `keep_artifacts = true` or `--keep` is passed. |
| `keep_last_runs` | `int` | `0` | Keep at most this many completed runs (newest first) regardless of age. `0` = no count bound. Ignored when `keep_artifacts = true` or `--keep` is passed. |

### Ephemeral vs non-ephemeral

**Ephemeral (default):**

- `gsd-test wait` renders the run result to stdout (self-contained), emits a verdict line with empty artifact paths, then calls `runstate.Release` to delete the state file and run directory.
- `gsd-test status` never releases — it is a pure reporter regardless of ephemeral mode.
- Blocking `gsd-test run` (non-async) is reclaimed by the prune pass on the next invocation — no in-process deletion.
- Old runs are pruned at startup according to `artifact_ttl` and `keep_last_runs`.

**Non-ephemeral (`keep_artifacts = true` or `--keep`):**

- `gsd-test wait` writes the failure-first digest (FAILURES.md, failures.json, per-failure files) and emits the verdict with artifact paths, then exits without releasing.
- The prune pass is skipped entirely for that invocation.

```toml
[storage]
# Opt out of ephemeral mode globally (keep all run artifacts until manual cleanup).
keep_artifacts = true

# — or — keep only the 10 newest runs, each for up to 72 hours.
# keep_artifacts = false
# artifact_ttl = "72h"
# keep_last_runs = 10
```

## CLI flags

CLI flags override the corresponding config values. Flags always take precedence.

| Flag | Overrides | Description |
|------|-----------|-------------|
| `--config <path>` | — | Explicit config file path |
| `--targets <os,...>` | `defaults.targets` | Comma-separated OS list |
| `--bench <name>` | `defaults.pin` | Pin to a specific Bench |
| `--exclude <name,...>` | `defaults.exclude` | Comma-separated Bench names to exclude |
| `--probe-benches` | — | Probe each Bench for reachability at startup |
| `--keep` | — | (on `gsd-test run --async`) Preserve run artifacts; opt this invocation out of ephemeral auto-release |
| `--json-events` | — | Emit events as JSON Lines instead of human-readable TTY output |
| `--verbose` | — | Show the full child-output firehose + per-test pass lines (also `GSD_TEST_VERBOSE=1`) |
| `--quiet` | — | Suppress the progress heartbeat; show only leg events and failures |
| `--base <ref>` | — | Base git ref to merge from (default: `main`) |
| `--head <ref>` | — | PR git ref to merge into base (default: `HEAD`) |
| `--source <path>` | — | Source git repo path (default: `.`) |
| `--scratch <dir>` | — | Scratch directory for worktree construction (default: system temp) |

The output stream is **quiet by default** (a compact per-OS heartbeat + leg
events + loud failures). `--verbose` restores the full firehose; `--quiet` drops
even the heartbeat; `--json-events` always emits the full typed stream regardless.
For the verbosity levels, the verdict line, and the run artifacts these produce,
see the [Failure-first Output Reference](failure-first-output-reference.md).

## Complete annotated example

```toml
# ~/.config/gsd-test/config.toml

# Default CLI flag values. CLI flags override these.
[defaults]
# Run tests on Linux and Windows by default.
targets = ["linux", "windows"]

# Uncomment to always pin to a specific Bench by name.
# pin = "linux-bench-a"

# Uncomment to exclude Benches from selection (e.g., while a machine is down).
# exclude = ["win-rig-2"]


# Declare your Linux Bench.
[[benches]]
name = "linux-bench-a"
host = "linux-bench-a"   # SSH alias in ~/.ssh/config
os   = "linux"

# A second Linux Bench for round-robin distribution.
[[benches]]
name = "linux-bench-b"
host = "linux-bench-b"
os   = "linux"

# Declare your Windows Bench.
[[benches]]
name = "win-rig-1"
host = "win-rig-1"
os   = "windows"


# Map each target OS to its expected Tester Image version.
# gsd-test verifies this against the image's sentinel label on every run.
[versions]
linux   = "v1.0.0"
windows = "v1.0.0"

[testing]
command = ["npm", "test", "--", "--test-reporter={{REPORTER_PATH}}", "--test-reporter-destination={{REPORTER_DEST}}"]
```
