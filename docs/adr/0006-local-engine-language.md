# 0006 — Local Engine is written in Go, distributed as static per-OS binaries

Status: Accepted (2026-05-23)

## Context

The Local Engine (see CONTEXT.md Target vocabulary) is the developer-side launcher. ADR-0004 mandates "fail loud at every pipeline leg with structured diagnostics" — 11 distinct legs, each with its own exit code, diagnostics path, and structured error envelope. The Dev Workstation must be platform-agnostic across macOS, Linux, and Windows. The project targets open contributors with mixed backgrounds; contributor friction at install time directly suppresses contributions.

The transitional implementation uses Bash + Python + Node ESM. Bash's error-handling weakness makes the ADR-0004 contract a constant fight against the language. Python's cross-platform story is reliable only when contributors already understand Python version management (py launcher vs `python` on Windows, venv vs system, PATH quirks). Node would require contributors to manage `node_modules` for a tool that has nothing to do with the project's own Node dependency tree.

## Decision

The Local Engine is written in Go and distributed as single static binaries per Dev Workstation OS: `gsd-test-darwin-arm64`, `gsd-test-darwin-amd64`, `gsd-test-linux-amd64`, `gsd-test-linux-arm64`, `gsd-test-windows-amd64.exe`. CI cross-compiles all targets from one build job and publishes them to GitHub Releases. Contributors download the binary for their OS, drop it in their PATH, and have a working Local Engine. No runtime to install, no version manager required, no dependency tree.

## Consequences

+ The ADR-0004 "fail loud per leg" contract becomes idiomatic — typed `error` returns, `log/slog` for structured logging, `encoding/json` stdlib, no third-party dependencies for the core pipeline.
+ Cross-platform Dev Workstation support comes free — Go's native cross-compile produces all targets from one CI job.
+ Contributor friction at install is minimized — single binary download, no runtime install.
+ The Local Engine becomes statically testable in Go's test toolchain (`go test ./...`).
- The repo gains a Go module and a `cmd/gsd-test/` (or similar) directory. The transitional Bash + Python + JS code stays until the Go implementation reaches parity.
- A GitHub Release publishing pipeline is required. CI complexity increases.
- Contributors who want to modify the Local Engine need Go installed (not just to run it). Acceptable: most contributors will only run it.

## Alternatives considered

- Python — Rejected: cross-platform install friction on Windows, Python version management is contributor friction, stdlib subprocess+json+pathlib would work but distribution is harder than a static binary.
- Bash — Rejected: fundamentally incompatible with the ADR-0004 structured-diagnostics contract; Windows support requires Git Bash and remains brittle.
- Rust — Rejected: the typing rigor is real but exceeds what this orchestrator needs; Go's stdlib and tooling are better matched to "subprocess + JSON + SSH + Docker."
- Node/TypeScript — Rejected: asking contributors to manage `node_modules` for a tool that orchestrates Node-based projects creates needless coupling and install friction.
- Keep transitional Bash + Python + JS — Rejected: this is the architecture ADR-0004 explicitly rejects.
