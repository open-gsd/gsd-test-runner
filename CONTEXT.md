# gsd-test-runner — Domain Context

## Status: transitioning to target architecture

The current implementation is a quick-built local-dev harness (described in the Transitional vocabulary section). The end goal — and the architecture that new work should target — is described in the Target vocabulary section. ADRs 0001–0005 in `docs/adr/` lock in the load-bearing decisions for the target architecture.

gsd-test-runner is a local-dev harness, not a CI system. It runs on the developer's own machine and on Linux/Windows boxes they have SSH access to — typically their own hardware: a desktop under the desk, a home lab box, a second machine. The point is parity with what CI would catch, but at edit-loop latency, before pushing. It runs the same Node test suite across three platforms (macOS native, Linux Docker, Windows Docker) in parallel and diffs the results. The bugs it catches are platform-specific: case-sensitive filesystems, missing system tools, different home directories, and path-separator divergence. All runtime code is shipped as heredocs inside a single idempotent installer (INSTALL_GSD_TEST.sh, ~1300 lines) that writes executables to ~/.local/bin/ and the shared Node reporter to ~/.local/share/gsd-test/.

Future direction: **macOS Containers** support is in active groundwork — Apple's native container runtime (distinct from Docker Desktop) as a fourth Runner target. The current macOS Runner is bare-metal `node --test`; a future macOS Runner will run inside an Apple container for true sandbox parity with the Linux and Windows Docker runners. The runtime abstraction, Containerfile, and workflow job are in place (see ADR-0020); activation awaits a macOS 26 GitHub Actions runner with Apple Containers preinstalled.

---

## Target vocabulary

### Tester Image

A versioned Docker image, one per supported OS (Linux, Windows, future macOS Containers), holding only the *test-execution sandbox*: base OS, Node runtime, build toolchain, the Reporter at a known in-image path, sandbox config (HOME, perms, npm cache location), and an Image-version sentinel file. Contains zero project code and zero test files. Distributed primarily via GHCR; the Dockerfile in this repo is the fallback build path and the canonical source CI builds from.

Dockerfiles live at `dockerfiles/linux.Dockerfile` and `dockerfiles/windows.Dockerfile` (per ADR-0012). The Reporter is baked in at `/opt/gsd-test/reporter.mjs` (Linux) and `C:\opt\gsd-test\reporter.mjs` (Windows); this path is contractual — the Local Engine's RunTests leg invokes `node --test --test-reporter=/opt/gsd-test/reporter.mjs`. The Image-version sentinel is the OCI label `sh.gsd-test.image-version` (per ADR-0011), injected at build time via `ARG IMAGE_VERSION`. The Reporter source of truth is `reporter/reporter.mjs` in this repo; Dockerfiles COPY it in at build time.

### Dev Workstation

The developer's own machine where the Local Engine runs. Must be platform-agnostic: macOS, Linux, or Windows. Holds the project source, the Local Engine binary, and SSH credentials for reaching one or more Benches. Does NOT run containers itself — that work is offloaded to Benches so the laptop isn't burdened by full test suites.

### Bench

A remote machine the Dev Workstation hands containerized test runs off to. SSH-reachable, has Docker (or a compatible container runtime) installed, and holds one or more pulled Tester Images. One Bench per target OS family (Linux Bench, Windows Bench, future macOS Container Bench). Typically the developer's own hardware — a desktop, a home-lab box, a spare workstation — not shared infrastructure or CI fleet. The name comes from the engineering "test bench": an isolated environment for running experiments on the unit under test.

### Image-version sentinel

A file baked into each Tester Image at a known path containing the image's version tag. The Local Engine reads it before running; if it doesn't match the expected version for this repo checkout, the Engine fails loud. Closes the "stale image silently produces wrong results" failure class.

### PR-merged worktree

The per-run payload. Constructed by the Local Engine in a scratch clone: base branch fetched, PR branch merged into it via real `git merge`. Merge conflicts surface here as a named failure, before any container starts. Contains all project source AND all tests including the PR's new/changed ones. Copied (not bind-mounted) into the Tester Image's container at run time.

