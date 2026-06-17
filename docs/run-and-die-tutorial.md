# Tutorial: Your First Run-and-die Run

This tutorial walks you through submitting a run to a disposable container and watching the watchdog reap a runaway test. By the end you will have run a passing suite and a hanging one through `gsd-test submit`, and seen a structured `reaped` result. For *why* run-and-die works this way, read [Run-and-die Execution](run-and-die.md) afterwards.

## Before you start

You need:

- `gsd-test` installed and a working **Linux Bench**. If you have completed [Getting Started](getting-started.md), you already have both. A local Docker daemon is fine — use a Bench with `host = "local"`.
- Docker reachable on that Bench (the first run pulls or builds the Tester Image, which can take a minute).

You do not need to understand the internals yet. Follow the steps in order; each one builds on the last.

## Step 1: Create a tiny project

Make a directory with one passing test:

```bash
mkdir -p ~/run-and-die-demo
cd ~/run-and-die-demo
cat > ok.test.mjs <<'EOF'
import { test } from 'node:test';
import assert from 'node:assert';

test('arithmetic still works', () => {
  assert.equal(2 + 2, 4);
});
EOF
```

## Step 2: Write a run spec

The run spec is the JSON an agent submits instead of running `node`. Create one that points at your project and targets Linux:

```bash
cat > spec.json <<'EOF'
{
  "repo": "/root/run-and-die-demo",
  "target": "linux",
  "budget": { "estimateMs": 10000 }
}
EOF
```

Set `repo` to the absolute path of the directory you just made (run `pwd` to check).

## Step 3: Validate the spec

Before running anything, submit the spec without `--execute`. This validates it and fills in the defaults — a quick, safe first result:

```bash
gsd-test submit --spec-file spec.json
```

You will see the normalised spec printed back, with a `runId` assigned and defaults applied:

```json
{
  "runId": "f3c1...-...-...",
  "repo": "/root/run-and-die-demo",
  "target": "linux",
  "testCommand": ["node", "--test"],
  "budget": { "estimateMs": 10000, "overrunFactor": 1.5, "hardCapMs": 3600000 },
  "isolation": "process"
}
```

Notice that you did not give a `runId`, a `testCommand`, or an `isolation` mode — the Engine filled them in. No container ran; this step only checked your spec.

## Step 4: Run it for real

Now add `--execute`. The Engine selects your Linux Bench, makes a disposable container, copies the project in, and runs the suite under the watchdog:

```bash
gsd-test submit --execute --spec-file spec.json
```

The first run pulls or builds the Tester Image, so give it a minute. When it finishes you will see a per-OS report:

```json
{
  "schema_version": 2,
  "kind": "pass",
  "outcome": "passed",
  "os": "linux",
  "total": 1,
  "passed": 1,
  "failed": 0
}
```

`"outcome": "passed"` — the suite ran in a container that has already been removed. You have run your first run-and-die run.

## Step 5: Make a test run away

Now add a test that wedges the runner — a synchronous loop that never returns:

```bash
cat > wedge.test.mjs <<'EOF'
import { test } from 'node:test';

test('this test wedges the runner', () => {
  while (true) { /* never returns */ }
});
EOF
```

A loop like this blocks the runner's own event loop, so `node --test`'s built-in timeout cannot fire. This is exactly the runaway the watchdog exists to catch. Tell the run to share one process so the wedge takes the whole runner down — edit `spec.json` to add `"isolation": "none"`:

```bash
cat > spec.json <<'EOF'
{
  "repo": "/root/run-and-die-demo",
  "target": "linux",
  "isolation": "none",
  "budget": { "estimateMs": 10000 }
}
EOF
```

## Step 6: Watch it get reaped

Submit again:

```bash
gsd-test submit --execute --spec-file spec.json
```

This run pauses for about half a minute — that pause is the deadline elapsing while the wedged test refuses to finish. Then the watchdog steps in, and you get a `reaped` report:

```json
{
  "schema_version": 2,
  "outcome": "reaped",
  "os": "linux",
  "kill": {
    "reason": "estimate_overrun",
    "reaped_by": "in_container",
    "last_active_test": { "file": "wedge.test.mjs", "name": "this test wedges the runner" },
    "signal_chain": ["SIGTERM@30000", "SIGKILL@30200"]
  }
}
```

Notice `"outcome": "reaped"` — not a silent hang, but a loud result. And `kill.last_active_test` points straight at `wedge.test.mjs`: the watchdog tells you *which* test ran away. The container, and the wedged process inside it, are already gone.

## What you have done

You submitted a run spec, ran a passing suite in a disposable container, and watched the watchdog reap a runaway test with a structured record of where it died. Try it again — change the estimate, or remove `isolation: "none"` and watch the ordinary per-test timeout catch the wedge instead.

When you are ready to use this for real work — routing an agent's tests, tuning the budget, reading the runaway leaderboard — see the [how-to guides](run-and-die-how-to.md). For the field-by-field details, see the [reference](run-and-die-reference.md).
