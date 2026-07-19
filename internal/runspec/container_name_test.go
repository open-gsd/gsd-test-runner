package runspec

import (
	"strings"
	"testing"
)

// TestSlugifyBranch_Cases covers the slug rules: lowercase, illegal-char
// replacement, run collapsing, leading/trailing trim, and the empty-fallback
// sentinel (a non-empty result is required for Docker --name).
func TestSlugifyBranch_Cases(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercases", "Feature/Foo", "feature-foo"},
		{"slash to dash", "fix/2329-opencode-commands-dir", "fix-2329-opencode-commands-dir"},
		{"preserves dot and underscore, slash still dashes", "feat/v1.2_beta", "feat-v1.2_beta"},
		{"preserves literal dot", "release.v1", "release.v1"},
		{"preserves literal underscore", "bug_fix", "bug_fix"},
		{"collapses illegal run", "fix///foo", "fix-foo"},
		{"trims leading trailing dash", "---fix/foo---", "fix-foo"},
		{"all-illegal becomes sentinel", "!!!", "branch"},
		{"empty becomes sentinel", "", "branch"},
		{"whitespace becomes sentinel", "   ", "branch"},
		{"unicode letters preserved lowercased", "Café", "caf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slugifyBranch(tt.in); got != tt.want {
				t.Errorf("slugifyBranch(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSlugifyBranch_NeverEmpty asserts the invariant the Docker --name
// argument depends on: slugifyBranch always returns a non-empty string.
func TestSlugifyBranch_NeverEmpty(t *testing.T) {
	cases := []string{"", "   ", "!!!", "---", "/", ".", "_", "---/---"}
	for _, in := range cases {
		if got := slugifyBranch(in); got == "" {
			t.Errorf("slugifyBranch(%q) returned empty; want non-empty sentinel", in)
		}
	}
}

// TestSpec_BranchSlug_PrefersPRBranch verifies the resolution order: PRBranch
// wins over Base, Base is the fallback, both-empty yields "unknown".
func TestSpec_BranchSlug_PrefersPRBranch(t *testing.T) {
	if got := (Spec{PRBranch: "fix/foo", Base: "main"}).BranchSlug(); got != "fix-foo" {
		t.Errorf("PRBranch set: BranchSlug = %q, want fix-foo", got)
	}
	if got := (Spec{Base: "main"}).BranchSlug(); got != "main" {
		t.Errorf("only Base: BranchSlug = %q, want main", got)
	}
	if got := (Spec{}).BranchSlug(); got != "unknown" {
		t.Errorf("neither set: BranchSlug = %q, want unknown", got)
	}
}

// TestSpec_ContainerName_Format checks the happy-path shape.
func TestSpec_ContainerName_Format(t *testing.T) {
	s := Spec{PRBranch: "fix/2329-opencode-commands-dir", RunID: "550e8400-e29b-41d4-a716-446655440000"}
	got := s.ContainerName()
	// Prefix + slug + 8-char runId tail.
	if !strings.HasPrefix(got, "gsd-test-fix-2329-opencode-commands-dir-") {
		t.Errorf("ContainerName = %q; want prefix gsd-test-fix-2329-opencode-commands-dir-", got)
	}
	tail := strings.TrimPrefix(got, "gsd-test-fix-2329-opencode-commands-dir-")
	if tail != "550e8400" {
		t.Errorf("ContainerName tail = %q, want 550e8400 (8-char runId head)", tail)
	}
}

// TestSpec_ContainerName_LengthCeiling asserts the Docker name limit. Docker
// container names effectively cap at 63 characters (hostname-style). We assert
// the boundary at limit, limit-1, and limit+1.
func TestSpec_ContainerName_LengthCeiling(t *testing.T) {
	const ceiling = 63
	runID := "550e8400-e29b-41d4-a716-446655440000" // realistic UUID
	// Build a branch long enough to force truncation. Slug is branch with - separators.
	branch := "fix/" + strings.Repeat("a", 80)
	got := (Spec{PRBranch: branch, RunID: runID}).ContainerName()
	if len(got) > ceiling {
		t.Errorf("ContainerName len = %d, want <= %d (got %q)", len(got), ceiling, got)
	}
	if !strings.HasSuffix(got, "-550e8400") {
		t.Errorf("ContainerName = %q; want suffix -550e8400 (runId tail preserved on truncation)", got)
	}
}

// TestSpec_ContainerName_ShortRunID is the boundary case where RunID is shorter
// than the standard 8-char tail: the whole RunID is used.
func TestSpec_ContainerName_ShortRunID(t *testing.T) {
	got := (Spec{PRBranch: "fix/foo", RunID: "ab"}).ContainerName()
	want := "gsd-test-fix-foo-ab"
	if got != want {
		t.Errorf("ContainerName short RunID = %q, want %q", got, want)
	}
}

// TestSpec_ContainerName_StaysUnderCeilingForAnyBranch is a property-style
// assertion that no branch input can produce an oversize name.
func TestSpec_ContainerName_StaysUnderCeilingForAnyBranch(t *testing.T) {
	const ceiling = 63
	branches := []string{
		"",
		"a",
		strings.Repeat("b", 200),
		"//" + strings.Repeat("c/.", 100),
		strings.Repeat("UPPER", 50),
	}
	runID := "abcdef12-3456-7890-abcd-ef1234567890"
	for _, b := range branches {
		got := (Spec{PRBranch: b, RunID: runID}).ContainerName()
		if len(got) > ceiling {
			t.Errorf("branch %q produced name %q len %d > %d", b, got, len(got), ceiling)
		}
		if len(got) < len("gsd-test-x-xxxxxxxx") {
			t.Errorf("branch %q produced suspiciously short name %q", b, got)
		}
	}
}

// TestSpec_ContainerName_DistinctForDistinctSpecs is the independence
// assertion: two runs of the same branch with different RunIDs must produce
// different names so concurrent runs don't collide on `docker create`.
func TestSpec_ContainerName_DistinctForDistinctSpecs(t *testing.T) {
	a := Spec{PRBranch: "fix/foo", RunID: "11111111-0000-0000-0000-000000000000"}
	b := Spec{PRBranch: "fix/foo", RunID: "22222222-0000-0000-0000-000000000000"}
	if a.ContainerName() == b.ContainerName() {
		t.Errorf("distinct specs produced same name %q; runId tail must disambiguate", a.ContainerName())
	}
}
