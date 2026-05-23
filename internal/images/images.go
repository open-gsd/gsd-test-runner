package images

// ImageID is a fully-qualified Tester Image reference, typically
// "ghcr.io/open-gsd/gsd-tester-<os>:<tag>" (ADR-0005) or a local
// tag like "gsd-tester-linux:dev" when built from the in-repo
// Dockerfile fallback.
type ImageID string