### Local Engine

The developer-side launcher, distributed as a single static Go binary per Dev Workstation OS (see ADR-0006). Per OS: (1) constructs the PR-merged worktree on the Dev Workstation, (2) selects a Bench for the target OS and verifies the right Tester Image is present on that Bench (pulls from GHCR if not, or builds from the in-repo Dockerfile as fallback), (3) checks the Image-version sentinel, (4) copies the worktree into a fresh container on the Bench, (5) runs npm ci + build + the full test suite, (6) drains JSON Lines output, (7) emits a Per-OS report. Each target OS runs independently on its assigned Bench; there is no cross-OS comparison.

### Per-OS report

The output shape for one OS run. Either `pass x/y` (with x == y) or a structured fail report listing failed tests with file, name, and captured output. Replaces the old cross-platform Diff entirely — if Linux passes and Windows fails, the user reads two reports rather than a divergence summary.

### Failure-first output (ADR-0023)

How runs are surfaced to an agent. The live TTY stream is **quiet by default** — a compact per-OS heartbeat plus loud, real-time failures (`✗ FAIL file:line · class · msg`); `--verbose` / `GSD_TEST_VERBOSE=1` restores the full firehose, `--quiet` drops the heartbeat, and `--json-events` still emits the full typed stream. Every run also writes a deterministic, failure-only digest under `$XDG_STATE_HOME/gsd-test/runs/<run-id>/` (`FAILURES.md`, `failures.json` with full untruncated evidence, `failures/NN-<slug>.md`, `junit.xml`, and the per-OS JSONL), and prints exactly one machine-readable **verdict** line (`{"type":"verdict",…}`) as the last line of stdout in every mode and outcome. The same `internal/digest` serializer and verdict back both the standard multi-OS path and the run-and-die path. Truncated blobs carry a pointer back into `failures.json`; identical failures are de-duplicated across OSes ("N failures, M unique"). Event emission is lossless (unbounded queue + pump; see ADR-0017 as amended).

### Fail-loud pipeline

The Local Engine's contract: every leg of the pipeline (image-version check, merge, copy-in, container start, npm ci, build, test run, JSONL drain, parse, report) emits a structured failure with the specific leg that failed and a path to diagnostics. The "neither platform produced test events" silent failure is the anti-pattern this contract exists to prevent.

### GHCR distribution

Tester Images are published to GitHub Container Registry from this repo's CI on tagged releases. Local Engines pull tagged images by version. Eliminates the per-host build drift that plagued the transitional architecture. Benches pull tagged images on first use; the Dev Workstation never holds Tester Images itself.

### macOS Containers (Apple Containers)

Apple's native container runtime for macOS 26+. The CLI is `container` (not `docker`). Unlike Docker Desktop on Mac — which runs Linux containers inside a Linux VM — Apple Containers runs macOS-native containers, giving genuine macOS sandbox parity with the Linux and Windows Docker runners. The macOS Bench target uses `bench.RuntimeContainer` as its runtime selector (ADR-0020); `internal/dockerexec` invokes the `container` binary when `Bench.Runtime == RuntimeContainer`. The macOS Tester Image is defined at `dockerfiles/macos.containerfile`; the publish-macos CI job is present but gated (`if: false`) until a macOS 26 GitHub Actions runner with Apple Containers is available. See ADR-0020 for the full design.

---

## Transitional vocabulary (current code — to be retired)

### Local-dev harness

_Transitional — will be retired when the target architecture lands._

What this project is. It runs on developer hardware — not CI, not cloud, not shared infra. The SSH-reachable Hosts are the developer's own machines (workstation, home lab, second box). The goal is to surface platform-specific bugs in the edit loop, before a push ever reaches CI.

### Runner

_Transitional — will be retired when the target architecture lands._

