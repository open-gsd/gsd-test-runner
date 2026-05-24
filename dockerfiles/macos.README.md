# macOS Tester Image

The macOS Tester Image (`ghcr.io/open-gsd/gsd-tester-macos`) is an alias of the Linux Tester Image — it is published by re-tagging `gsd-tester-linux:vX.Y.Z` as `gsd-tester-macos:vX.Y.Z` in the `tag-macos-alias` job of `.github/workflows/publish-tester-images.yml`. No separate Dockerfile exists because the Linux image provides the required container isolation when run on a Mac via Docker Desktop or colima (per ADR-0020, amended 2026-05-24).

If Apple Containers support is added in the future (requires macOS 26 + `macos-26` GH Actions runners), a native macOS Tester Image Dockerfile would be added back here as `macos.containerfile`. The `Runtime` field on `bench.Bench` and the `RuntimeContainer` constant are already in place for that path.

See [docs/adr/0020-macos-bench-via-apple-containers.md](../docs/adr/0020-macos-bench-via-apple-containers.md) for the full design rationale.
