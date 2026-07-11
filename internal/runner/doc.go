// Package runner owns the multi-OS test-run orchestration: the full lifecycle
// from config loading through verdict emission (ADR-0018 as amended by the
// Node matrix enhancement #108, which collapsed the original 5-phase structure
// into 3: Load → Plan → Schedule, with EnsurePresent folded into each scheduler
// worker because bench assignment is now dynamic/capacity-aware).
//
// The package also exports the shared verdict-emission helpers
// (WriteVerdict, WriteInconclusiveVerdict, WriteRunArtifacts,
// EmitRunDieArtifacts) used by the run-and-die path in cmd/gsd-test.
// The runner is the module that "owns the output contract" (ADR-0023): the
// last line written to Out is always a machine-readable JSON verdict, in every
// mode and every outcome.
package runner
