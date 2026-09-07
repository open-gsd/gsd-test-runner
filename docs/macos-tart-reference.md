# macOS via Tart Reference

Field-by-field, exhaustive reference for `RuntimeTart`. For tasks see the [how-to guides](macos-tart-how-to.md); for concepts see [macOS via Tart](macos-tart.md). Design is fixed in [ADR-0030](adr/0030-macos-bench-via-tart.md).

## The `runtime = "tart"` config value

Set on a `[[benches]]` entry with `os = "macos"`. See the [Configuration Reference's `[[benches]]` table](configuration.md#benches) for the field's full definition alongside `RuntimeDocker` and `RuntimeContainer`. Setting `runtime = "tart"` on a Bench:

- Routes that Bench's pipeline execution through `internal/tartpipeline.Pipeline` instead of `internal/pipeline.Pipeline`.
- Routes image presence checks through `internal/images.EnsurePresentTart` instead of `EnsurePresent` — see [Known limitations](#known-limitations) for how that check differs (no build-fallback).
- Requires Tart installed on the Bench and a `gsd-tester-macos-tart` image already pulled or pullable — see the [how-to guide](macos-tart-how-to.md#how-to-install-tart-on-a-bench).

Everything else about the Bench entry (`name`, `host`, `capacity`, `platform`) is unchanged from a Docker-runtime Bench.

## The 8-leg execution sequence

`tartpipeline.Pipeline` reports the same 8 named legs as the Docker pipeline (`CheckImageVersion`, `StartContainer`, `CopyWorktree`, `NpmCI`, `Build`, `RunTests`, `Drain`, `Parse`) — every leg still emits its own `EventLegStart`/`EventLegSuccess`/`EventLegFailure`, so the renderer and any `--json-events` consumer treat a Tart run identically to a Docker run in terms of leg *names* and event *shape*.

**The call order differs, and this matters if you compare a Docker run and a Tart run for the same suite:**

| | Docker (`internal/pipeline`) | Tart (`internal/tartpipeline`) |
|---|---|---|
| Leg call order | `CheckImageVersion → StartContainer → CopyWorktree → NpmCI → Build → RunTests → Drain → Parse` | `CopyWorktree → StartContainer → CheckImageVersion → NpmCI → Build → RunTests → Drain → Parse` |

The reordering is structural, not arbitrary:

- **`CopyWorktree` moves before `StartContainer`.** Docker's `docker cp` copies into an already-running container, so the container starts first. Tart's `--dir` share (the copy-in mechanism) must be attached at `tart run` boot time — the worktree has to be on the Bench's filesystem *before* the VM boots. `StartContainer` and `CopyWorktree` are effectively the same operation on Tart: clone, set memory, boot with the mount attached, wait for the guest to become reachable, all under the `StartContainer` leg.
- **`CheckImageVersion` moves after `StartContainer`.** Docker reads the `sh.gsd-test.image-version` OCI label via `docker image inspect` without running anything. Tart has no confirmed way to read a label back after pull (see [Decision 3](adr/0030-macos-bench-via-tart.md)), so the version sentinel is an in-guest file (`/opt/gsd-test/image-version`) read over SSH — which requires the guest to already be booted and reachable.

If you script something that reads `per_os`/leg events across both a Docker run and a Tart run of the same suite, expect the same 8 leg names in both, but not the same order of *which leg's events appear first*.

## `DefaultMemoryMB`

`internal/tartpipeline.DefaultMemoryMB = 4096` (4 GB). Applied via `tart set --memory <MB>` before boot, in the `StartContainer` leg's `set_memory` sub-step — this is the Tart analogue of `internal/dispatch.DefaultMemory` ("2g") for Docker Benches, and the direct fix for the bare-metal Runner's OOM failure mode (ADR-0030 Decision 5). The value is higher than Docker's 2g because a real macOS guest carries more baseline memory overhead than a Linux container.

The cap is hypervisor-enforced at the VM configuration level, not just advisory. Setting it too low manifests as an **out-of-memory failure inside the guest** — a test process killed by the guest's own kernel, or the guest becoming unresponsive under memory pressure — not a host-level failure. That is the entire point: a runaway test is bounded by the VM's allocation and cannot balloon into the host Mac's RAM the way a bare-metal process can.

## Leaked-VM reaping (`internal/tartreaper`)

Tart carries no label/tag mechanism at all — confirmed empirically (`tart get`/`tart list --format json` return no metadata field, see [ADR-0030 Decision 7](adr/0030-macos-bench-via-tart.md)) — so there is no direct Docker-`--label` equivalent to attribute a leaked VM to a branch/run/deadline. Instead, `StartContainer` writes a small JSON side-channel state file to the Bench at `/tmp/gsd-test-tart-state/<vmName>.json` (`{"run_id":...,"branch_slug":...,"deadline_ms":...}`) as a **required** sub-step: if the write fails, the leg fails, but `Cleanup` still fires in the same run and removes the VM immediately. This means a VM can only ever survive past its own pipeline's lifetime if the state-file write succeeded.

`internal/tartreaper.Sweep` reads these state files to reap `gsd-tart-*` VMs whose deadline has passed, mirroring `internal/reaper.Sweep`'s branch-scoped + safety-net two-tier shape (including reusing `reaper.SafetyNetGrace` directly, so the cross-branch grace period can never drift between the two runtimes). A `gsd-tart-*` VM with no readable state file is never swept automatically — by construction, per the invariant above, it cannot be a normal leak from this pipeline. This sweep is wired into both `internal/runner`'s automatic pre-run sweep (alongside the existing Docker-side sweep) and the manual [`gsd-test sweep`](run-and-die-reference.md#gsd-test-sweep) command, which now covers Tart Benches transparently alongside Docker/Apple Containers Benches in the same invocation.

## Known limitations

These are the exhaustive, currently-known gaps in `RuntimeTart` versus the Docker pipeline. None of them are hidden — if you hit one, this is the section that explains it.

- **No live per-test event streaming during `RunTests`.** `internal/pipeline.Pipeline`'s Docker path runs a concurrent JSONL-tail goroutine that emits `EventTestPass`/`EventTestFail` as each test completes. `tartexec.Exec` is a two-hop SSH request/response primitive (issue one command, get one result back), not a streaming primitive the way `dockerexec.Stream` is — mirroring Docker's live-tail behavior would need a materially harder mechanism (a second concurrent `tail -F` Exec racing the main run, or polling), which `RuntimeTart` does not implement. Renderer consumers see nothing from a Tart run's `RunTests` leg until the whole suite finishes, then the full captured stdout/stderr as one `EventChildOutput` burst, followed by `Drain`/`Parse`'s aggregate pass/fail/total counts. This is a real UX gap versus Docker, not a bug.

  **This limitation is not fixed by the liveness signal below.** While `RunTests`'s single blocking guest-exec call is in flight, a concurrent goroutine periodically (every 20s, `internal/tartpipeline.livenessProbeInterval`) probes the VM via `internal/tartreaper.Probe` (`tart get <vmName> --format json`, confirmed real single-object shape per [ADR-0030 Decision 8](adr/0030-macos-bench-via-tart.md)) and emits a `pipeline.EventLiveness` event carrying a human-readable statement of whether the VM is still alive — rendered by the TTY renderer at `VerbosityNormal`/`VerbosityFull` (suppressed at `VerbosityQuiet`, matching the existing pass-count heartbeat's own quiet-suppression). This tells you the VM is still alive/registered/running (or isn't, or has vanished entirely) — it does **not** tell you which test is currently executing or how far through the suite it is. It closes the "is this hung?" gap for a long, silent `RunTests` leg without closing the live-per-test-streaming gap above.
- **No exact-patch Node version pinning.** `macos-tart-provision.sh` installs Node via `brew install node@${NODE_MAJOR}` — Homebrew always resolves to whatever patch release is currently in `homebrew-core` for that major. Unlike a Docker image tag, there is no way to pin an exact patch version (e.g. `22.19.0`) in the bake script. This is a known, accepted limitation of the Homebrew-based provenance choice, not something worked around.
- **`EnsurePresentTart` has no build-fallback.** `internal/images.EnsurePresent` (Docker) falls back to a local `docker build` when an image is missing. `EnsurePresentTart` does not — baking a Tart image requires the full Packer pipeline (`dockerfiles/macos-tart.pkr.hcl`), which is not something to trigger ad hoc mid-test-run. A missing image on the Bench is a hard pull failure (`*PullNotFoundError` or `*PullAuthError`, the same types Docker's `EnsurePresent` returns), not an automatic local build.

## The Tester Image bake recipe

The macOS Tart Tester Image is built by Packer (`dockerfiles/macos-tart.pkr.hcl`, using the `cirruslabs/tart` Packer plugin) plus a shell provisioner (`dockerfiles/macos-tart-provision.sh`) that runs inside the guest.

**Packer variables** (`macos-tart.pkr.hcl`):

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `node_version` | Yes | — | Node major to bake in (e.g. `"22"`), mirrors `linux.Dockerfile`'s `ARG NODE_VERSION`. |
| `image_version` | Yes | — | Tester Image version sentinel (e.g. `"v1.9.0"`), mirrors `linux.Dockerfile`'s `ARG IMAGE_VERSION`. |
| `vm_base_name` | No | `ghcr.io/cirruslabs/macos-tahoe-base:latest` | Cirrus Labs base image to clone from. Must ship the Tart Guest Agent. |

The `tart-cli` Packer source clones `vm_base_name` into a VM named `gsd-tester-macos-tart-node<node_version>` (per-major naming so concurrent builds for different Node versions don't collide on one Bench), configured with 4 CPUs, 8 GB memory, a 50 GB disk, and the base image's default `admin`/`admin` SSH credentials.

**What the provisioning script does** (`macos-tart-provision.sh`, run in this order):

1. Installs Xcode Command Line Tools headlessly (a placeholder file trick avoids the interactive `xcode-select --install` GUI dialog).
2. Bootstraps Homebrew non-interactively if not already present.
3. Installs Node via `brew install node@${NODE_MAJOR}` — keg-only, so it's referenced via its keg's own `bin` path (`/opt/homebrew/opt/node@<major>/bin`) rather than a symlinked `/opt/homebrew/bin/node`.
4. Verifies `node --version` / `npm --version` succeed, failing loud (non-zero exit) if either is broken.
5. Installs the Reporter to `/opt/gsd-test/reporter.mjs` (uploaded by Packer's `file` provisioner to a world-writable temp path first, then moved into the root-owned `/opt/gsd-test` with corrected ownership).
6. Writes the **version-sentinel file** at `/opt/gsd-test/image-version` (plain text, the `image_version` value) — this is what `tartpipeline.Pipeline`'s `CheckImageVersion` leg reads over SSH, since Tart has no confirmed OCI-label read-back mechanism (see [Decision 3](adr/0030-macos-bench-via-tart.md)).

**Default guest credentials — a known, unrotated security fact.** The baked image ships the Cirrus Labs base image's default guest account, **`admin` / `admin`**, unchanged. This is not a debugging convenience left in by accident — it is load-bearing, since SSH (using these exact credentials) is `RuntimeTart`'s entire in-guest execution transport (see [ADR-0030 Decision 4](adr/0030-macos-bench-via-tart.md)). Rotating or scoping down these credentials in the baked `gsd-tester-macos-tart` image is a known, explicitly flagged follow-up — **not yet done**. Anyone publishing their own image should be aware every VM cloned from it shares this same default account until that follow-up lands.

## The `publish-macos-tart` CI job

`.github/workflows/publish-tester-images.yml`'s `publish-macos-tart` job builds and publishes the image via CI, as an alternative to running `packer build` locally.

- **Requires a self-hosted runner**: `runs-on: [self-hosted, macos, tart]`. It does **not** run on GitHub-hosted `macos-*` runners — whether those expose the nested-virtualization CPU features Tart's Virtualization.framework usage needs is unconfirmed by ADR-0030's research. If you want to publish your own image via CI, you need a self-hosted Mac runner carrying those three labels.
- Installs `packer`, `tart`, and `sshpass` via Homebrew if not already present (idempotent — safe to re-run).
- Authenticates to GHCR via `TART_REGISTRY_USERNAME`/`TART_REGISTRY_PASSWORD` environment variables (`tart pull`'s documented env-var auth mechanism, which `push` shares) — no `tart login`/Keychain involved, so no interactive-session dependency on the runner.
- Runs `packer init` then `packer build` with `node_version`/`image_version` set from the matrix (`node: ["22", "24"]`) and the resolved release tag.
- Pushes the built VM with `tart push`, tagged `gsd-tester-macos-tart:<tag>-node<major>`, carrying both `sh.gsd-test.image-version` and `sh.gsd-test.node-major` labels. The Active-LTS major (`DEFAULT_NODE_MAJOR`, currently `"24"`) additionally gets the plain `:<tag>` and `:latest` tags, matching the Linux/Windows publish jobs' conditional-tag pattern.
- Verifies the push by cloning the just-pushed image fresh, booting it, waiting for an IP, and reading back the in-guest sentinel file over SSH with the default `admin`/`admin` credentials — the same read path `CheckImageVersion` uses at runtime, since there's no label-inspect equivalent to verify against directly.
