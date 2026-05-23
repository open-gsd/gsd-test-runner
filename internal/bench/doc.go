// Package bench models a Bench: a remote SSH-reachable machine that runs
// containerized test suites on behalf of a Dev Workstation.
//
// See CONTEXT.md (Target vocabulary: Bench) and
// docs/adr/0007-dev-workstation-vs-bench-vocabulary.md.
//
// Bench selection (policy: round-robin, pinned, exclude-list) lives in
// this package per docs/adr/0008-per-os-pipeline-executor-shape.md
// (decision: Bench selection lives outside the Pipeline Executor).
package bench
