package digest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

func TestGroupFailures_DedupAcrossOSes(t *testing.T) {
	f := fail("a.test.js", "suite > x", "assertion", "expected 1, got 2", "", "")
	reps := []report.Report{
		makeReport("linux", 10, 11, f),
		makeReport("windows", 10, 11, f),
	}
	groups := GroupFailures(reps)
	if len(groups) != 1 {
		t.Fatalf("expected 1 unique group, got %d", len(groups))
	}
	if !reflect.DeepEqual(groups[0].Platforms, []string{"linux", "windows"}) {
		t.Errorf("expected platforms [linux windows], got %v", groups[0].Platforms)
	}
	if groups[0].Count != 2 {
		t.Errorf("expected count 2, got %d", groups[0].Count)
	}
}

func TestGroupFailures_VolatileMessageStillGroups(t *testing.T) {
	// Different numeric timeout values must group (Option G normalization).
	reps := []report.Report{
		makeReport("linux", 0, 1, fail("r.test.js", "reaps", "timeout", "timed out after 30000ms", "", "")),
		makeReport("macos", 0, 1, fail("r.test.js", "reaps", "timeout", "timed out after 30017ms", "", "")),
	}
	groups := GroupFailures(reps)
	if len(groups) != 1 {
		t.Fatalf("expected timeouts to group into 1, got %d", len(groups))
	}
}

func TestGroupFailures_DistinctFiles(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 0, 2,
			fail("a.test.js", "x", "assertion", "boom", "", ""),
			fail("b.test.js", "y", "throw", "kaboom", "", ""),
		),
	}
	groups := GroupFailures(reps)
	if len(groups) != 2 {
		t.Fatalf("expected 2 distinct groups, got %d", len(groups))
	}
	// Deterministic sort by file: a before b.
	if groups[0].Key.File != "a.test.js" || groups[1].Key.File != "b.test.js" {
		t.Errorf("groups not sorted by file: %q, %q", groups[0].Key.File, groups[1].Key.File)
	}
}

func TestGroupFailures_DeterministicOrder(t *testing.T) {
	mk := func() []Group {
		reps := []report.Report{
			makeReport("windows", 0, 2,
				fail("z.test.js", "z", "assertion", "z", "", ""),
				fail("a.test.js", "a", "assertion", "a", "", ""),
			),
		}
		return GroupFailures(reps)
	}
	g1, g2 := mk(), mk()
	if !reflect.DeepEqual(keys(g1), keys(g2)) {
		t.Errorf("grouping not deterministic: %v vs %v", keys(g1), keys(g2))
	}
	if g1[0].Key.File != "a.test.js" {
		t.Errorf("expected a.test.js first, got %q", g1[0].Key.File)
	}
}

func keys(gs []Group) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Key.File
	}
	return out
}

func TestNormalizeMessage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Expected 200, got 404", "expected n, got n"},
		{"timed out after 30000ms", "timed out after nms"},
		{"value at 0xDEADBEEF", "value at addr"},
		{"  multiple    spaces  ", "multiple spaces"},
		{"failed at /work/src/foo.js:12:5", "failed at path"},
	}
	for _, tc := range cases {
		if got := normalizeMessage(tc.in); got != tc.want {
			t.Errorf("normalizeMessage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeMessage_AbsPathAnchoringDistinct verifies that tokens which
// contain slashes but are NOT filesystem paths (regex literals, date strings,
// URL route segments without a root-like prefix+separator pattern) are NOT
// collapsed to "path" and therefore keep distinct failures distinct (B-15).
func TestNormalizeMessage_AbsPathAnchoringDistinct(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// shouldCollapse: true if the token is a real abs path and should be masked.
		shouldCollapse bool
	}{
		// Real absolute paths → must collapse.
		{"unix abs path", "error at /foo/bar/baz.js:12", true},
		{"unix abs path with multi-sep", "at /a/b/c", true},
		// NOT paths → must stay distinct (not collapsed to "path").
		{"regex literal single slash", "pattern /foo/ did not match", false},
		{"date YYYY/MM/DD", "failed on 2024/01/02", false},
		{"url route no leading slash", "GET /v1/users returned 404", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeMessage(tc.in)
			containsPath := strings.Contains(got, "path")
			if tc.shouldCollapse && !containsPath {
				t.Errorf("normalizeMessage(%q) = %q; expected abs path to be collapsed to 'path'", tc.in, got)
			}
			if !tc.shouldCollapse && containsPath {
				t.Errorf("normalizeMessage(%q) = %q; non-path token was incorrectly collapsed to 'path' (B-15)", tc.in, got)
			}
		})
	}
}

// TestGroupFailures_DistinctMessagesNotMerged verifies that failures with
// different slash-containing tokens that are NOT abs paths remain as distinct
// groups (B-15).
func TestGroupFailures_DistinctMessagesNotMerged(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 0, 2,
			fail("a.test.js", "x", "assertion", "pattern /foo/ did not match", "", ""),
			fail("a.test.js", "x", "assertion", "date 2024/01/02 invalid", "", ""),
		),
	}
	groups := GroupFailures(reps)
	if len(groups) != 2 {
		t.Errorf("expected 2 distinct groups (regex vs date should NOT merge), got %d (B-15)", len(groups))
	}
}

