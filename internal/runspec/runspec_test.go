package runspec

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func ptrInt64(v int64) *int64 { return &v }

func TestParse_MinimalValid_AppliesDefaults(t *testing.T) {
	spec, err := Parse([]byte(`{"repo":"/work","target":"linux"}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if spec.Repo != "/work" {
		t.Errorf("Repo = %q, want %q", spec.Repo, "/work")
	}
	if spec.Target != "linux" {
		t.Errorf("Target = %q, want %q", spec.Target, "linux")
	}
	if want := []string{"node", "--test"}; !reflect.DeepEqual(spec.TestCommand, want) {
		t.Errorf("TestCommand = %v, want %v", spec.TestCommand, want)
	}
	if spec.Budget.OverrunFactor != 1.5 {
		t.Errorf("Budget.OverrunFactor = %v, want 1.5", spec.Budget.OverrunFactor)
	}
	if spec.Budget.HardCapMs != 3600000 {
		t.Errorf("Budget.HardCapMs = %d, want 3600000", spec.Budget.HardCapMs)
	}
	if spec.Isolation != IsolationProcess {
		t.Errorf("Isolation = %q, want %q", spec.Isolation, IsolationProcess)
	}
}

func TestParse_ExplicitValues_Preserved(t *testing.T) {
	spec, err := Parse([]byte(`{
		"repo": "/work",
		"target": "windows",
		"testCommand": ["npm", "test"],
		"testPathPatterns": ["test/**/*.test.js"],
		"env": {"CI": "1"},
		"budget": {"estimateMs": 120000, "overrunFactor": 2.0, "hardCapMs": 600000},
		"isolation": "none"
	}`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if want := []string{"npm", "test"}; !reflect.DeepEqual(spec.TestCommand, want) {
		t.Errorf("TestCommand = %v, want %v", spec.TestCommand, want)
	}
	if want := []string{"test/**/*.test.js"}; !reflect.DeepEqual(spec.TestPathPatterns, want) {
		t.Errorf("TestPathPatterns = %v, want %v", spec.TestPathPatterns, want)
	}
	if spec.Env["CI"] != "1" {
		t.Errorf("Env[CI] = %q, want %q", spec.Env["CI"], "1")
	}
	if spec.Budget.EstimateMs == nil || *spec.Budget.EstimateMs != 120000 {
		t.Errorf("Budget.EstimateMs = %v, want 120000", spec.Budget.EstimateMs)
	}
	if spec.Budget.OverrunFactor != 2.0 {
		t.Errorf("Budget.OverrunFactor = %v, want 2.0", spec.Budget.OverrunFactor)
	}
	if spec.Budget.HardCapMs != 600000 {
		t.Errorf("Budget.HardCapMs = %d, want 600000", spec.Budget.HardCapMs)
	}
	if spec.Isolation != IsolationNone {
		t.Errorf("Isolation = %q, want %q", spec.Isolation, IsolationNone)
	}
}

func TestParse_TargetRejected(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"missing", `{"repo":"/work"}`},
		{"unknown", `{"repo":"/work","target":"solaris"}`},
		{"empty", `{"repo":"/work","target":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json))
			var invalid *InvalidSpecError
			if !errors.As(err, &invalid) {
				t.Fatalf("Parse error = %v, want *InvalidSpecError", err)
			}
			if invalid.Field != "target" {
				t.Errorf("InvalidSpecError.Field = %q, want %q", invalid.Field, "target")
			}
		})
	}
}

func TestParse_FieldRejected(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		wantField string
	}{
		{"missing repo", `{"target":"linux"}`, "repo"},
		{"empty repo", `{"repo":"","target":"linux"}`, "repo"},
		{"overrunFactor below 1", `{"repo":"/work","target":"linux","budget":{"overrunFactor":0.5}}`, "budget.overrunFactor"},
		{"negative estimateMs", `{"repo":"/work","target":"linux","budget":{"estimateMs":-5}}`, "budget.estimateMs"},
		{"zero estimateMs", `{"repo":"/work","target":"linux","budget":{"estimateMs":0}}`, "budget.estimateMs"},
		{"unknown isolation", `{"repo":"/work","target":"linux","isolation":"sandbox"}`, "isolation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json))
			var invalid *InvalidSpecError
			if !errors.As(err, &invalid) {
				t.Fatalf("Parse error = %v, want *InvalidSpecError", err)
			}
			if invalid.Field != tt.wantField {
				t.Errorf("InvalidSpecError.Field = %q, want %q", invalid.Field, tt.wantField)
			}
		})
	}
}

func TestParse_MalformedJSON_Errors(t *testing.T) {
	_, err := Parse([]byte(`{not json`))
	if err == nil {
		t.Fatal("Parse returned nil error for malformed JSON")
	}
	var invalid *InvalidSpecError
	if errors.As(err, &invalid) {
		t.Errorf("malformed JSON should not be an *InvalidSpecError, got %v", err)
	}
}

func TestNewRunID_UniqueAndFormatted(t *testing.T) {
	a, err := NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	b, err := NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	if a == b {
		t.Errorf("NewRunID returned duplicate ids: %q", a)
	}
	// RFC-4122 v4: 8-4-4-4-12 hex, version nibble '4', length 36.
	if len(a) != 36 || a[14] != '4' {
		t.Errorf("NewRunID format = %q, want 36-char v4 uuid", a)
	}
}

