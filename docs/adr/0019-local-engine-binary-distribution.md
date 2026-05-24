# 0019 — Local Engine binary distribution via GitHub Release assets

Status: Accepted (2026-05-23)

## Context

ADR-0006 mandates a "single static binary per Dev Workstation OS" for the Local Engine and explicitly states "CI cross-compiles all targets from one build job and publishes them to GitHub Releases." v1.0.0 was tagged and pushed without a binary-publishing workflow — macOS and Linux users who did not clone the repo had no way to obtain the binary. This ADR resolves four concrete decisions required before the publishing workflow can be implemented.

The parallel Tester Image workflow (ADR-0005) provides the style reference: `push: tags: 'v*.*.*'` trigger, `workflow_dispatch` for manual re-triggers, GitHub Container Registry for storage. Binary distribution is the same pattern applied to release assets instead of container images.

## Decision

**1. Distribution mechanism: GitHub Release assets.**

Each tagged release (`v*.*.*`) gets pre-built binaries attached as release assets on the GitHub Releases page for this repo. Users install via:

```bash
gh release download v1.0.0 --repo open-gsd/gsd-test-runner --pattern "gsd-test-v1.0.0-darwin-arm64"
chmod +x gsd-test-v1.0.0-darwin-arm64
mv gsd-test-v1.0.0-darwin-arm64 ~/.local/bin/gsd-test
```

Or directly via the public download URL:

```bash
curl -L -o gsd-test https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.0.0-darwin-arm64
chmod +x gsd-test && mv gsd-test ~/.local/bin/
```

Rationale: zero infrastructure beyond GitHub; assets co-locate with release notes, changelogs, and the GHCR-published Tester Images that trigger on the same tag; consistent cross-platform download experience; `gh release download` is scriptable and verifiable. No separate registry, CDN, or hosting account required.

**2. Platform matrix: 5 targets.**

| Binary name | GOOS | GOARCH |
|---|---|---|
| `gsd-test-v<ver>-darwin-amd64` | darwin | amd64 |
| `gsd-test-v<ver>-darwin-arm64` | darwin | arm64 |
| `gsd-test-v<ver>-linux-amd64` | linux | amd64 |
| `gsd-test-v<ver>-linux-arm64` | linux | arm64 |
| `gsd-test-v<ver>-windows-amd64.exe` | windows | amd64 |

`windows/arm64` and `linux/386` are excluded until demand justifies. ADR-0006 commits to "platform-agnostic: macOS, Linux, Windows" without specifying architecture; the five targets listed above cover all common developer workstations in the project's current contributor base.

**3. Build approach: `go build` matrix on `ubuntu-latest` with cross-compilation.**

All five targets are built from a single `ubuntu-latest` runner using Go's native cross-compilation (`GOOS`/`GOARCH` env vars). Build flags:

- `CGO_ENABLED=0` — produces a fully static binary with no libc dependency; satisfies ADR-0006's "no runtime dependencies" requirement.
- `-ldflags="-s -w"` — strips DWARF debug info and symbol table, reducing binary size.
- `-ldflags="-X main.version=v<tag>"` — stamps the version at build time so `gsd-test --version` reports the release tag.
- `-trimpath` — removes local filesystem paths from the binary; improves reproducibility.

The Go toolchain on `ubuntu-latest` supports all five targets without additional cross-compile toolchains (no `arm-linux-gnueabihf` or `mingw` needed; Go ships its own assembler and linker). Each matrix job uploads its binary as a GitHub Actions artifact; the `release` job downloads all artifacts and attaches them to the GitHub Release.

**4. Artifact naming and format: plain binaries, no archives.**

Binaries are named `gsd-test-v<version>-<goos>-<goarch>` (plus `.exe` for Windows). Example: `gsd-test-v1.0.0-darwin-arm64`. Each binary ships with a `<binary-name>.sha256` sidecar for integrity verification. No `.tar.gz` or `.zip` wrapper.

Rationale: the simplest possible install — one `curl` or `gh release download`, one `chmod +x`, one `mv` to `$PATH`. Archives add an extraction step that provides no benefit for a single-file artifact. If checksum-bundle workflows (e.g., signed manifests, cosign attestations) become important in a future release, a `.tar.gz` wrapper can be added at that point without breaking the plain-binary naming convention already adopted by consumers.

## Consequences

+ macOS, Linux, and Windows users obtain the Local Engine binary in one command — no Go toolchain, no `git clone`, no build step required at install time.
+ The publish workflow fires on the same `v*.*.*` tag trigger as the Tester Image workflow (ADR-0005), keeping the two release artifacts in lock-step with no additional coordination.
+ `CGO_ENABLED=0` guarantees no host libc dependency — the darwin-arm64 binary runs on any macOS ≥ 11 without Homebrew or Rosetta.
+ Version stamping via `-X main.version=...` means `gsd-test --version` always reports the exact release tag, enabling scripted installs to verify the correct binary was downloaded.
+ SHA256 sidecars provide a lightweight integrity check without a full signing infrastructure.
+ Single-runner cross-compilation (no per-OS runner) keeps the workflow simple and fast — five binaries in one `ubuntu-latest` job instead of five separate OS runners.
- A GitHub Release publishing pipeline must be maintained. A future re-naming of `cmd/gsd-test` would require updating the workflow's `-o` flag.
- `windows/arm64` is not shipped initially; a Windows ARM developer would need to build from source until demand justifies adding the target.
- Plain binaries (no archives) means consumers who want to verify integrity must download the `.sha256` sidecar separately and run `sha256sum` manually. A future signed manifest (`cosign` SBOM) would improve this.
- Binary size is larger than necessary for debug builds; `-s -w` mitigates but does not eliminate this. `upx` compression was considered and rejected (adds toolchain complexity and triggers some AV scanners).

## Alternatives considered

- **goreleaser** — Rejected for this slice. goreleaser provides checksums, Homebrew formula generation, changelog injection, and signing in a single tool. The added value does not yet justify the configuration overhead and extra dependency for a project at v1.0.0. The plain `go build` matrix achieves the same output; migrating to goreleaser later is a non-breaking change.
- **Homebrew formula** — Rejected as a distribution mechanism (would not preclude it as a secondary channel). Requires maintaining a separate tap repo or getting the formula into homebrew-core (gated by download counts). Adds latency between tag and user-installable release. Not viable for Linux or Windows users.
- **curl-pipe-bash install script** — Rejected: opaque to security-conscious users; does not compose with `gh release download`; harder to version-pin. The direct curl-to-binary approach (with explicit `chmod +x`) is transparent and equally simple.
- **Per-OS GitHub Actions runners (darwin, windows, linux each build their own binary)** — Rejected: three runner types instead of one; macOS runners cost 10× more billing minutes; no benefit over cross-compilation for pure-Go binaries.
- **Pre-built binaries committed to the repo** — Rejected: binary churn in git; balloons repo clone size; not reproducible from source; directly violates ADR-0005's pattern of not shipping pre-built images in git.
- **Separate binary registry (e.g., S3, GCS, Cloudflare R2)** — Rejected: requires additional infrastructure outside GitHub; adds credential management; no benefit over release assets for a project hosted on GitHub.
