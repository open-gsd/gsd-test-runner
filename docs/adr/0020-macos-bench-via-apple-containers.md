# ADR-0020 — macOS Bench via Apple Containers

**Date**: 2026-05-23
**Status**: Accepted (2026-05-23)
**Amended**: 2026-05-24 — pivoted from Apple Containers to Docker on macOS — see Decision 5.
**Context**: Issue #8 — Create macOS containers to run on macOS hardware and test macOS-based products.

---

> **AMENDED 2026-05-24**
>
> The original ADR (2026-05-23) over-specified Apple Containers as the only valid runtime for macOS Benches.
> Apple Containers requires macOS 26 — not yet generally available on developer Macs or GitHub Actions runners.
> This amendment pivots to Docker on macOS (Docker Desktop or colima) using the Linux Tester Image as content,
> satisfying the actual user requirement (container isolation from the Mac's local filesystem) with technology
> available today. The Apple Containers path is preserved as a future evolution — see Decision 5.
>
> Decisions 1, 3, and 4 are revised below. Decision 2 is unchanged. Decision 5 is new.

---

## Context

Issue #8 asks for macOS as a Bench target. CONTEXT.md lists "macOS Containers (planned) — Apple's native container runtime, distinct from Docker Desktop. Planned as a fourth Runner target."

Apple Containers shipped in macOS 26 as a native sandbox runtime. The CLI is `container` (not `docker`). It runs OCI images, and unlike Docker on Mac — which runs Linux containers inside a Linux VM — Apple Containers can run macOS-native containers. This is the only way to test actual macOS code paths in a sandboxed environment on macOS hardware.

The existing Bench abstraction (ADR-0011, ADR-0014) assumes Docker as the container runtime, invoking `docker` directly in `internal/dockerexec`. macOS support requires the Local Engine to invoke `container` instead. The sentinel mechanism (OCI label `sh.gsd-test.image-version`, ADR-0011) is runtime-agnostic — both Docker and Apple Containers consume OCI images — so the sentinel continues to work without change.

Separately, GitHub Actions hosted runners with macOS 26 + Apple Containers preinstalled are not yet generally available (latest is `macos-15` as of this ADR). The original ADR established the design and file structure so that issue #8 can close; the actual macOS Tester Image build was gated on runner availability.

**Amendment context**: after writing the original ADR, it became clear that requiring macOS 26 + Apple Containers blocked practical use of macOS Benches entirely — no developer Macs and no GH Actions runners meet the requirement today. The actual user requirement from issue #8 is *container isolation from the developer's local Mac filesystem* during npm ci / build / test runs. Docker on macOS (Docker Desktop or colima) satisfies that requirement using technology available today.

---

## Decisions

### Decision 1 — macOS runtime: Docker (amended 2026-05-24; Apple Containers reserved for future)

**Current decision**: macOS Benches use Docker (Docker Desktop or colima) running Linux containers. The `runtime` field on `bench.Bench` defaults to `"docker"`, which applies to macOS Benches as well as Linux and Windows Benches.

Apple Containers becomes a future evolution path when macOS 26 + GH Actions `macos-26` runners are generally available. The `Runtime` field and `RuntimeContainer` constant are preserved in the codebase for that future — no code needs to change when the time comes.

**Original decision (superseded by amendment)**: Apple Containers was selected as the macOS runtime. Rejected alternatives included Docker Desktop (Linux containers in a VM, cannot sandbox macOS code paths), Podman (same limitation), and Lima/colima (VM managers, same limitation).

**Why Docker on macOS is sufficient now**: the user requirement is container isolation from the Mac filesystem — keeping `npm ci`, build artifacts, and test state out of the developer's local environment. Docker Desktop and colima both provide this isolation for Linux containers running on a Mac. macOS-specific code paths (FSEvents, case-insensitive HFS+, macOS-only Node APIs) are not exercised by this configuration, but that is a known and documented limitation rather than a blocker for most use cases.

### Decision 2 — Runtime abstraction: extend `dockerexec` with a runtime selector

*Unchanged from original ADR.*

The existing `internal/dockerexec` package (`Run`, `Stream`) hardcodes `"docker"` as the subprocess binary. To support Apple Containers (future), this binary must be selectable per Bench.

**Decision**: extend, not replace. Add a `Runtime string` field to `bench.Bench` with two sentinel values — `RuntimeDocker = "docker"` (default, preserves all existing behavior) and `RuntimeContainer = "container"` (Apple Containers, macOS Benches). Add a `RuntimeBin() string` method to `Bench` that returns the right binary name. Update `dockerexec.Run` and `dockerexec.Stream` to invoke `b.RuntimeBin()` instead of the hardcoded `"docker"` string.

**Rejected alternative — separate `containerexec` package**: would duplicate approximately 80% of `dockerexec`'s logic (pipe setup, scanner goroutines, context-cancellation unification, ExecError shape). The API surface the rest of the codebase sees (`Run`, `Stream`, `ExecError`) is unchanged under the extension approach. Cost of extension: two one-line changes in `dockerexec` and three additions to `bench.Bench`. Cost of a separate package: ~120 lines of copied code, import churn across all call sites, risk of the two packages diverging over time.

**Consequence for `bench.BenchDockerError`**: the error message currently says "docker X failed". This remains "docker" for backward compatibility — callers logging this error see the same format regardless of the underlying binary. If this becomes confusing in practice, a follow-up ADR can rename the field and update the message. Not addressed here.

**Consequence for `DOCKER_HOST`**: `DockerHost()` returns `"ssh://<host>"` regardless of runtime. Apple Containers supports the same SSH transport, so the value is reused unchanged.

### Decision 3 — Tester Image format: alias-publish (amended 2026-05-24; no separate macOS Dockerfile)

**Current decision**: no separate macOS Tester Image is built or published. The publish workflow re-tags the existing `gsd-tester-linux:vX.Y.Z` image as `gsd-tester-macos:vX.Y.Z` (alias publish on the Linux runner — a pure manifest operation, no rebuild). The Linux image provides container isolation when run on a Mac via Docker Desktop or colima.

`dockerfiles/macos.containerfile` is deleted. The `tag-macos-alias` job in the publish workflow depends on `publish-linux` and runs on `ubuntu-latest`.

The sentinel label (`sh.gsd-test.image-version`) is preserved on the alias automatically — re-tagging carries all labels from the source image. The publish workflow verifies this with a `docker image inspect` step.

**Original decision (superseded by amendment)**: a `dockerfiles/macos.containerfile` was added (using Apple Containers Dockerfile syntax). The file used `FROM scratch` as a placeholder pending Apple publishing official macOS base images.

### Decision 4 — GH Actions runner: no macOS runner needed for publish (amended 2026-05-24)

**Current decision**: no macOS GH Actions runner is needed for the publish. The `tag-macos-alias` job runs on `ubuntu-latest` and performs a pure manifest operation (pull Linux image, re-tag, push). The `macos-26` runner requirement is eliminated.

**Original decision (superseded by amendment)**: a `publish-macos` job was added with `if: false` gate, waiting on `macos-26` runner availability from GitHub Actions.

### Decision 5 (new, 2026-05-24) — Why this amendment

The original ADR over-specified Apple Containers as the only valid runtime for macOS Benches. The actual user requirement from issue #8 is container isolation from the developer's local Mac filesystem during npm ci / build / test runs — Docker on macOS satisfies that requirement using technology available today (Docker Desktop, colima).

The amendment makes macOS Benches usable immediately without waiting for macOS 26 or GH Actions runner updates. The `Runtime` field on `bench.Bench` and the `RuntimeContainer` constant are preserved in the codebase — when Apple Containers becomes broadly available, the path to enable it is: (1) set `runtime = "container"` in the bench config, (2) flip the publish job to build a native macOS image. No architectural changes are needed.

The amendment is made in-place on this ADR (not as a new ADR) because the framing and context remain correct — only the implementation decisions changed.

---

## Consequences

- `bench.Bench` gains a `Runtime` field and two constants. Existing bench values constructed without the field default to `""`, which `RuntimeBin()` maps to `"docker"` — fully backward-compatible.
- `internal/dockerexec.Run` and `internal/dockerexec.Stream` each change one line (hardcoded `"docker"` → `b.RuntimeBin()`). All existing tests continue to pass; the runtime abstraction is exercised by new unit tests in `internal/bench/bench_test.go`.
- `internal/config` gains `runtime` as an optional TOML field on `[[benches]]` entries. Omitting it (all existing configs) continues to work.
- `dockerfiles/macos.containerfile` is deleted (amendment). A `dockerfiles/macos.README.md` documents the alias-publish approach and the path back to a native macOS Tester Image.
- The `publish-macos` workflow job is replaced by `tag-macos-alias` — a pure manifest re-tag on `ubuntu-latest` that runs after `publish-linux` succeeds (amendment).
- Mac developers can use the harness today with Docker Desktop or colima — no macOS 26 requirement.
- No separate image build/publish for macOS — the alias keeps Tester Images in lock-step with the Linux image.
- macOS-specific code paths (FSEvents, case-insensitive HFS+, macOS-only Node APIs) are NOT tested by this configuration. Real macOS-native testing waits for Apple Containers + `macos-26` GH Actions runners.

---

## Alternatives considered

See Decision 1 (Docker Desktop rejected in original — now accepted as the interim path), Decision 2 (separate `containerexec` package rejected), and the original Decision 4 (omitting the macOS job entirely — rejected in favor of the disabled stub, now replaced by the alias-publish approach).

**Alias-publish (chosen amendment)**: re-tag the Linux image as `gsd-tester-macos:vX.Y.Z`. No separate image build. The Linux image provides container isolation on macOS. Chosen because it makes macOS Benches available today, requires zero new Dockerfile content, and keeps macOS and Linux Tester Images in lock-step automatically.

**Continue waiting for Apple Containers**: keep `if: false` and `macos-26` runner. Rejected because no timeline exists for `macos-26` runner availability on GH Actions, leaving macOS Bench support permanently blocked.

**Separate Linux-based macOS image**: build a dedicated Dockerfile with macOS-relevant tooling on top of Linux. Rejected because it adds maintenance burden without benefit — the Linux Tester Image already has everything needed (Node 22, npm, git, tar, reporter), and the content difference between "Linux Tester Image" and "Linux-based macOS Tester Image" would be zero in practice.
