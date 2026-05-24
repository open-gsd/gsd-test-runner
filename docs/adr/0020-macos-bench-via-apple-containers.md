# ADR-0020 — macOS Bench via Apple Containers

**Date**: 2026-05-23
**Status**: Accepted
**Context**: Issue #8 — Create macOS containers to run on macOS hardware and test macOS-based products.

---

## Context

Issue #8 asks for macOS as a Bench target. CONTEXT.md lists "macOS Containers (planned) — Apple's native container runtime, distinct from Docker Desktop. Planned as a fourth Runner target."

Apple Containers shipped in macOS 26 as a native sandbox runtime. The CLI is `container` (not `docker`). It runs OCI images, and unlike Docker on Mac — which runs Linux containers inside a Linux VM — Apple Containers can run macOS-native containers. This is the only way to test actual macOS code paths in a sandboxed environment on macOS hardware.

The existing Bench abstraction (ADR-0011, ADR-0014) assumes Docker as the container runtime, invoking `docker` directly in `internal/dockerexec`. macOS support requires the Local Engine to invoke `container` instead. The sentinel mechanism (OCI label `sh.gsd-test.image-version`, ADR-0011) is runtime-agnostic — both Docker and Apple Containers consume OCI images — so the sentinel continues to work without change.

Separately, GitHub Actions hosted runners with macOS 26 + Apple Containers preinstalled are not yet generally available (latest is `macos-15` as of this ADR). This ADR establishes the design and file structure so that issue #8 can close; the actual macOS Tester Image build is gated on runner availability.

---

## Decisions

### Decision 1 — macOS runtime: Apple Containers (not Docker, not Podman)

Apple Containers is the selected runtime for macOS Benches. It ships natively in macOS 26+; the CLI binary is `container`; it builds and runs OCI images natively; and — critically — it can run macOS-native containers, giving genuine macOS sandbox parity.

**Rejected alternatives:**

- **Docker Desktop (macOS)**: runs Linux containers inside a Linux VM. Cannot sandbox macOS-native code paths. Tests running inside Docker Desktop on Mac would observe Linux, not macOS, kernel behavior. Wrong tool.
- **Podman (macOS)**: same limitation — Linux containers in a VM (via `gvisor`, `lima`, or `podman machine`). Also wrong tool.
- **Lima / colima**: these are VM managers layered on top of qemu or Apple Virtualization. Still Linux VMs; same limitation as Docker Desktop.

Apple Containers is the only available runtime on macOS 26+ that can run macOS containers natively.

### Decision 2 — Runtime abstraction: extend `dockerexec` with a runtime selector

The existing `internal/dockerexec` package (`Run`, `Stream`) hardcodes `"docker"` as the subprocess binary. To support Apple Containers, this binary must be selectable per Bench.

**Decision**: extend, not replace. Add a `Runtime string` field to `bench.Bench` with two sentinel values — `RuntimeDocker = "docker"` (default, preserves all existing behavior) and `RuntimeContainer = "container"` (Apple Containers, macOS Benches). Add a `RuntimeBin() string` method to `Bench` that returns the right binary name. Update `dockerexec.Run` and `dockerexec.Stream` to invoke `b.RuntimeBin()` instead of the hardcoded `"docker"` string.

**Rejected alternative — separate `containerexec` package**: would duplicate approximately 80% of `dockerexec`'s logic (pipe setup, scanner goroutines, context-cancellation unification, ExecError shape). The API surface the rest of the codebase sees (`Run`, `Stream`, `ExecError`) is unchanged under the extension approach. Cost of extension: two one-line changes in `dockerexec` and three additions to `bench.Bench`. Cost of a separate package: ~120 lines of copied code, import churn across all call sites, risk of the two packages diverging over time.

**Consequence for `bench.BenchDockerError`**: the error message currently says "docker X failed". This remains "docker" for backward compatibility — callers logging this error see the same format regardless of the underlying binary. If this becomes confusing in practice, a follow-up ADR can rename the field and update the message. Not addressed here.

**Consequence for `DOCKER_HOST`**: `DockerHost()` returns `"ssh://<host>"` regardless of runtime. Apple Containers supports the same SSH transport, so the value is reused unchanged.

### Decision 3 — Tester Image format: OCI Containerfile at `dockerfiles/macos.containerfile`

Apple Containers accepts standard Dockerfile syntax (it's an OCI image builder). The macOS Tester Image uses the same Dockerfile syntax as the Linux and Windows Tester Images.

The file is placed at `dockerfiles/macos.containerfile`. The `.containerfile` extension is chosen (over `.Dockerfile`) to signal at the file-layout level that this file targets a non-Docker runtime. The content is semantically identical to `dockerfiles/linux.Dockerfile` in structure — same ARG/LABEL pattern, same sentinel OCI label, same Reporter COPY — but the base image and system package installation differ because macOS base images use different package managers and paths.

**Placeholder status**: as of this ADR, Apple does not publish official macOS base images for Apple Containers to a public registry. The `FROM` line is replaced with `FROM scratch` as a syntactically valid placeholder. The file documents the intended structure. The actual base image will be filled in when Apple makes one available.

**Image naming and distribution**: tagged and pushed to GHCR at `ghcr.io/<owner>/gsd-tester-macos:<tag>`, consistent with the Linux (`gsd-tester-linux`) and Windows (`gsd-tester-windows`) pattern. The `cmd/gsd-test/main.go` ImageID derivation (`ghcr.io/open-gsd/gsd-tester-<os>`) already works for `os=macos` without change.

### Decision 4 — GH Actions runner: placeholder job with `if: false` gate

A `publish-macos` job is added to `.github/workflows/publish-tester-images.yml`. The job is unconditionally disabled via `if: false` in its condition. The `runs-on` field is set to `macos-26` (not yet available from GitHub Actions as of this ADR's date).

When GitHub Actions provides a macOS 26 runner with Apple Containers preinstalled:

1. Set `runs-on` to the correct runner label.
2. Set `if: true` (or remove the condition entirely).
3. Replace the placeholder `FROM scratch` in `dockerfiles/macos.containerfile` with the actual Apple-published macOS base image.

No other code changes are needed — the `container` binary invocations in the job match Apple Containers' CLI exactly.

---

## Consequences

- `bench.Bench` gains a `Runtime` field and two constants. Existing bench values constructed without the field default to `""`, which `RuntimeBin()` maps to `"docker"` — fully backward-compatible.
- `internal/dockerexec.Run` and `internal/dockerexec.Stream` each change one line (hardcoded `"docker"` → `b.RuntimeBin()`). All existing tests continue to pass; the runtime abstraction is exercised by new unit tests in `internal/bench/bench_test.go`.
- `internal/config` gains `runtime` as an optional TOML field on `[[benches]]` entries. Omitting it (all existing configs) continues to work.
- `dockerfiles/macos.containerfile` is added but will not build until the placeholder `FROM scratch` is replaced and a macOS-capable runner is available.
- The `publish-macos` workflow job is inert (`if: false`) until the runner and base image are available.
- Issue #8 closes on this PR: the design is locked, the file structure is established, the runtime abstraction is wired, and the only remaining work is operational (wait for GH Actions runner availability + Apple base image).

---

## Alternatives considered

See Decision 1 (Docker Desktop, Podman, Lima/colima rejected) and Decision 2 (separate `containerexec` package rejected).

One additional alternative considered for the workflow gate: omitting the `publish-macos` job entirely and adding it later. Rejected because having the job present — even disabled — makes the runner-availability gate visible in the repo and documents exactly what needs to change (runner label + `if: false` → `if: true`). Future contributors can find the activation path without reading ADRs.
