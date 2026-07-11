//go:build !unix

package main

import "errors"

// realSpawn is a stub for non-unix platforms (ADR-0022 Decision 3, issue #70).
// Async mode requires POSIX process-group semantics; use a unix workstation.
func realSpawn(runID, configPath string) (int, error) {
	return 0, errors.New("gsd-test: async mode requires a unix workstation")
}

// realWorkerPIDAlive always returns false on non-unix platforms. Exposed via
// the package-level workerPIDAlive seam (ADR-0028).
func realWorkerPIDAlive(pid int) bool { return false }
