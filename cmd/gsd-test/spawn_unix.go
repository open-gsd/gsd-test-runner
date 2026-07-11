//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/open-gsd/gsd-test-runner/internal/runstate"
)

// realSpawn is the unix implementation of spawnFunc (ADR-0022 Decision 3,
// issue #70). It launches a detached worker process that runs __run-worker
// for the given runID. The worker is fully detached (new process group, no
// inherited stdin/stdout/stderr) so the calling agent session can exit
// without killing the run.
func realSpawn(runID, configPath string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("spawn: locate executable: %w", err)
	}

	// Redirect the worker's stdout/stderr to a per-run log file so diagnostics
	// survive after the parent process exits.
	dir, err := runstate.Dir()
	if err != nil {
		return 0, fmt.Errorf("spawn: runstate dir: %w", err)
	}
	logPath := filepath.Join(dir, runID+".worker.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Non-fatal: fall back to /dev/null so the worker still runs.
		logFile, _ = os.Open(os.DevNull)
	}

	args := []string{"__run-worker", "--run-id", runID, "--config", configPath}
	cmd := exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("spawn: start worker: %w", err)
	}
	pid := cmd.Process.Pid
	// Do not cmd.Wait() — we want the worker to outlive us.
	// Close our reference to the log file; the worker owns the fd now.
	logFile.Close()
	return pid, nil
}

// realWorkerPIDAlive reports whether a process with the given pid is still
// alive. It uses kill(pid, 0) which does not send a signal but checks
// reachability. ESRCH means the process does not exist; treat any other error
// conservatively as alive (e.g. EPERM — process exists but we lack permission
// to signal it). Exposed via the package-level workerPIDAlive seam (ADR-0028).
func realWorkerPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}
