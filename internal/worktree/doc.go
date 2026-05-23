// Package worktree owns the PR-merged worktree: legs 2 and 3 of the
// pipeline (base-branch fetch, real git merge in a scratch clone).
// Runs ONCE per Local Engine invocation; its output (a Worktree value
// with Close()) is shared by all Per-OS Pipelines.
//
// See docs/adr/0002-local-engine-copies-pr-merged-worktree.md. The
// Worktree handle (Close-on-defer ownership) is established by
// docs/adr/0009-local-engine-top-level-orchestration.md.
//
// Per ADR-0010, the worktree module takes full commit SHAs (resolved via
// internal/refs), not symbolic refs.
package worktree
