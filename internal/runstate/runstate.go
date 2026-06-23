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
	"sort"
	"strings"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// ErrTraversal is returned when a runID would escape the runs store directory
// via path traversal (B-5, defense-in-depth).
var ErrTraversal = errors.New("runstate: run-id escapes the store directory (path traversal detected)")

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

// containmentCheck verifies that joining dir and a runID-derived suffix does
// not escape dir. It is defense-in-depth (B-5): the primary gate is
// runspec.validate(), but raw filepath.Join is vulnerable to paths containing
// ".." even after the charset gate. Returns ErrTraversal when the resolved path
// is not rooted under dir.
func containmentCheck(dir, suffix string) error {
	resolved := filepath.Clean(filepath.Join(dir, suffix))
	// filepath.Rel returns an error or a ".." path when resolved is outside dir.
	rel, err := filepath.Rel(dir, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ErrTraversal
	}
	return nil
}

// Path returns the absolute path to the state file for runID.
func Path(runID string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := containmentCheck(dir, runID+".json"); err != nil {
		return "", err
	}
	return filepath.Join(dir, runID+".json"), nil
}

// RunDir returns the per-run artifact directory for runID: <Dir()>/<runID>.
// It is a sibling of the <runID>.json state file written by Save and reuses the
// same XDG resolution as Dir(). Failure-first run artifacts (FAILURES.md,
// failures.json, per-failure files, junit, the verdict) are written here
// (issue #84, ADR-0023).
func RunDir(runID string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := containmentCheck(dir, runID); err != nil {
		return "", err
	}
	return filepath.Join(dir, runID), nil
}

// EnsureRunDir creates (MkdirAll 0o755) and returns the per-run artifact
// directory for runID. Idempotent: a second call is a no-op.
func EnsureRunDir(runID string) (string, error) {
	dir, err := RunDir(runID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("runstate: create run dir %s: %w", dir, err)
	}
	return dir, nil
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

// Release deletes all on-disk artifacts for runID: the <runID>.json state file,
// the <runID>/ artifact dir, and the <runID>.worker.log if present. Used by the
// ephemeral consume-on-read path (#102 Option B) and Prune. Containment-checked
// like Path/RunDir. Missing files are not an error (idempotent).
func Release(runID string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}

	var errs []error

	// Remove the .json state file.
	jsonPath, err := Path(runID)
	if err != nil {
		return err
	}
	if err := os.Remove(jsonPath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("runstate: release %s json: %w", runID, err))
	}

	// Remove the artifact directory.
	runDir, err := RunDir(runID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(runDir); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("runstate: release %s dir: %w", runID, err))
	}

	// Remove the worker log.
	workerLog := runID + ".worker.log"
	if err := containmentCheck(dir, workerLog); err != nil {
		return err
	}
	workerLogPath := filepath.Join(dir, workerLog)
	if err := os.Remove(workerLogPath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("runstate: release %s worker.log: %w", runID, err))
	}

	return errors.Join(errs...)
}

// PruneOptions controls Prune behavior.
type PruneOptions struct {
	TTL          time.Duration // 0 = no TTL bound
	KeepLastRuns int           // 0 = no count bound
	Now          time.Time     // injectable for tests; zero value = time.Now().UTC()
}

// Prune enforces retention on the runs store (#102 Option C). It groups on-disk
// entries by run-id (<id>.json state, <id>/ dir, <id>.worker.log), and removes
// runs that are older than TTL or beyond the KeepLastRuns newest. A run whose
// state file has Status == StatusRunning is NEVER pruned (an in-flight async run
// from a concurrent invocation). Returns the number of runs removed. Best-effort
// per run: a failure to remove one run does not stop the others (errors joined).
func Prune(opts PruneOptions) (int, error) {
	dir, err := Dir()
	if err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("runstate: prune readdir %s: %w", dir, err)
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// Group entries by runID.
	type runEntry struct {
		runID   string
		entries []os.DirEntry
	}
	runMap := make(map[string]*runEntry)

	for _, e := range entries {
		name := e.Name()
		// Skip hidden/temp files (e.g. .runstate-tmp-*).
		if strings.HasPrefix(name, ".") {
			continue
		}

		var runID string
		switch {
		case strings.HasSuffix(name, ".json"):
			runID = strings.TrimSuffix(name, ".json")
		case strings.HasSuffix(name, ".worker.log"):
			runID = strings.TrimSuffix(name, ".worker.log")
		default:
			// Plain entry (directory) — the name itself is the runID.
			runID = name
		}

		// Defense-in-depth: skip non-conforming names.
		if !runspec.ValidRunID(runID) {
			continue
		}

		if runMap[runID] == nil {
			runMap[runID] = &runEntry{runID: runID}
		}
		runMap[runID].entries = append(runMap[runID].entries, e)
	}

	// For each runID, determine: running? and timestamp.
	type runInfo struct {
		runID     string
		running   bool
		timestamp time.Time
	}

	infos := make([]runInfo, 0, len(runMap))
	for runID, re := range runMap {
		info := runInfo{runID: runID}

		// Check if running via state file.
		st, loadErr := Load(runID)
		if loadErr == nil {
			if st.Status == StatusRunning {
				info.running = true
			}
			info.timestamp = st.UpdatedAt
		}

		// If we couldn't load the state (missing or corrupt), fall back to newest
		// ModTime among the runID's entries.
		if info.timestamp.IsZero() {
			for _, e := range re.entries {
				fi, fiErr := e.Info()
				if fiErr != nil {
					continue
				}
				if mt := fi.ModTime(); mt.After(info.timestamp) {
					info.timestamp = mt
				}
			}
		}

		infos = append(infos, info)
	}

	// Build the prunable set = all non-running runs.
	type candidate struct {
		runID     string
		timestamp time.Time
	}
	var prunable []candidate
	for _, info := range infos {
		if !info.running {
			prunable = append(prunable, candidate{runID: info.runID, timestamp: info.timestamp})
		}
	}

	// Mark for removal by TTL first.
	markedForRemoval := make(map[string]bool)
	if opts.TTL > 0 {
		cutoff := now.Add(-opts.TTL)
		for _, c := range prunable {
			if c.timestamp.Before(cutoff) {
				markedForRemoval[c.runID] = true
			}
		}
	}

	// Then apply KeepLastRuns to the survivors.
	if opts.KeepLastRuns > 0 {
		// Collect survivors (not yet marked for removal).
		survivors := make([]candidate, 0, len(prunable))
		for _, c := range prunable {
			if !markedForRemoval[c.runID] {
				survivors = append(survivors, c)
			}
		}
		// Sort survivors newest first.
		sort.Slice(survivors, func(i, j int) bool {
			return survivors[i].timestamp.After(survivors[j].timestamp)
		})
		// Mark everything beyond KeepLastRuns for removal.
		for i := opts.KeepLastRuns; i < len(survivors); i++ {
			markedForRemoval[survivors[i].runID] = true
		}
	}

	// Remove each marked run.
	var errs []error
	removed := 0
	for runID := range markedForRemoval {
		if err := Release(runID); err != nil {
			errs = append(errs, err)
		} else {
			removed++
		}
	}

	return removed, errors.Join(errs...)
}
