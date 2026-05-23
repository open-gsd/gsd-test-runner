# 0010 — Ref resolution lives in internal/refs; worktree takes SHAs only

Status: Accepted (2026-05-23)

## Context

ADR-0002 specifies that the Local Engine constructs the PR-merged worktree via a scratch clone and a real git merge. The shape of the inputs to that construction was implied but not specified.

Implementing `internal/worktree/` surfaced a concrete contract question. Two facts collided:

1. The scratch clone uses `git clone --local <source-repo> <scratch-dir>` (chosen for speed and offline use — see the implementation commit). The `--local` form treats the source as a remote and brings its branches in as `refs/remotes/origin/<name>`, not as local branches.
2. The original `worktree.Construct` design called `git merge <PRRef>` directly with the user-supplied ref. With `--local`, `git merge feat-branch` fails because no local ref `feat-branch` exists in the scratch — only `origin/feat-branch`.

The first implementation worked around this with `git fetch origin <PRRef>` followed by `git merge FETCH_HEAD`. Universal but ugly: it fused symbolic-ref resolution with merge execution inside the worktree module, hardcoded the `origin` remote, and produced unintuitive error messages ("merge failed: FETCH_HEAD does not resolve") when the real problem was a typo in the ref name.

The cleaner decomposition: resolve user-supplied refs (`feat/foo`, `HEAD`, a SHA prefix, a tag) to full commit SHAs **before** worktree construction. The worktree module then takes two SHAs and merges them — no fetch, no remote knowledge, no symbolic-name handling.

## Decision

**A new package `internal/refs/` owns ref resolution.** It exposes one function:

```go
func Resolve(ctx context.Context, repoPath, userRef string) (string, error)
```

Implementation: shells `git -C <repoPath> rev-parse <userRef>^{commit}`. The `^{commit}` suffix dereferences annotated tags to their underlying commit, which is what every downstream consumer expects.

Typed errors: `*UnknownRefError` (the ref does not exist in `repoPath`), `*InvalidRepoError` (`repoPath` is not a git repo). Both implement `error`.

**The worktree module takes SHAs.** `Options.BaseRef` and `Options.PRRef` are renamed `BaseSHA` and `PRSHA`. The fetch step is removed. `Construct` checks out the base SHA in the scratch and merges the PR SHA directly. The `--local` clone has every object the source repo has, so no fetch is ever needed.

**The Local Engine resolves before calling Construct.** The top-level `main` (per ADR-0009) is responsible for resolving the user's `--base` and `--pr` flags via `refs.Resolve` before invoking `worktree.Construct`. Resolution failures abort the run at config-load time with a clear `unknown ref` message, before any scratch directory is created.

## Consequences

+ The worktree module's surface shrinks: no fetch logic, no `origin` remote knowledge, no FETCH_HEAD handling. Tests no longer need to set up a remote — just rev-parse local commits.
+ Symbol-resolution failures (typo'd ref names) surface at config load with a clear "unknown ref" error, not midway through construction with a misleading "merge failed."
+ The SHA captured at start-of-run is the precise commit tested. Concurrent pushes to the PR branch during a long run do not affect this run's result; the next run will resolve the new tip.
+ Two modules instead of one for ref handling: `internal/refs` (resolution) and `internal/worktree` (merge). Each is independently testable. Locality of "what does this ref mean" is concentrated in one ~30-line module.
+ Future GitHub-special-ref support (`pull/123/head`) becomes additive in `internal/refs` (e.g., auto-fetch from `origin` when local rev-parse fails) without touching the worktree module.
- Two modules instead of one is more code overall than the fused approach — paid for by clearer error messages and smaller per-module test surface.
- A `pull/123/head` style ref that the developer hasn't fetched locally fails at resolution time. Acceptable: developers running this just made the PR locally, so the SHA is already in their refs. If auto-fetch is needed later, it lives in `internal/refs`.

## Alternatives considered

- **Keep universal-fetchable PRRef in the worktree module (Option A in the grilling)** — Rejected: fuses symbol resolution with merge execution, hardcodes the `origin` remote, produces unintuitive error messages, and inflates the worktree module's test surface.
- **Resolve symbolic refs in the scratch clone after the `--local` step** — Rejected: solves nothing the SHA-at-boundary approach doesn't solve, while keeping resolution logic fused with merge logic. Dissolved into the "keep current" option once examined.
- **Put resolution in `internal/config/refs.go` alongside config loading (B1 in the grilling)** — Rejected: fewer modules but worse single-responsibility. A future auto-fetch enhancement would force config to know about git plumbing.
- **Use a `type SHA string` newtype for the resolved values** — Rejected: Go convention treats SHA strings as bare strings; the newtype adds clutter without meaningful safety here.
- **Pre-validate SHA existence in worktree.Construct via `git cat-file -e`** — Rejected: a bogus SHA already surfaces as `CheckoutError` or `MergeError` at the relevant Stage. Double validation adds code without changing the failure-mode surface.
