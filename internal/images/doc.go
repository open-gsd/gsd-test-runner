// Package images owns Tester Image acquisition on a Bench: GHCR pull
// primary, in-repo Dockerfile build fallback. Also owns the shared
// image-policy modules used by both execution engines (ADR-0027):
// images.Ref encodes the ADR-0024 tag convention, and
// images.VerifyImageVersion is the single version-sentinel check used by
// both the Pipeline engine's CheckImageVersion leg and the Watchdog
// engine's pre-run check.
//
// See docs/adr/0001-tester-image-is-released-sandbox.md and
// docs/adr/0005-tester-images-published-to-ghcr.md.
//
// Two-adapter shape (GHCR pull + Dockerfile build) is intentional per
// the "two adapters = real seam" principle.
package images
