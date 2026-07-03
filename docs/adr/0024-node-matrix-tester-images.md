# 0024 — Node-matrix Tester Images: node-suffixed tags + node-major sentinel

Status: Accepted (2026-07-03)

## Context

Enhancement #108 requires testing against more than one Node.js major version per OS. Every Tester Image up to this point was published one-per-OS at a fixed, implicit Node major baked into the Dockerfile. ADR-0011 established the `sh.gsd-test.image-version` OCI label as the forgery-resistant version sentinel and the tag-then-verify contract `Pipeline.CheckImageVersion` relies on. Adding a Node dimension to the image matrix forces four concrete decisions that ADR-0011 did not anticipate:

1. **How does one Dockerfile produce images for multiple Node majors?** A build arg, separate Dockerfiles per major, or a base-image matrix.
2. **How does the image name/tag encode the Node major** so a Bench (and a human) can tell `gsd-tester-linux:v1.5.0` built for Node 22 apart from the same version built for Node 24?
3. **Does the existing `sh.gsd-test.image-version` sentinel change shape**, or does the Node major get its own sentinel?
4. **How does CI (`publish-tester-images.yml`) build and tag the matrix without hardcoding the supported Node set** in workflow logic, so the supported-LTS-set can change without a CI rewrite?

Getting this right once matters because the fan-out scheduler (ADR-0025) picks images by (OS, Node major) at dispatch time — a new dimension per pipeline run, not just per release.

## Decision

**1. One Dockerfile per OS, parameterized by `ARG NODE_VERSION` (default `22`).** Each Tester Image Dockerfile (`gsd-tester-linux`, `gsd-tester-windows`, and the macOS alias) accepts `ARG NODE_VERSION=22` and installs that Node major as its runtime. No separate Dockerfile-per-major, no separate base-image matrix — one file, one build-arg axis. Local fallback builds (ADR-0012) pass `--build-arg NODE_VERSION=<major>` so a locally-built fallback image bakes the correct major for whichever cell dispatched it.

**2. Tag convention: `<image>:<version>-node<major>`**, e.g. `ghcr.io/open-gsd/gsd-tester-linux:v1.5.0-node22`. Rejected: separate image repositories per major (`gsd-tester-linux-node22`) — that multiplies image names across every OS × Node combination and complicates every place a Bench or config references "the Linux image." A tag suffix composes with the existing `<image>:<version>` scheme with a single new segment and keeps one image name per OS.

**3. New companion label `sh.gsd-test.node-major=<major>`; `sh.gsd-test.image-version` stays un-suffixed.** The image-version label continues to carry the plain release version (`v1.5.0`), matching what `[versions]` in `config.toml` already expects and what `Pipeline.CheckImageVersion` already verifies (ADR-0011 dec 1). The Node major is a second, independent label rather than folded into the version string — `CheckImageVersion` does not need to change, and any future consumer that only cares about release version (not Node major) keeps working unmodified. The node suffix lives in exactly two places: the image tag, and the new label — never in the version sentinel itself.

**4. CI matrix is `strategy.matrix.node: ["22", "24"]`, driven off `config.DefaultNodeLTS()`; `DEFAULT_NODE_MAJOR=24` gets the back-compat plain tags.** `publish-tester-images.yml` builds linux + windows + the macOS alias across the Node matrix. The non-default majors (`22`) get only their `-node<major>` tags. The default major (`24`, the current Active LTS) additionally gets the plain `:<version>` and `:latest` tags, so every existing Bench/config referencing the un-suffixed tag keeps resolving to a working image with no migration required. The supported set itself (22 + 24 as of 2026-07; 20 reaches EOL 2026-04-30; 26 enters LTS 2026-10-28, per the [Node.js Release schedule](https://github.com/nodejs/Release)) is data, not workflow logic: it lives in the `[node]` config table and `config.DefaultNodeLTS()`, and changing it in CI is a one-line matrix edit, never new conditional steps.

Windows resolves the latest patch for its selected major from `nodejs.org/dist/index.json` at build time, so the image always ships the newest patch of that major rather than a stale pinned patch.

## Consequences

+ A single Dockerfile per OS stays the unit of maintenance; adding Node 26 later is a matrix-array edit plus a build, not a new Dockerfile.
+ Existing Benches and configs that reference the plain `<image>:<version>` tag are unaffected — that tag keeps resolving to the Active LTS build.
+ `Pipeline.CheckImageVersion` requires zero changes: the image-version label's shape and meaning are untouched by this ADR.
+ The Node major is independently queryable via `docker image inspect` without parsing the tag string, giving the scheduler (ADR-0025) a structured signal instead of a string-split on the tag.
+ The supported-LTS-set living in config + a matrix array (not workflow conditionals) means dropping Node 20 or adding Node 26 touches no CI script logic, only data.
- Every non-default Node major now requires an explicit `-node<major>` reference wherever an image is named (Bench pre-pull instructions, fallback build args). A config or doc that assumes the plain tag always means "the only Node version" is now wrong — this ADR and the accompanying doc updates make that explicit.
- The image count published per release grows linearly with the Node matrix size (2 majors × 3 OS today = 6 images instead of 3). GHCR storage and CI build time both scale with the matrix.
- Windows resolving the latest patch at build time means two builds of the same `(version, major)` on different days can produce different patch-level Node binaries inside the image, even though the tag is identical. Acceptable: patch-level Node differences are not expected to change test outcomes, and pinning further would require a manual patch-bump step per release that this design avoids.

## Alternatives considered

- **Separate image repository per Node major (`gsd-tester-linux-node22`, `gsd-tester-linux-node24`)** — Rejected: multiplies image names across every OS × Node combination; every config, doc, and Bench setup step doubles.
- **Fold Node major into the existing image-version label (e.g. `v1.5.0+node22`)** — Rejected: would require rewriting `Pipeline.CheckImageVersion`'s comparison and every `[versions]` config entry; also conflates two independent axes (release version, Node major) into one string that then needs parsing everywhere it's consumed.
- **Separate Dockerfile per Node major** — Rejected: duplicates every other Dockerfile instruction (base image, tool installs, entrypoint) across N files; a change to shared setup steps needs N edits instead of one.
- **Hardcode the supported Node set directly in `publish-tester-images.yml`'s matrix with no config-side source of truth** — Rejected: the CI matrix and the runtime `[node]` default would drift independently; `config.DefaultNodeLTS()` is the single place both CI (via a generation step) and runtime consult.
- **No back-compat plain tag; require every Bench to switch to `-node<major>` tags immediately** — Rejected: forces a synchronized config migration across every Bench the moment this ships. Keeping the Active LTS on the plain tag makes this release non-breaking for existing single-Node-version setups.