A per-platform script that executes the test suite on one platform and emits JSON Lines on stdout. Three exist: gsd-test (Linux Docker), gsd-test-local (macOS native), and gsd-test-windows (Windows Docker). Each implements the same CLI surface and output schema.

### Reporter

_Transitional — will be retired when the target architecture lands._

The shared Node test reporter (reporter.mjs) consumed by every Runner. Emits one JSON Line per test event and marshals Error objects so they survive JSON serialization.

### Orchestrator

_Transitional — will be retired when the target architecture lands._

gsd-test-both. Spawns 2–3 Runners in parallel, collects their JSON Lines into per-PID files under /tmp, and invokes the Diff.

### Diff

_Transitional — will be retired when the target architecture lands._

gsd-test-diff. A Python comparator that reads 2–3 JSON Lines streams, normalizes platform-specific paths (Docker /work/, Windows C:\work\, Mac absolute) into a canonical form, and prints divergence between platforms.

### Mirror

_Transitional — will be retired when the target architecture lands._

The rsync'd copy of the developer's working tree on a remote Docker host. Persistent across runs (delta sync) and mounted into the ephemeral test container as /work.

### Host

_Transitional — superseded by the target term **Bench**. Retained here only to map old terminology to new._

An SSH alias listed in ~/.config/gsd-test/hosts (Linux) or ~/.config/gsd-test/windows-hosts (Windows). These are typically the developer's own machines — a workstation, a home lab box, a second box on the same network — not shared infra or a CI fleet. Key-auth SSH is the assumed trust boundary because it's the developer's own network. Must be key-authenticated and have Docker installed.

### Installer

_Transitional — will be retired when the target architecture lands._

INSTALL_GSD_TEST.sh. The idempotent installer that emits all Runners, the Reporter, the Diff, and both Dockerfiles via heredocs. The repository's source of truth for runtime code.

### JSON Lines stream

_Transitional — will be retired when the target architecture lands._

The canonical inter-module data format. One JSON object per line, one line per test event. Produced by the Reporter and consumed by the Diff.

### Path normalization

_Transitional — will be retired when the target architecture lands._

The Diff's central responsibility: rewriting platform-specific paths (/work/foo.test.js, C:\work\foo.test.js, /Users/x/proj/foo.test.js) to a single canonical form so divergence detection compares apples to apples.

---

## Module map (transitional code)

These modules describe the current INSTALL_GSD_TEST.sh-embedded implementation. The target architecture in ADRs 0001–0005 replaces this structure.

| Module | Location in INSTALL_GSD_TEST.sh | Language | Lines | Notes |
|---|---|---|---|---|
| Reporter | lines 77–86 | JavaScript ESM | 10 | |
| gsd-test (Linux Runner) | lines 91–292 | Bash | 201 | |
| gsd-test-windows (Windows Runner) | lines 298–498 | Bash | 200 | |
| gsd-test-local (macOS Runner) | lines 504–580 | Bash | 76 | bare-metal `node --test`; macOS Containers runner planned |
| gsd-test-both (Orchestrator) | lines 586–690 | Bash | 104 | |
| gsd-test-diff (Diff) | lines 696–954 | Python | 258 | |

---

## Out of scope

- **The get-shit-done project itself.** This harness runs its tests; it does not own them.
- **CI execution.** This project is deliberately a local-dev tool. CI parity is the goal — catching platform bugs before you push, while the edit loop is still hot — but CI execution is explicitly someone else's job. There is no .github/workflows here and there never will be.
- **Tested-package conventions.** This repo has no tests of its own — the harness is too thin to warrant them, and dogfooding via the get-shit-done suite covers the real risks.
- **Cross-OS divergence reporting.** The transitional Diff did this; the target Per-OS report does not. If platforms disagree, the user reads two reports.
- **Running containers on the Dev Workstation.** The Dev Workstation orchestrates; Benches execute. A developer with no Bench cannot use the harness — they must SSH-reach at least one machine per target OS they want covered.
