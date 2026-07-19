# ADR-0029 — Branch-derived container naming and branch-scoped reaper ownership

**Date**: 2026-07-18
**Status**: Accepted (2026-07-18)
**Context**: Issue #123 — name the run container after the branch under test so a Bench operator can tell at a glance whether a container was spawned by gsd-test-runner, and tighten the Tier-2 reaper's ownership so an invocation only reaps containers belonging to the branch it works on.

## Context

ADR-0021 Decision 2 establishes the Tier-2 reaper's contract: every run container is labeled `sh.gsd-test.run-id` + `sh.gsd-test.deadline`, and the next Engine invocation against a Bench sweeps and kills any labeled container past its deadline ("reap on next contact"). The contract is correct but **coarse**:

- A Bench is typically shared with unrelated Docker work. The run container's only externally-visible identifier is a Docker-assigned random name (`keen_euclid`, `sleepy_kepler`, …). To tell which branch a leftover container was testing — or even to confirm a container is a gsd-test-runner container at all — an operator must `docker inspect <id>` and read the labels, then re-derive the branch from the worktree path. That is slow and error-prone exactly when something else is already broken.
- The reaper claims ownership of *any* container the runner ever labeled, regardless of which branch it was for. An operator running `gsd-test submit` for branch `fix/foo` today will sweep (and kill) a leftover overdue container from yesterday's `fix/bar` run. The operator's stated intent — "the runner should only own containers with the names of the fix it works on" — is not honored.

The run spec (`runspec.Spec`) already carries the branch under test (`PRBranch` and `Base`), so the information needed to fix both gaps is present at the dispatch/Watchdog execution engine; it just never reaches the container's name or a reaper-readable label.

## Decision

### 1. Name run containers after the branch under test

The dispatch/Watchdog execution engine passes `--name gsd-test-<branch-slug>-<short-runId>` on every `docker create` / `docker run`. The slug is derived from `Spec.PRBranch` (preferred) or `Spec.Base` (fallback) by `Spec.BranchSlug()` / `slugifyBranch()` in `internal/runspec/container_name.go`. The slug rules:

- Lowercase the input.
- Replace every run of `[^a-z0-9._-]` with a single `-` (this maps `/` → `-`, the load-bearing transformation for branch names like `fix/2329-opencode-commands-dir`).
- Collapse runs of `-`; trim leading/trailing `-`.
- Fall back to the sentinel `"branch"` when the result would be empty (so the name is always valid Docker syntax).

