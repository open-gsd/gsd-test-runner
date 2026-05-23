# 0001 — Tester Image is the released sandbox, not a code snapshot

Status: Accepted (2026-05-22)

## Context

The transitional architecture builds Docker images per-host manually (`docker build` on each of plex2, holodeck, cartographer, redshirt). This produced per-host image drift: docker-test-runner-issues.md §4.2 documents plex2 at 307 MB vs the other three at 108 MB — same Dockerfile, different build moments, diverged base layer digests. The transitional `INSTALL_GSD_TEST.sh` also went stale relative to the deployed `~/.local/bin/gsd-test` (§2.4), proving that any artifact requiring manual sync across hosts will drift.

PRs frequently modify tests themselves. Baking project code into the image is therefore impossible — the tests added in a PR cannot be present in the image they are supposed to be tested by. The image and the code under test must be decoupled.

These two facts together — per-host build drift and the impossibility of code-in-image — define what the Tester Image must and must not contain.

## Decision

The Tester Image holds only the test-execution sandbox: base OS, Node 22, build toolchain, the Reporter at a known in-image path, sandbox config (HOME=/home/gsdtest mode 1777 per §2.3 lesson), and an Image-version sentinel. Project code and tests arrive per-run via the PR-merged worktree (see ADR-0002). One image per supported OS, versioned by tag.

## Consequences

+ Per-host drift becomes structurally impossible — all Hosts pull the same tagged image.
+ The HOME/UID/permissions lessons from §2.1–§2.3 live inside the image, not in wrapper flags.
+ Image-version sentinel makes "stale image" a loud, immediate failure (ADR-0004).
- A new image version is required when the sandbox itself changes (Node bump, base OS update, sandbox config tweak). Distinct from PR cadence.
- The Local Engine must verify the image version on every run.

## Alternatives considered

- Bake project code into per-PR images — Rejected: every PR would need an image build; tests added in the PR couldn't be in the image they themselves are tested by.
- Keep building per-host — Rejected: this is precisely the drift class §4.2 documents.
