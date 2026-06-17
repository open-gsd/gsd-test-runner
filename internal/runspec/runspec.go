// Package runspec parses and validates the JSON "run spec" an agent submits
// to the Local Engine in place of invoking node directly (see issue #60 and
// ADR-0021). Parse is pure: it applies defaults and validates, but never
// assigns a RunID or touches the filesystem — the submit command owns those.
package runspec

import (
	"encoding/json"
	"fmt"
)

// InvalidSpecError reports a run spec that failed validation. It names the
// offending field so the failure is loud and specific (ADR-0004), mirroring
// config.InvalidConfigError.
type InvalidSpecError struct {
	Field  string
	Reason string
}

func (e *InvalidSpecError) Error() string {
	return fmt.Sprintf("invalid run spec at %s: %s", e.Field, e.Reason)
}

// validTargets is the set of OS targets a run spec may name; mirrors the
// Bench.OS values accepted by internal/config.
var validTargets = map[string]bool{
	"linux":           true,
	"windows":         true,
	"macos-container": true,
}

// Isolation selects the Node test-runner isolation mode (ADR-0021 Decision 5).
type Isolation string

const (
	// IsolationProcess runs each test file in its own child process (Node's
	// default). A wedged file is a contained child the watchdog can reap with
	// precise per-test attribution.
	IsolationProcess Isolation = "process"
	// IsolationNone runs all tests in the shared runner process: faster, but a
	// hang wedges the whole runner and reaper attribution degrades.
	IsolationNone Isolation = "none"
)

// Default budget values per ADR-0021 Decision 1.
const (
	defaultOverrunFactor = 1.5
	defaultHardCapMs     = 3600000 // 1h absolute ceiling
)

// Budget bounds a run's wall-clock (ADR-0021 Decision 1). The effective
// deadline is min(EstimateMs*OverrunFactor, HardCapMs), armed at the start of
// the RunTests leg.
type Budget struct {
	// EstimateMs is the agent's estimate of expected test-run duration. Nil
	// means "no estimate" — the reaper falls back to the telemetry median, or
	// to HardCapMs when no telemetry exists.
	EstimateMs    *int64  `json:"estimateMs"`
	OverrunFactor float64 `json:"overrunFactor"`
	HardCapMs     int64   `json:"hardCapMs"`
}

// Spec is a validated run spec.
type Spec struct {
	RunID            string            `json:"runId"`
	Repo             string            `json:"repo"`
	Target           string            `json:"target"`
	TestCommand      []string          `json:"testCommand"`
	TestPathPatterns []string          `json:"testPathPatterns"`
	Env              map[string]string `json:"env"`
	Budget           Budget            `json:"budget"`
	Isolation        Isolation         `json:"isolation"`
	Concurrency      *int              `json:"concurrency"`
}

// Parse unmarshals a JSON run spec, applies ADR-0021 defaults, and returns the
// validated Spec.
func Parse(data []byte) (*Spec, error) {
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, err
	}

	if len(spec.TestCommand) == 0 {
		spec.TestCommand = []string{"node", "--test"}
	}
	if spec.Budget.OverrunFactor == 0 {
		spec.Budget.OverrunFactor = defaultOverrunFactor
	}
	if spec.Budget.HardCapMs == 0 {
		spec.Budget.HardCapMs = defaultHardCapMs
	}
	if spec.Isolation == "" {
		spec.Isolation = IsolationProcess
	}

	if err := spec.validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (s *Spec) validate() error {
	if !validTargets[s.Target] {
		return &InvalidSpecError{Field: "target", Reason: fmt.Sprintf("must be one of linux, windows, macos-container; got %q", s.Target)}
	}
	if s.Repo == "" {
		return &InvalidSpecError{Field: "repo", Reason: "must be a non-empty path to the run payload"}
	}
	if s.Budget.OverrunFactor < 1.0 {
		return &InvalidSpecError{Field: "budget.overrunFactor", Reason: fmt.Sprintf("must be >= 1.0; got %v", s.Budget.OverrunFactor)}
	}
	if s.Budget.EstimateMs != nil && *s.Budget.EstimateMs <= 0 {
		return &InvalidSpecError{Field: "budget.estimateMs", Reason: fmt.Sprintf("must be > 0 when set; got %d", *s.Budget.EstimateMs)}
	}
	if s.Isolation != IsolationProcess && s.Isolation != IsolationNone {
		return &InvalidSpecError{Field: "isolation", Reason: fmt.Sprintf("must be \"process\" or \"none\"; got %q", s.Isolation)}
	}
	return nil
}
