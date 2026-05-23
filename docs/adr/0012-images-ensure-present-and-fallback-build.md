# 0012 — images.EnsurePresent: presence-only, on-Bench fallback build, pre-logged-in auth

Status: Accepted (2026-05-23)

## Context

ADR-0001 establishes the Tester Image as the released sandbox. ADR-0005 commits to GHCR for distribution with the in-repo Dockerfile as fallback. ADR-0009's illustrative orchestrator calls `images.EnsurePresent(bench, os, expectedVersion)` before each Pipeline starts. Implementing `EnsurePresent` forces four concrete decisions the architecture ADRs did not pre-commit:

1. **Who manages GHCR credentials on the Bench?** Pre-logged-in (one-time `docker login` per Bench), Local-Engine-managed via `gh auth token`, or per-pull credential injection.
2. **Where does the fallback Dockerfile build run?** On the Bench via `DOCKER_HOST=ssh://`, or locally on the Dev Workstation then transferred via `docker save | docker load`.
3. **Does EnsurePresent verify the version label, or just presence?** Verify both (duplicating Pipeline.CheckImageVersion's logic) or presence-only (deferring version check to per-Pipeline-run).
4. **Where do the Tester Image Dockerfiles live in the repo?** Top-level by name (`Dockerfile.linux`), top-level subdirectory (`dockerfiles/linux.Dockerfile`), or in-package (`internal/images/dockerfiles/`).

## Decision

**1. Bench-side GHCR auth: pre-logged-in.** Each Bench is `docker login ghcr.io`'d once per setup. Local Engine does not manage credentials. When a pull returns "unauthorized" / "denied" / "authentication required", EnsurePresent returns a `*PullAuthError` whose message points the user at the README setup step.

**2. Fallback build location: on the Bench via `DOCKER_HOST=ssh://<bench.Host>`.** The docker CLI runs on the Dev Workstation; build context streams over SSH to the Bench's daemon; the Bench performs the build. The Dev Workstation never needs docker installed — preserving ADR-0006's "static binary, no runtime dependencies" ergonomic.

**3. EnsurePresent is presence-only.** Pull if absent (or fall back to build); do not verify the OCI label. Version verification stays in `Pipeline.CheckImageVersion` per ADR-0011. Reasoning: CheckImageVersion runs within seconds of EnsurePresent finishing, so version mismatches surface immediately anyway; duplicating the label-check logic in two places is a single-responsibility violation we'd have to undo later.

**4. Dockerfile layout: `dockerfiles/<os>.Dockerfile` at the repo root.** Examples: `dockerfiles/linux.Dockerfile`, `dockerfiles/windows.Dockerfile`. Discoverable without root-directory clutter. The transitional `Dockerfile` at the repo root remains untouched during transition; new Tester Image Dockerfiles live in `dockerfiles/`.

The function shape:

```go
func EnsurePresent(ctx context.Context, b bench.Bench, image ImageID, opts EnsurePresentOptions) error

type EnsurePresentOptions struct {
    // FallbackDockerfile, if non-empty, is the path to a Dockerfile
    // EnsurePresent builds when the pull fails with "not found". Empty
    // disables fallback (pull-only mode).
    FallbackDockerfile string
    // FallbackContextDir is the build context directory passed to
    // docker build. Required when FallbackDockerfile is set.
    FallbackContextDir string
}
```

Error classification (typed Causes returned as `error`, NOT wrapped in an envelope — EnsurePresent is upstream of the Pipeline's LegError envelope per ADR-0009):

- `*PullAuthError` — pull failed with auth-related stderr ("unauthorized", "denied", "authentication required").
- `*PullNotFoundError` — pull failed with not-found stderr ("manifest unknown", "not found", "manifest for ... not found") AND no fallback was configured.
- `*PullDockerError` — pull failed for any other reason (network, registry 5xx, etc.).
- `*BuildError` — fallback build failed.
- `*BenchDockerError` — the initial presence check (`docker image inspect`) failed for a non-image-related reason (Bench unreachable, daemon down).

## Consequences

+ Local Engine carries no GHCR-credential state. Setup happens once per Bench; thereafter zero auth ceremony in Local Engine code.
+ The fallback build path requires no docker on the Dev Workstation. ADR-0006's "single static binary" ergonomic survives even when GHCR is unreachable.
+ EnsurePresent and CheckImageVersion have clean, non-overlapping responsibilities. Future contributors don't have to ask "which one should verify the version?"
+ The Dockerfile layout is discoverable via top-level `ls`. CI (per ADR-0005) can use the same paths.
- A Bench that loses its GHCR credentials (token expired, etc.) fails noisily on the next EnsurePresent. Detection is good, but recovery requires manual SSH-and-relogin. Future improvement: surface a clearer error message with the precise re-login command. Not load-bearing for v1.
- Fallback builds upload the build context over SSH for every build. For large contexts this is slow. Acceptable: fallback is a rare path used only when GHCR is unreachable or the image isn't yet published.
- The "v1.2.3 tag points at a v1.2.2 image" failure surfaces inside Pipeline.CheckImageVersion (a few seconds after EnsurePresent), not inside EnsurePresent. Slightly worse failure-localization than the alternative, but the cost of the duplicated label-check logic is worse than the cost of the slightly-delayed error.
- The Bench's docker daemon performs the fallback build, which means the build artifacts (intermediate layers, build cache) live on the Bench. Two Benches building the same Dockerfile produce independent caches. Not a problem for correctness; relevant for cross-Bench build-time consistency.

## Alternatives considered

- **Local-Engine-managed GHCR auth via `gh auth token`** — Rejected: requires the Dev Workstation user to have a `gh` token with GHCR scope; re-shells SSH on every pull to refresh credentials; introduces multiple silent failure modes (wrong token, expired token, missing scope).
- **Per-pull credential injection via `--password-stdin`** — Rejected: `docker pull` does not accept per-command credentials. The mechanism doesn't exist.
- **Fallback build on Dev Workstation then `docker save | ssh bench docker load`** — Rejected: requires the Dev Workstation to have docker installed, contradicting ADR-0006's static-binary ergonomic.
- **EnsurePresent verifies the OCI label** — Rejected: duplicates Pipeline.CheckImageVersion logic. The "earlier failure localization" benefit is marginal because CheckImageVersion runs within seconds.
- **Dockerfile per-package layout (`internal/images/dockerfiles/`)** — Rejected: the Dockerfiles are consumed by CI per ADR-0005 too, not just Local Engine. Hiding them inside `internal/` makes them feel package-private when they aren't.
- **Dockerfile top-level by name (`Dockerfile.linux`)** — Rejected: clutters the root directory once more than two OSes are supported.