// TestGroupFailures_SampleDeterministic verifies that the displayed Sample for a
// group (Error, Stack, Output) is the same regardless of input ordering (B-16).
// Two reports with the same GroupKey but different Error/Stack/Output must always
// yield the lexicographically-minimal sample.
func TestGroupFailures_SampleDeterministic(t *testing.T) {
	// Two failures that will group together (same file, name, class, NormMsg
	// after normalization — use fixed volatile parts).
	f1 := fail("a.test.js", "x", "assertion", "expected 1, got 2", "stack-a", "output-a")
	f2 := fail("a.test.js", "x", "assertion", "expected 1, got 2", "stack-b", "output-b")

	// Order 1: f1 first.
	g1 := GroupFailures([]report.Report{
		makeReport("linux", 0, 1, f1),
		makeReport("macos", 0, 1, f2),
	})
	// Order 2: f2 first.
	g2 := GroupFailures([]report.Report{
		makeReport("linux", 0, 1, f2),
		makeReport("macos", 0, 1, f1),
	})

	if len(g1) != 1 || len(g2) != 1 {
		t.Fatalf("expected 1 group each, got %d / %d", len(g1), len(g2))
	}
	if g1[0].Sample.Stack != g2[0].Sample.Stack {
		t.Errorf("Sample.Stack non-deterministic across input orders: %q vs %q (B-16)",
			g1[0].Sample.Stack, g2[0].Sample.Stack)
	}
	if g1[0].Sample.Error != g2[0].Sample.Error {
		t.Errorf("Sample.Error non-deterministic across input orders: %q vs %q (B-16)",
			g1[0].Sample.Error, g2[0].Sample.Error)
	}
	if g1[0].Sample.Output != g2[0].Sample.Output {
		t.Errorf("Sample.Output non-deterministic across input orders: %q vs %q (B-16)",
			g1[0].Sample.Output, g2[0].Sample.Output)
	}
	// The winner must be the lexicographically-minimal one.
	// "stack-a" < "stack-b" → Sample.Stack must be "stack-a".
	if g1[0].Sample.Stack != "stack-a" {
		t.Errorf("expected lex-min Sample.Stack=%q, got %q (B-16)", "stack-a", g1[0].Sample.Stack)
	}
}

func TestDeriveLine(t *testing.T) {
	cases := []struct {
		name, file, stack string
		want              int
	}{
		{"matches file frame", "src/reaper.js", "Error: x\n    at wait (src/reaper.js:88:14)\n    at z (other.js:3:1)", 88},
		{"fallback first frame", "", "Error\n    at foo (bar.js:42:7)", 42},
		{"no line info", "a.js", "Error: just a message", 0},
		{"empty stack", "a.js", "", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveLine(tc.file, tc.stack); got != tc.want {
				t.Errorf("deriveLine = %d, want %d", got, tc.want)
			}
		})
	}
}
