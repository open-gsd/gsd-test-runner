# Implementation worktree: Local Engine (Go) — skeleton

This worktree exists to bootstrap the Go implementation of the Local
Engine described in ADRs 0001–0009. Docs branch: `claude/blissful-benz-f65ef0`.

## What lives here

- All architecture docs (CONTEXT.md, docs/adr/0001–0009) are present
  because this branch was forked from the docs branch HEAD. Read them
  before writing code.
- Implementation work goes in `cmd/gsd-test/` (entrypoint) and
  `internal/` (pipeline, worktree, images, bench, renderer).

## What does NOT belong here

- Further architecture ADRs (write those on the docs branch and rebase
  this branch onto main once the docs PR lands).
- Changes to the transitional `INSTALL_GSD_TEST.sh` — that code is
  untouched until the Go implementation reaches parity.

## Next concrete steps

1. `go mod init github.com/<org>/gsd-test-runner` (org TBD per the
   docs-PR routing resolution)
2. Stub `cmd/gsd-test/main.go` with the orchestrator shape from
   ADR-0009 (illustrative interface)
3. Stub `internal/pipeline/pipeline.go` with the Step-chain shape from
   ADR-0008 (illustrative interface)
4. First real leg implementation: `internal/worktree/` (the upstream
   module ADRs 0002 and the remaining open candidate #3 describe;
   smallest leaf-node module, easiest to land first)

## Bootstrap commit

The Go module is initialized at `github.com/open-gsd/gsd-test-runner`.
If the upstream-routing decision moves the canonical home to a
different org, run:

    go mod edit -module github.com/<new-org>/gsd-test-runner

and update any internal import paths (none yet — packages are skeleton-only).

## Package layout

| Package | ADR | Status |
|---|---|---|
| `cmd/gsd-test`         | 0009 | minimal main, exits 3 |
| `internal/bench`       | 0007, 0008 | doc.go + Bench struct (minimal) |
| `internal/config`      | 0007       | doc.go only |
| `internal/images`      | 0001, 0005 | doc.go + ImageID newtype (minimal) |
| `internal/pipeline`    | 0008       | skeleton: LegError, Event, 8 stubbed step methods, RunAll |
| `internal/plan`        | 0009       | doc.go only |
| `internal/renderer`    | 0009       | doc.go only |
| `internal/worktree`    | 0002       | Construct(): scratch clone + merge |
| `internal/refs`        | 0010       | Resolve(): user ref → commit SHA |

`go build ./...` succeeds; `go test ./internal/refs/...` and `go test ./internal/worktree/...` pass.
