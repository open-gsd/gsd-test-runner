// Package plan builds a per-run execution plan: which Benches will run
// which OSes, which targeted OSes are being skipped via
// --allow-skip-os, and any unreachable Benches that should abort the
// run before any Pipeline starts.
//
// Fail-loud-at-planning-time discipline per
// docs/adr/0009-local-engine-top-level-orchestration.md (default
// fail-loud, opt-in skip behavior).
package plan
