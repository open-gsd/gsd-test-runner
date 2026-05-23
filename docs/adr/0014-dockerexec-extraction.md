# 0014 — internal/dockerexec extraction, Bench.DockerHost, unified cancellation

Status: Accepted (2026-05-23)

## Context

NOTES.md flags deferred duplication: `dockerExecError` + `realDockerInspect` + `DOCKER_HOST=ssh://` transport logic are byte-for-byte duplicated between `internal/pipeline:80–163` and `internal/images:155–217`. `EnsurePresent` landing as leg #2 of three-leg trigger satisfies the Rule-of-Three threshold for extraction. Implementing that extraction forces five concrete decisions the architecture ADRs did not pre-commit:

1. **Where does the "ssh:// vs local" transport rule live?** Inside `dockerexec`, inside each caller, or on `bench.Bench`?
2. **What's the interface shape of the extracted package?** Per-operation API (Inspect, Pull, Build, ...), single `Run`, or a fluent builder?
3. **How do callers stub `dockerexec` without violating ADR-0011 dec 4's function-variable-no-interface rule?**
4. **How does the context-cancellation dual-path** (pre-cancel vs mid-docker-cancel) **get unified?**
5. **How does `dockerexec` itself get test coverage?**

## Decision

**1. `func (b Bench) DockerHost() string` lives on `bench.Bench`.** Returns `""` for a local or empty `Host`; returns `"ssh://" + b.Host` otherwise. A new `bench.Local = "local"` constant names the sentinel. The bench-to-transport rule lives where `Bench` is defined — reusable if any non-docker tool ever needs the same SSH-or-local routing. This replaces two identical inline `if b.Host != "" && b.Host != "local"` blocks (pipeline.go:271, images.go:50).

**2. `internal/dockerexec` exports a single `Run(ctx, bench.Bench, args []string) (string, error)`.** Returns captured stdout on success; on non-zero exit returns `*ExecError{Args, Stdout, Stderr, ExitCode}`. Callers compose docker subcommands themselves (`args = []string{"image", "inspect", image, "--format", format}`). A smaller surface than a per-operation API (Inspect/Pull/Build/...) which would require touching `dockerexec` for every new docker subcommand future legs need.

**3. Per-package wrapper var stub pattern.** Each caller (`internal/pipeline`, `internal/images`) keeps its own thin wrapper variable:

```go
var dockerInspect = func(ctx context.Context, b bench.Bench, image, format string) (string, error) {
    return dockerexec.Run(ctx, b, []string{"image", "inspect", image, "--format", format})
}
```

Tests in `pipeline_test.go` / `images_test.go` swap the wrapper var (matching the existing pattern from ADR-0011 dec 4). Production code — `exec.Cmd` construction, `DOCKER_HOST` env injection via `Bench.DockerHost()`, and error wrapping — is fully consolidated inside `dockerexec`; only the stub-target var stays per-package. This eliminates cross-package test parallelism races on a shared global.

**4. Cancellation unified inside `dockerexec.Run`.** After `cmd.Run()` returns, `dockerexec.Run` checks `ctx.Err() != nil` and returns `ctx.Err()` directly — not wrapped in `ExecError` or `fmt.Errorf`. This collapses two cancellation paths into one. Both pre-cancel and mid-docker-cancel produce `ctx.Err()` at the caller; `runLeg` wraps it in `LegError{Cause: context.Canceled}` consistently.

**5. `dockerexec` gets transitive test coverage through pipeline and images tests.** No direct `dockerexec_test.go` file. The logic surface is approximately 30 lines (build env, run cmd, classify error, ctx check). Pipeline and images tests already exercise the full path by swapping their wrapper vars. This matches the codebase's existing stub-at-callsite testing pattern and avoids a three-layer stub stack.

The package signature:

```go
package dockerexec

type ExecError struct {
    Args     []string
    Stdout   string
    Stderr   string
    ExitCode int
}

func (e *ExecError) Error() string { /* ... */ }

func Run(ctx context.Context, b bench.Bench, args []string) (string, error)
```

## Consequences

+ `dockerExecError` and `realDockerInspect` deleted from both packages (~80 LOC duplication removed).
+ New pipeline legs (Drain via `docker cp`, StartContainer via `docker run`, etc.) land cleanly against an existing single pattern.
+ `Bench.DockerHost()` is trivially unit-testable in the `bench` package (IPv6 hosts, SSH ports, the `local` sentinel) without any subprocess.
+ Cancellation behavior is consistent across all docker callers; `runLeg` does not need per-caller cancellation branches.
+ `pipeline.dockerInspect` and `images.dockerInspect` become two-line forwarding functions, easy to read at the call site.
- `dockerexec` has no direct tests — regressions in env construction or error classification surface only through caller tests, which may not cover every code path in `dockerexec` directly.
- Per-package wrapper vars don't fully consolidate the stub surface; true cross-package consolidation was rejected for test-parallelism safety (see below).
- `ExecError` is a structurally weaker type than the typed Cause errors callers wrap it in (`PullAuthError`, `ImageNotPresentError`, etc.) — callers must still classify `ExecError.Stderr` to produce a typed Cause.

## Alternatives considered

- **Per-operation Inspect/Pull/Build/Cp API** — Rejected: larger surface; requires touching `dockerexec` for every new docker subcommand future legs need. Compounds the maintenance cost rather than eliminating it.
- **Builder/fluent API** — Rejected: out of step with the codebase's plain-function style established across ADR-0008 through ADR-0012.
- **Private `DockerHost` helper inside `dockerexec`** — Rejected: the bench-to-transport rule belongs with `Bench`, not with the docker-invocation layer. Placing it in `dockerexec` makes it unreachable to non-docker tools that may need the same routing.
- **Exported `dockerexec.Run` as a package-level variable for cross-package test swap** — Rejected: cross-package test parallelism races on a global mutable. Two parallel test packages swapping the same global simultaneously would require external synchronization.
- **Test-only injection via `testkit` subpackage** — Rejected: extra subpackage for marginal encapsulation benefit; still a global underneath. Adds a public API to hide an implementation detail.
- **ADR-0011 reinterpreted as allowing a Go interface here** — Rejected: function-variable swap is the locked pattern per ADR-0011 dec 4. One new package does not change the analysis.
- **Pure/impure classifier split for direct testing** — Rejected: over-decomposed for ~30 lines. The pure classifier (Stderr → error class) is a handful of `strings.Contains` calls, not a standalone unit.
- **Integration tests against real docker** — Rejected: adds a CI docker requirement and runs slow. Keeping unit tests stub-based preserves the fast feedback loop ADR-0011 dec 4 was designed for.
- **Inline `exec.CommandContext` stub** — Rejected: three-layer stub stack (caller test → wrapper var → exec stub) is brittle and harder to read than the current two-layer pattern.
