package digest

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

func TestJUnitFromReports_RoundTrip(t *testing.T) {
	reps := []report.Report{
		makeReport("windows", 4, 5, fail("b.test.js", "beta", "throw", "kaboom", "at b (b.test.js:9:2)", "")),
		makeReport("linux", 5, 7,
			fail("a.test.js", "alpha", "assertion", "expected 1, got 2", "at a (a.test.js:3:1)", ""),
			fail("a.test.js", "gamma", "timeout", "timed out", "", ""),
		),
	}
	xmlBytes, err := JUnitFromReports(reps)
	if err != nil {
		t.Fatalf("JUnitFromReports: %v", err)
	}

	var root junitTestsuites
	if err := xml.Unmarshal(xmlBytes, &root); err != nil {
		t.Fatalf("junit does not round-trip: %v\n%s", err, xmlBytes)
	}
	if root.Tests != 12 { // 7 linux + 5 windows
		t.Errorf("testsuites tests = %d, want 12", root.Tests)
	}
	if root.Failures != 3 {
		t.Errorf("testsuites failures = %d, want 3", root.Failures)
	}
	// Suites are sorted by OS (deterministic): linux before windows.
	if len(root.Suites) != 2 || root.Suites[0].Name != "linux" || root.Suites[1].Name != "windows" {
		t.Fatalf("suites not sorted by OS: %+v", root.Suites)
	}
	linux := root.Suites[0]
	if linux.Tests != 7 || linux.Failures != 2 || len(linux.Cases) != 2 {
		t.Errorf("linux suite wrong: %+v", linux)
	}
	if linux.Cases[0].Failure == nil || linux.Cases[0].Failure.Type != "assertion" {
		t.Errorf("expected assertion failure on first linux case: %+v", linux.Cases[0])
	}
	if linux.Cases[0].Classname != "a.test.js" {
		t.Errorf("classname = %q, want a.test.js", linux.Cases[0].Classname)
	}
}

func TestJUnitFromReports_Deterministic(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 5, 6, fail("a.test.js", "x", "assertion", "boom", "", "")),
		makeReport("windows", 6, 6),
	}
	a, _ := JUnitFromReports(reps)
	// Reverse the input order — output must be identical (suites sorted by OS).
	b, _ := JUnitFromReports([]report.Report{reps[1], reps[0]})
	if string(a) != string(b) {
		t.Errorf("junit not deterministic across input order:\n%s\n---\n%s", a, b)
	}
}

func TestJUnitFromReports_PassRun(t *testing.T) {
	xmlBytes, err := JUnitFromReports([]report.Report{makeReport("linux", 7, 7)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xmlBytes), `failures="0"`) {
		t.Errorf("pass run should report failures=0:\n%s", xmlBytes)
	}
	if strings.Contains(string(xmlBytes), "<failure") {
		t.Errorf("pass run should have no <failure> elements:\n%s", xmlBytes)
	}
}
