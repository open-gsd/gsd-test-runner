package runspec

import (
	"errors"
	"reflect"
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
