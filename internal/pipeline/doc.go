// Package pipeline implements the Per-OS Pipeline Executor: one
// instance per (Bench, OS) per Local Engine run. Owns 8 of the 11
// pipeline legs from ADR-0004: image-version sentinel check, copy-in,
// container start, npm ci, build, test run, JSONL drain, parse.
//
// Shape per docs/adr/0008-per-os-pipeline-executor-shape.md:
//   - Step chain (one method per leg, plus RunAll)
//   - Structured event stream via chan<- Event
//   - LegError envelope wrapping typed Cause errors
//   - Bench accepted at construction (selection lives in package bench)
package pipeline
