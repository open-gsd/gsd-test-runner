// Package runstate persists per-run state for async dispatch
// (ADR-0022 Decision 3, issue #70). One JSON file is written per run under
// $XDG_STATE_HOME/gsd-test/runs/ (falling back to ~/.local/state/gsd-test/runs/).
// Writes are atomic (temp-file + rename) so a concurrent reader of wait/status
// never observes a partial file.
package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// Status values for State.Status (ADR-0022 Decision 3).
const (
	StatusRunning = "running"
	StatusDone    = "done"
)

// ErrNotFound is returned by Load when the run-id has no state file.
var ErrNotFound = errors.New("runstate: run not found")

// State is the persisted per-run state envelope.
type State struct {
	RunID     string       `json:"run_id"`
	Target    string       `json:"target"`
	Repo      string       `json:"repo"`
	Status    string       `json:"status"`
	PID       int          `json:"pid"`
	StartedAt time.Time    `json:"started_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Spec      runspec.Spec `json:"spec"`

	// Report is nil until the run is done.
	Report *report.Report `json:"report,omitempty"`

	// ExitCode is the runrender exit code, set when Status == StatusDone.
	ExitCode int `json:"exit_code"`

	// Err is set (with Status=done, ExitCode=2) when dispatch was inconclusive.
	Err string `json:"err,omitempty"`
}

// Dir returns the directory where per-run state files are stored.
// It follows the XDG Base Directory Specification, mirroring telemetry.RepoLogPath:
//
//	$XDG_STATE_HOME/gsd-test/runs/
//
// falling back to ~/.local/state/gsd-test/runs/ when XDG_STATE_HOME is unset.
func Dir() (string, error) {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("runstate: home dir: %w", err)
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "gsd-test", "runs"), nil
}

// Path returns the absolute path to the state file for runID.
func Path(runID string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runID+".json"), nil
}

// Save writes st to its state file atomically. The directory is created if it
// does not exist. Atomicity is achieved by writing to a temp file in the same
// directory and then calling os.Rename so a concurrent Load never sees a
// half-written file.
func Save(st State) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("runstate: create dir %s: %w", dir, err)
	}

	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("runstate: marshal state: %w", err)
	}

	// Write to a temp file in the same directory so os.Rename is atomic
	// (same filesystem, rename = atomic swap on POSIX).
	tmp, err := os.CreateTemp(dir, ".runstate-tmp-*")
	if err != nil {
		return fmt.Errorf("runstate: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up on failure.
	defer func() {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("runstate: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("runstate: close temp file: %w", err)
	}
	tmp = nil // prevent deferred cleanup from removing after rename

	target := filepath.Join(dir, st.RunID+".json")
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath) // best-effort cleanup on rename failure
		return fmt.Errorf("runstate: rename to %s: %w", target, err)
	}
	return nil
}

// Load reads and unmarshals the state file for runID. If the file does not
// exist, it returns ErrNotFound (callers should use errors.Is to test for it).
func Load(runID string) (State, error) {
	path, err := Path(runID)
	if err != nil {
		return State{}, err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, fmt.Errorf("runstate: %s: %w", runID, ErrNotFound)
		}
		return State{}, fmt.Errorf("runstate: read %s: %w", path, err)
	}

	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("runstate: unmarshal %s: %w", path, err)
	}
	return st, nil
}
