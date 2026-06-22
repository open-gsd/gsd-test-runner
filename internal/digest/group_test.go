package digest

import (
	"reflect"
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
