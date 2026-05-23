// Package images owns Tester Image acquisition on a Bench: GHCR pull
// primary, in-repo Dockerfile build fallback. Also owns the
// Image-version sentinel check.
//
// See docs/adr/0001-tester-image-is-released-sandbox.md and
// docs/adr/0005-tester-images-published-to-ghcr.md.
//
// Two-adapter shape (GHCR pull + Dockerfile build) is intentional per
// the "two adapters = real seam" principle. The exact interface
// (where the sentinel check lives, the failure envelope when both
// paths fail) is the open question deepening candidate #2 will close
// in a future ADR.
package images
