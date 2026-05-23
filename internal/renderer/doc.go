// Package renderer consumes the pipeline.Event channel and renders
// human-readable progress + final Per-OS Reports to a writer (typically
// os.Stdout for the report banners and os.Stderr for live progress).
//
// Print-as-they-finish per
// docs/adr/0009-local-engine-top-level-orchestration.md. Each event is
// labeled with its OS so parallel pipelines interleave legibly.
package renderer
