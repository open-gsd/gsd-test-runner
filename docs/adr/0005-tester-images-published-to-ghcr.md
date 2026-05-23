# 0005 — Tester Images are published to GHCR; in-repo Dockerfile is the fallback build path

Status: Accepted (2026-05-22)

## Context

Per ADR-0001, Tester Images are the deployment unit. They must be distributable without committing binary blobs to the repo and without re-introducing the per-host build drift documented in docker-test-runner-issues.md §4.2. Two real consumers of the Dockerfile exist: CI (building the published image) and the developer's local fallback build (when GHCR auth is unavailable or the developer prefers offline-first). Two adapters define a real seam.

The Dockerfile must therefore remain a first-class artifact in the repo — tested by CI on every release, not a secondary convenience script. If the Dockerfile drifts from what CI builds, the fallback path diverges from the canonical image, recreating a variant of the §4.2 drift problem.

## Decision

This repo's CI builds Tester Images and publishes them to GitHub Container Registry on tagged releases (`ghcr.io/<org>/gsd-tester-<os>:<tag>`). The Local Engine's default behavior is to pull the tag matching this repo checkout's expected version. If the pull fails or GHCR auth is unavailable, the Local Engine falls back to building from the in-repo Dockerfile — the same Dockerfile CI uses, so byte-for-equivalent outcomes are achievable. The Dockerfile lives in the repo; built images do not.

## Consequences

+ No binary churn in git — only the Dockerfile is versioned.
+ Per-host drift (§4.2) eliminated for GHCR-using developers — all Hosts pull the same tagged image.
+ One-time GHCR auth setup per developer (`gh auth token | docker login ghcr.io ...`).
+ The Dockerfile remains a real, tested artifact — CI builds from it on every release, fallback builds use the same source.
- A network dependency exists for first-pull on each Host.
- The CI publish step is a new piece of infrastructure to maintain.
- A developer using the local-build fallback re-introduces per-host drift relative to GHCR-using developers (acceptable: rare path, opt-in).

## Alternatives considered

- Local build only — Rejected: this is the transitional pattern. Recreates §4.2 drift.
- Publish to Docker Hub — Rejected: GHCR is co-located with the repo, uses GitHub auth, no separate account needed.
- Ship pre-built tarballs in GitHub Releases — Rejected: requires manual `docker load` per Host; loses Docker's layer caching benefits.
