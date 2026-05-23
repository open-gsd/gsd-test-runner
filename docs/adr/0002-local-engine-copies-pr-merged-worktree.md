# 0002 — Local Engine copies a PR-merged worktree into a fresh container per run

Status: Accepted (2026-05-22)

## Context

The transitional Runners rsync the dirty working tree to a shared per-host Mirror directory and bind-mount it into the container. Two failure classes followed. First, parallel branch runs corrupt the shared Mirror: docker-test-runner-issues.md §4.1 documents holodeck pass / plex2 97 fail / redshirt 18 fail from concurrent `rsync --delete` operations racing against each other. Second, the container can write back into the developer's working tree — npm artifacts and node_modules churn leak sandbox side effects to the host.

The dirty-tree input also makes "CI-equivalent locally" a lie. CI sees the merged PR result; the local runner sees whatever the developer's working state happens to be at invocation time.

A real `git merge` in a scratch clone surfaces the shape of work that CI will process, and it surfaces merge conflicts early — before any container starts — as a specific, named failure rather than a subtle downstream test failure.

## Decision

The Local Engine constructs the PR-merged worktree in a scratch clone: shallow-clone the base, fetch the PR branch, real `git merge` (not `merge-tree`). Merge conflicts surface here as a named failure before any container starts. The resulting worktree is copied into a fresh container per run — no bind-mounts, no shared Mirror. The container is `--rm` on exit; nothing persists to the host or across runs.

## Consequences

+ Mirror corruption class (§4.1) eliminated — there is no shared mutable state across runs.
+ Sandbox leakage eliminated — container writes can't reach the developer's tree.
+ Pre-push gate sees what CI sees: the merged PR result.
+ Merge conflicts fail loud and early, with a specific failure mode.
- Copy-in adds seconds of wall time per run vs bind-mount.
- A scratch clone must be created or reused per run; cleanup discipline required.
- Tests cannot mutate state and expect it to persist between Local Engine runs.

## Alternatives considered

- Bind-mount the worktree (transitional approach) — Rejected: see §4.1 and the "Symptoms observed" section of gsd-test-summary-docker.md.
- `git merge-tree --write-tree` without a real merge — Rejected: writes a tree object with no commit; harder for the developer to inspect failures, less familiar primitive.
- Continue using the shared per-host Mirror with per-branch subdirectories — Rejected: this is a workaround for the symptom, not a fix for the architecture. The deeper issue is shared mutable state.
