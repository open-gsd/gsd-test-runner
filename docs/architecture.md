# Architecture

This document is for contributors. It describes how `gsd-test` is built and where to find things. For design rationale, read the ADRs in [docs/adr/](adr/).

## The 5-phase orchestrator

`cmd/gsd-test/main.go` sequences five phases on every invocation (per ADR-0018):

| Phase | Code | Description |
|-------|------|-------------|
| 1. **Load** | `config.Load` | Reads `config.toml`. Optionally probes Bench reachability (`--probe-benches`). |
| 2. **Plan** | `plan.Build` | Pure function. Resolves target OSes to `(Bench, ImageID, Version)` tuples. Produces `Plan{Runs, Skipped}`. |
| 3. **EnsureImages** | `images.EnsurePresent` (parallel) | Confirms each Tester Image is present on its Bench. Pulls from GHCR; falls back to local build if pull fails. |
| 4. **RunPipelines** | `pipeline.Pipeline.RunAll` (parallel) | Runs the 8-leg pipeline per OS. Renderer subscribes to event channels for live progress. |
| 5. **Aggregate+Render** | `renderer.Renderer` | Collects per-OS Reports and LegErrors. Maps to exit code 0/1/2. Prints final summary. |

Phases 3 and 4 do I/O. Phases 1, 2, and 5 are CPU/memory only and are independently testable against synthetic inputs.

## Package layout

| Package | Path | Summary |
|---------|------|---------|
| `cmd/gsd-test` | `cmd/gsd-test/` | CLI entry point. Flag parsing, phase sequencing, exit code mapping. |
| `config` | `internal/config/` | TOML config loading and validation. Bench registry, version map, defaults. |
| `plan` | `internal/plan/` | Pure resolver. Builds the list of `Run` values from config + selector. |
| `bench` | `internal/bench/` | `Bench` type (with `Runtime` field; `"docker"` default for all Benches today; `"container"` reserved for future Apple Containers). `Selector` (round-robin + pin/exclude). `BenchDockerError`. |
| `refs` | `internal/refs/` | Git ref resolution. Converts `--base` and `--head` string refs to SHAs. |
| `worktree` | `internal/worktree/` | PR-merged worktree construction. Shallow clone + real `git merge` in a scratch directory. |
| `images` | `internal/images/` | `EnsurePresent`: pull from GHCR, fall back to `docker build` on the Bench. Typed pull errors. |
| `dockerexec` | `internal/dockerexec/` | Low-level docker subprocess wrapper. Sets `DOCKER_HOST=ssh://<bench.Host>`. `ExecError` type. `Stream` for line-by-line output. |
| `pipeline` | `internal/pipeline/` | 8-leg Per-OS Pipeline. `LegError` envelope. Typed Cause errors per leg. Event emission. |
| `parse` | `internal/pipeline/` (same package) | JSONL parser for test events emitted by the Reporter. Called by the Parse leg. |
| `report` | `internal/report/` | `Report` type: pass/fail counts, `[]Failure` list. `KindPass` / `KindFail` discriminant. |
| `renderer` | `internal/renderer/` | Consumes per-OS event channels. TTY mode (human) and JSON-events mode (machine). |

## The 8 pipeline legs

The `pipeline.Pipeline` type owns 8 legs (per ADR-0008). They run in order; a failure in any leg short-circuits the rest.

| # | Leg | What it does |
|---|-----|-------------|
| 1 | `CheckImageVersion` | Reads the `sh.gsd-test.image-version` OCI label from the Tester Image on the Bench via `docker image inspect`. Fails if it does not match the expected version from config. |
| 2 | `StartContainer` | Starts a fresh idle container (`docker run --rm -d ... sleep infinity`). Records the container ID. |
| 3 | `CopyWorktree` | Copies the PR-merged worktree into `/work` in the container via `docker cp`. No bind-mounts. |
| 4 | `NpmCI` | Runs `npm ci` inside the container. Streams stdout/stderr live as `EventChildOutput`. |
| 5 | `Build` | Runs `npm run build` inside the container. Streams output live. |
| 6 | `RunTests` | Runs `node --test --test-reporter=/opt/gsd-test/reporter.mjs`. A concurrent tail goroutine emits `EventTestPass`/`EventTestFail` as the Reporter writes JSONL. |
| 7 | `Drain` | Copies the JSONL capture file from the container to a local temp file via `docker cp`. |
| 8 | `Parse` | Parses the JSONL file into structured `report.Failure` values. Populates `Report`. |

`RunAll` runs all 8 legs and defers `docker rm -f` so the container is removed even on failure.

## Tester Image contract

Each Tester Image (per ADR-0001) provides:

- **Base OS**: Debian Bookworm (Linux), Windows Server Core LTSC 2022 (Windows).
- **Node 22**: pre-installed.
- **Build toolchain**: `npm` (bundled with Node), `git`, `tar`.
- **Reporter** at `/opt/gsd-test/reporter.mjs` (Linux) or `C:\opt\gsd-test\reporter.mjs` (Windows). This path is contractual — `RunTests` passes it as `--test-reporter=`.
- **Sandbox isolation**: `HOME=/home/test` (Linux) with world-writable permissions. The container is `--rm`; no state persists across runs.
- **Image-version sentinel**: OCI label `sh.gsd-test.image-version` set at build time via `ARG IMAGE_VERSION`. `CheckImageVersion` reads this label.

Images are published to GHCR on every `v*.*.*` tag by `.github/workflows/publish-tester-images.yml`. The publish workflow verifies the sentinel label before succeeding.

## Event emission contract

The Pipeline emits typed `Event` values on a `chan<- Event` (per ADR-0017):

| Event kind | When | Key fields |
|------------|------|-----------|
| `EventLegStart` | Each leg begins | `Leg` |
| `EventLegSuccess` | Leg completes without error | `Leg` |
| `EventLegFailure` | Leg fails | `Leg`, `Detail` (error message) |
| `EventChildOutput` | Subprocess stdout/stderr line | `Line`, `Stream` (`"stdout"` or `"stderr"`) |
| `EventTestPass` | Reporter emits a passing test | `Line` (test name) |
| `EventTestFail` | Reporter emits a failing test | `Line` (test name), `Detail` (failure output) |

The `renderer.Renderer` is one consumer. `--json-events` switches to a JSON Lines renderer that emits the same events as machine-readable output.

## ADR index

All design decisions live in [docs/adr/](adr/). Read them in numeric order for the full context. If you disagree with a decision, file an issue — do not re-litigate in code review.

| ADR | Topic |
|-----|-------|
| 0001 | Tester Image is the released sandbox |
| 0002 | PR-merged worktree instead of bind-mount |
| 0003 | Per-OS pass/fail replaces cross-OS diff |
| 0004 | Fail-loud at every pipeline leg |
| 0005 | Tester Images published to GHCR |
| 0006 | Local Engine written in Go |
| 0007 | Dev Workstation / Bench vocabulary |
| 0008 | Per-OS Pipeline executor shape |
| 0009 | Local Engine top-level orchestration |
| 0010 | PRRef resolution contract |
| 0011 | Image-version sentinel + Bench transport |
| 0012 | `images.EnsurePresent` + fallback build |
| 0013 | Per-OS Report shape |
| 0014 | `dockerexec` extraction |
| 0015 | Drain/Parse split |
| 0016 | `bench.Selector` design |
| 0017 | Event emission contract |
| 0018 | Local Engine orchestration phases |
| 0019 | Local Engine binary distribution via GitHub Release assets |
| 0020 | macOS Bench via Apple Containers (amended 2026-05-24: pivoted to Docker on macOS) |
