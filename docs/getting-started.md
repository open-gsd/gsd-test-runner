# Getting Started

This guide walks you through your first `gsd-test` run: from a working Node project to a cross-platform pass/fail result.

## Prerequisites

- `gsd-test` installed. See [Installation](installation.md).
- At least one Bench configured. See [Setting up Benches](benches.md). A Bench is a remote machine you SSH to that has Docker installed and a pulled Tester Image.
- A Node project with tests that run locally via `node --test` or `npm test`.

## Step 1: Confirm your tests run locally

`gsd-test` runs the same `node --test` invocation inside the Tester Image on your Bench. If your tests don't pass locally, they won't pass on the Bench either.

```bash
cd ~/my-node-project
npm ci
npm run build   # skip if your project has no build step
node --test
```

Expected output (abbreviated):

```text
✔ parses config (3ms)
✔ returns 404 for unknown routes (1ms)
✗ fails on missing HOME directory
  Error: ENOENT: no such file or directory, ...
ℹ tests 3
ℹ pass 2
ℹ fail 1
```

Fix any failures before continuing.

## Step 2: Create your config.toml

`gsd-test` reads `~/.config/gsd-test/config.toml`. Create the directory and a minimal config for one Linux Bench:

```bash
mkdir -p ~/.config/gsd-test
```

```toml
# ~/.config/gsd-test/config.toml

[defaults]
targets = ["linux"]

[[benches]]
name   = "lab-rig-1"
host   = "lab-rig-1.local"   # SSH host alias from ~/.ssh/config
os     = "linux"

[versions]
linux = "v1.6.2"   # Tester Image version to expect on the Bench
```

Replace `lab-rig-1.local` with your Bench's SSH alias. Replace `v1.6.2` with the current release version.

The `[versions]` table tells `gsd-test` which Tester Image version to expect on each Bench. If the Bench has a different version, `gsd-test` fails loud before running any tests — this prevents silent drift (see [Troubleshooting: image-version mismatch](troubleshooting.md#image-version-mismatch)).

## Step 3: Run gsd-test

From inside your project's git repository:

```bash
cd ~/my-node-project
gsd-test
```

`gsd-test` runs five sequential phases before you see test results:

1. **Load** — reads `config.toml`.
2. **Plan** — resolves target OSes to Benches.
3. **EnsureImages** — confirms the Tester Image is present on each Bench (pulls from GHCR if absent).
4. **RunPipelines** — runs the 8-leg pipeline on each Bench in parallel.
5. **Aggregate+Render** — prints the final per-OS summary and a one-line verdict.

## Step 4: Read the output

`gsd-test` is **quiet by default**: it prints the pipeline legs, a periodic
heartbeat, and any failures — but not every passing test. A successful run with
one Linux Bench looks like this:

```text
[linux] check_image_version ✔
[linux] start_container ✔
[linux] copy_worktree ✔
[linux] npm_ci ✔
[linux] build ✔
[linux] run_tests ✔
[linux] drain ✔
[linux] parse ✔

── Results ──────────────────────────────────────────
linux    PASS  3/3
{"type":"verdict","outcome":"passed","per_os":{"linux":{"passed":3,"failed":0,"total":3,"outcome":"passed"}},"unique_failures":0,"total_failures":0,"top":[],"artifacts":{"dir":"~/.local/state/gsd-test/runs/9f2c1a","failures_json":"…","failures_md":"…","junit_xml":"…","events_jsonl":"…"}}
```

Each line begins with `[linux]` — the target OS. Legs print `✔` on success. The
**last line is always a `verdict`** — one compact JSON object whose `outcome`
matches the exit code and whose `artifacts.dir` points at this run's saved
output. On a larger suite you would also see a heartbeat, `[linux]   … 25 passed`,
once every 25 passing tests.

A failed run surfaces the failure loudly, the instant it happens:

```text
[linux] check_image_version ✔
[linux] start_container ✔
[linux] copy_worktree ✔
[linux] npm_ci ✔
[linux] build ✔
[linux]   ✗ FAIL routes.test.js:42 · assertion · returns 404 for unknown routes — AssertionError: 404 !== 200
[linux] run_tests ✔
[linux] drain ✔
[linux] parse ✔

── Results ──────────────────────────────────────────
linux    FAIL  2/3
  ✗ returns 404 for unknown routes
      AssertionError: 404 !== 200
{"type":"verdict","outcome":"failed","per_os":{"linux":{"passed":2,"failed":1,"total":3,"outcome":"failed"}},"unique_failures":1,"total_failures":1,"top":[{"class":"assertion","file":"routes.test.js","line":42,"name":"returns 404 for unknown routes"}],"artifacts":{"dir":"~/.local/state/gsd-test/runs/9f2c1a","failures_json":"…","failures_md":"…","junit_xml":"…","events_jsonl":"…"}}
```

You do not need to scroll back to find the failure: it appears in real time as
`✗ FAIL <file>:<line> · <class> · <name> — <msg>`, and the same run also writes a
`FAILURES.md` (plus `failures.json` and `junit.xml`) to the `artifacts.dir`
directory shown in the verdict — open that to read the full stack and captured
output. See [Failure-first Output](failure-first-output.md) for the model and the
[output how-to guides](failure-first-output-how-to.md) for reading a failed run.

A leg failure (infrastructure problem, not test failure) looks like this:

```text
[linux] check_image_version ✗
  image ghcr.io/open-gsd/gsd-tester-linux:v1.0.0 on bench lab-rig-1:
  expected version "v1.0.0", got "v0.9.0"
  (diagnostics: /tmp/gsd-test-123/check_image_version.log)

── Results ──────────────────────────────────────────
linux    INCONCLUSIVE (leg check_image_version failed)
{"type":"verdict","outcome":"infra_error","per_os":{},"unique_failures":0,"total_failures":0,"top":[],"artifacts":{}}
```

The verdict's `outcome` is `infra_error` here — the suite did not run as designed.
To turn the full firehose back on (every passing test and all `npm ci` / build
output), add `--verbose`.

## Step 5: Interpret exit codes

| Exit code | Meaning |
|-----------|---------|
| `0` | All target OSes passed. Safe to push. |
| `1` | At least one OS had failing tests. Fix the failures before pushing. |
| `2` | Infrastructure problem — at least one pipeline did not run as planned. Check the specific leg that failed and see [Troubleshooting](troubleshooting.md). |

## Next: Add a second OS

Add a Windows Bench to your config and a Windows entry in `[versions]`:

```toml
[defaults]
targets = ["linux", "windows"]

[[benches]]
name   = "lab-rig-1"
host   = "lab-rig-1.local"
os     = "linux"

[[benches]]
name   = "win-rig-1"
host   = "win-rig-1.local"
os     = "windows"

[versions]
linux   = "v1.6.2"
windows = "v1.6.2"
```

Both pipelines run in parallel. The final summary prints one line per OS. See [Setting up Benches](benches.md) for Windows-specific Bench setup.