func TestBudget_EffectiveDeadline_EstimateWithinCap(t *testing.T) {
	est := int64(120000)
	b := Budget{EstimateMs: &est, OverrunFactor: 1.5, HardCapMs: 3600000}
	if got := b.EffectiveDeadlineMs(0); got != 180000 {
		t.Errorf("EffectiveDeadlineMs = %d, want 180000", got)
	}
}

func TestBudget_EffectiveDeadline_Cases(t *testing.T) {
	est := func(v int64) *int64 { return &v }
	tests := []struct {
		name         string
		budget       Budget
		telemetryMed int64
		want         int64
	}{
		{
			name:   "clamped to hard cap when estimate*factor exceeds it",
			budget: Budget{EstimateMs: est(3000000), OverrunFactor: 1.5, HardCapMs: 3600000},
			want:   3600000,
		},
		{
			name:         "telemetry median used when no estimate",
			budget:       Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
			telemetryMed: 100000,
			want:         150000,
		},
		{
			name:   "no estimate and no telemetry falls back to hard cap",
			budget: Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
			want:   3600000,
		},
		{
			name:   "tiny estimate floored to minimum",
			budget: Budget{EstimateMs: est(1000), OverrunFactor: 1.5, HardCapMs: 3600000},
			want:   30000,
		},
		{
			name:   "custom overrun factor respected",
			budget: Budget{EstimateMs: est(100000), OverrunFactor: 3.0, HardCapMs: 3600000},
			want:   300000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.budget.EffectiveDeadlineMs(tt.telemetryMed); got != tt.want {
				t.Errorf("EffectiveDeadlineMs = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParse_BaseAndPRBranch(t *testing.T) {
	spec, err := Parse([]byte(`{"repo":"/src","target":"linux","base":"main","prBranch":"feat/x"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if spec.Base != "main" || spec.PRBranch != "feat/x" {
		t.Errorf("base/prBranch = %q/%q, want main/feat/x", spec.Base, spec.PRBranch)
	}
}

func TestParse_OnlyOneOfBasePRBranchRejected(t *testing.T) {
	for _, body := range []string{
		`{"repo":"/src","target":"linux","base":"main"}`,
		`{"repo":"/src","target":"linux","prBranch":"feat/x"}`,
	} {
		_, err := Parse([]byte(body))
		var invalid *InvalidSpecError
		if !errors.As(err, &invalid) {
			t.Errorf("Parse(%s) err = %v, want *InvalidSpecError", body, err)
		}
	}
}

func TestParse_TelemetryField(t *testing.T) {
	spec, err := Parse([]byte(`{"repo":"/w","target":"linux","telemetry":{"sampleHandlesMs":5000,"captureStacks":true}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if spec.Telemetry.SampleHandlesMs != 5000 || !spec.Telemetry.CaptureStacks {
		t.Errorf("Telemetry = %+v, want {5000 true}", spec.Telemetry)
	}
}

func TestParse_NegativeSampleHandlesRejected(t *testing.T) {
	_, err := Parse([]byte(`{"repo":"/w","target":"linux","telemetry":{"sampleHandlesMs":-1}}`))
	var invalid *InvalidSpecError
	if !errors.As(err, &invalid) || invalid.Field != "telemetry.sampleHandlesMs" {
		t.Errorf("err = %v, want InvalidSpecError on telemetry.sampleHandlesMs", err)
	}
}

// ── B-5: RunID validation ─────────────────────────────────────────────────────

// TestParse_TraversalRunIDRejected verifies that Parse rejects RunID values
// that could cause path-traversal in the runs store (B-5, security).
func TestParse_TraversalRunIDRejected(t *testing.T) {
	cases := []struct {
		name  string
		runID string
	}{
		{"dotdot", "../../../../etc/passwd"},
		{"slash", "run/id"},
		{"space", "run id"},
		{"too long", strings.Repeat("a", 129)},
		{"backslash in segment", `run\id`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{
				"repo":   "/work",
				"target": "linux",
				"runId":  tc.runID,
			})
			_, err := Parse(body)
			var inv *InvalidSpecError
			if !errors.As(err, &inv) {
				t.Fatalf("Parse(runId=%q): expected *InvalidSpecError, got %v", tc.runID, err)
			}
			if inv.Field != "runId" {
				t.Errorf("InvalidSpecError.Field = %q, want %q", inv.Field, "runId")
			}
		})
	}
}

// TestValidRunID verifies the exported ValidRunID helper (B-5).
func TestValidRunID(t *testing.T) {
	want := map[string]bool{
		"abc":                    true,
		"run-001":                true,
		"run_001":                true,
		strings.Repeat("a", 128): true,
		"":                       false,
		strings.Repeat("a", 129): false,
		"../../../../etc/passwd": false,
		"run/id":                 false,
		"run id":                 false,
	}
	for id, wantOK := range want {
		if got := ValidRunID(id); got != wantOK {
			t.Errorf("ValidRunID(%q) = %v, want %v", id, got, wantOK)
		}
	}
}