The full container name is `gsd-test-<slug>-<short-runId>` where `short-runId` is the first 8 characters of `Spec.RunID` (32 bits of UUID v4 entropy — ample for collision avoidance between concurrent runs of the same branch on the same Bench). The total length is capped at 63 bytes (Docker's hostname-style ceiling); on truncation the slug is shortened and the runId tail is preserved so the uniqueness guarantee never weakens. When `RunID` is not yet assigned, the tail falls back to `noid`.

### 2. Mirror the slug on a `sh.gsd-test.branch` label

The same `Spec.BranchSlug()` value is emitted as `--label sh.gsd-test.branch=<slug>`. The label is the **machine-readable** ownership signal the reaper reads; the name is the **human-readable** signal an operator reads in `docker ps`. Both derive from the same function so they cannot drift — the parity invariant is asserted by `TestDockerRunArgs_NameAndBranchLabelConsistent`. The label follows the reverse-DNS convention from ADR-0011.

### 3. Scope the Tier-2 reaper to the current invocation's branch

`reaper.Sweep` gains a `branchSlug string` parameter. When non-empty, the reaper filters containers by `sh.gsd-test.branch` label value **before** the existing deadline filter — so an invocation working `fix/foo` reaps only overdue `fix/foo` containers, leaving `fix/bar` leftovers for their own invocations. When empty, Sweep retains the pre-ADR-0029 behavior (reap every labeled+overdue container) — that is the operator escape hatch, preserved for a future `gsd-test sweep` command or manual cleanup use.

The branch filter matches on the **label value** (`Container.BranchSlug`), not on parsing the human-readable name. This stays correct when Docker substitutes a random name (pre-ADR-0029 containers, or any future runner that sets the label but not the name).

The single production call site (`cmd/gsd-test/main.go`, `dispatchRun`) passes `spec.BranchSlug()`.

### 4. Pipeline engine naming is explicitly deferred

The Pipeline engine (ADR-0027) does **not** label its containers today — its `StartContainer` leg runs `docker run --rm -d --workdir /work … <image> sleep infinity` with no labels, so the reaper's `--filter label=sh.gsd-test.run-id` ignores Pipeline containers entirely. Pipeline containers are torn down by `RunAll`'s deferred `docker rm -f`, not by the reaper.

Pipeline's struct (`internal/pipeline.Pipeline`) also doesn't carry the branch — it only holds the worktree path. Threading branch info to Pipeline means changing `pipeline.New`'s signature and the `internal/runner` caller that prepares worktrees. That is a larger refactor than this issue called for; it is tracked as a follow-up. When it lands, the same `Spec.BranchSlug()` helper applies without modification.

## Consequences

- `docker ps` on a Bench now shows `gsd-test-fix-foo-550e8400` instead of `keen_euclid`. One glance answers both "is this mine?" (the `gsd-test-` prefix) and "which branch is it?" (the slug). `docker logs` / `docker exec` debugging during a live run no longer requires looking up the container ID first.
- The Tier-2 reaper becomes branch-scoped on the default `submit --execute` path. On the day this lands, pre-ADR-0029 leftover containers (no `sh.gsd-test.branch` label, no `gsd-test-…` name) will **not** be auto-cleaned by scoped Sweeps — they sit until the operator invokes `Sweep(_, _, _, "")` (the escape hatch) or removes them manually. This is the explicit feature, not a side-effect: the operator asked for the runner to "only own containers of the fix it works on."
- Two new files (`internal/runspec/container_name.go`, `internal/reaper/sweep_branch_test.go`) and one new struct field set (`reaper.Container.Name` + `reaper.Container.BranchSlug`) — purely additive. The `Sweep` signature change from 3-arg to 4-arg touches five test call sites and one production call site, all in this PR.
- `Slugify` accepts arbitrary branch strings (including hostile input) and produces a value constrained to `[a-z0-9._-]`, which is safe to embed in a Docker `--name` and a `--label` value. There is no command-injection surface: the runner abstraction passes args as a Go slice to `exec`, never through a shell.

## Alternatives considered

- **Add only the label, skip the human-readable name.** Rejected: the operator's stated need is to read the branch off `docker ps` at a glance. Labels do not appear in `docker ps`'s default format, so the operator would still need `docker inspect` — exactly the friction this ADR exists to remove.
- **Derive the slug from parsing the worktree path instead of `Spec.PRBranch`.** Rejected: the worktree path is an internal detail that may change shape; the spec field is the contract. Parsing paths also re-introduces the path-separator normalization class the codebase already polices.
- **Make `Sweep` variadic with options instead of positional.** Rejected: one optional parameter does not justify an options type. The positional empty-string default is clear at the call site and idiomatic for Go ("empty = unscoped").
- **Filter by parsing the `--name` instead of by label.** Rejected: name-parsing is more fragile (depends on the prefix scheme never changing) and breaks for any future runner that sets the label but not the name. The label is the durable ownership signal; the name is for humans.
- **Name Pipeline containers in this PR too.** Rejected as scope creep: Pipeline doesn't currently label its containers, doesn't carry the branch, and isn't reaped by the Tier-2 reaper at all. The signature change to `pipeline.New` and the `internal/runner` caller is a separate refactor. Documented here so the follow-up has a clear shape: reuse `Spec.BranchSlug()` once Pipeline carries the branch.
