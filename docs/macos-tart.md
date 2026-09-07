# macOS via Tart

This document explains *why* `RuntimeTart` exists and what it is at a glance. For hands-on material see the [how-to guides](macos-tart-how-to.md) and the [reference](macos-tart-reference.md). For the full design record and research trail, read [ADR-0030](adr/0030-macos-bench-via-tart.md).

## The problem: no real macOS-native execution, and an unbounded-memory Runner

Before this feature, `gsd-test` had two ways to touch macOS, and neither actually ran macOS as the guest under test:

- **Docker-on-macOS** (the current default for `os = "macos"` Benches) runs the *Linux* Tester Image inside Docker Desktop or colima on a Mac host. It isolates `npm ci` / build / test from your Mac's local filesystem, but the code under test never sees FSEvents, case-insensitive HFS+ semantics, or a macOS-only Node/system API — it's Linux, running on a Mac.
- **Apple Containers**, evaluated as the native path in [ADR-0020](adr/0020-macos-bench-via-apple-containers.md), turned out to be architecturally Linux-guest-only — Apple's own `container` CLI and `containerization` package only ever boot Linux VMs. There is no macOS-guest mode, and none is on any Apple roadmap. That path is permanently closed, not just waiting on a macOS version.
- Separately, the transitional bare-metal macOS Runner (`gsd-test-local`) runs `node --test` directly on the host Mac with **no memory ceiling**. The Linux and Windows Docker Benches have carried a memory cap since ADR-0021; the bare-metal macOS Runner has no equivalent, so a leaking or fork-bombing test has repeatedly run the host Mac out of memory.

`RuntimeTart` closes both gaps at once: it runs a genuine macOS guest, and that guest carries a hard, hypervisor-enforced memory ceiling.

## What it is, at a glance

`RuntimeTart` boots a real macOS virtual machine on the Bench using Apple's Virtualization.framework, via [Tart](https://github.com/cirruslabs/tart) (Cirrus Labs' CLI wrapper around it). The guest is macOS itself — not a Linux container running on a Mac — so macOS-specific code paths are actually exercised.

Commands run inside the guest over SSH, not `tart exec`. `tart exec` looked like the natural fit (it mirrors `docker exec`'s shape closely), but a live end-to-end test found it fails silently and unreliably against a real headless guest. Cirrus Labs' own reference CI integration (`gitlab-tart-executor`) doesn't use `tart exec` either — it uses SSH with the base image's documented default credentials. `RuntimeTart` follows that same, proven path. See [ADR-0030 Decision 4](adr/0030-macos-bench-via-tart.md) for the full research trail; that trail — the `tart exec` failure investigation, the disk-space false alarm, and so on — isn't repeated here.

Every Tart-backed run sets a hard per-VM memory cap (`tart set --memory`, default 4096 MB) before boot. A runaway process inside the guest is bounded by that allocation and cannot balloon into the host Mac's RAM — the same protection Docker's `--memory` flag has given the Linux and Windows Benches since ADR-0021, now available for a real macOS guest too.

## How it relates to Docker-on-macOS

`RuntimeTart` is additive, not a replacement. `runtime = "docker"` (or an unset `runtime` field) remains the default for macOS Benches — zero extra setup beyond Docker Desktop or colima, at the cost of only exercising Linux behavior. `runtime = "tart"` is the opt-in upgrade: it requires Tart installed on the Bench and a prepared Tart Tester Image, in exchange for genuine macOS-native test coverage and the bare-metal OOM fix. Pick whichever a given Bench needs; both can coexist across your `[[benches]]` list. See [Setting up a macOS Bench](benches.md#setting-up-a-macos-bench) for where this sits alongside the rest of the macOS Bench story.

## Trade-offs and limits

- **No live per-test event streaming.** Unlike the Docker path, a Tart run's test output only surfaces after the whole suite finishes — see the [reference](macos-tart-reference.md#known-limitations) for why.
- **No automated reaping of leaked VMs.** The Linux/Windows Docker Benches have a Tier-2 reaper sweeping stale containers; Tart VMs have no equivalent yet. See the [how-to guide](macos-tart-how-to.md#how-to-clean-up-a-leaked-vm-by-hand) for cleaning one up manually.
- **No exact Node patch pinning.** The bake script installs Node via Homebrew, which floats within a major version — there is no way to pin an exact patch release the way a Docker image tag can.
- **Missing image is a hard failure, not an automatic build.** Unlike Docker Benches, there is no local-build fallback when the Tester Image is absent — see the [reference](macos-tart-reference.md#known-limitations).

None of this blocks adoption — see the [how-to guide](macos-tart-how-to.md) to get a Bench running.
