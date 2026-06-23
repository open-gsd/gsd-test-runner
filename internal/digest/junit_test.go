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

func TestJUnitFromReports_ControlCharsProduceValidXML(t *testing.T) {
	// XML 1.0 forbids control characters U+0000–U+001F except U+0009, U+000A,
	// U+000D. ESC (\x1b), NUL (\x00), and BEL (\x07) are all invalid. We embed
	// them in the test Name, Error message, and Stack to confirm the produced
	// document is well-formed and that none of these bytes survive raw.
	const (
		esc = "\x1b"
		nul = "\x00"
		bel = "\x07"
	)
	poison := "boom" + esc + "[31mred" + nul + bel
	rep := makeReport("linux", 0, 1,
		fail(
			"ctrl"+esc+"chars.test.js", // Name field (file)
			"test"+nul+"name",          // name
			"assertion",
			poison, // error message
			"at line 1"+bel+"\nat line 2"+esc+"[0m", // stack
			"",
		),
	)

	out, err := JUnitFromReports([]report.Report{rep})
	if err != nil {
		t.Fatalf("JUnitFromReports returned error: %v", err)
	}

	// Assertion 1: output must be well-formed XML.
	var v any
	if xmlErr := xml.Unmarshal(out, &v); xmlErr != nil {
		t.Errorf("well-formedness FAIL: xml.Unmarshal returned error: %v\noutput:\n%s", xmlErr, out)
	}

	// Assertion 2: no raw invalid byte (ESC, NUL, BEL) must survive in the output.
	for _, b := range []byte{0x1b, 0x00, 0x07} {
		if strings.ContainsRune(string(out), rune(b)) {
			t.Errorf("raw invalid byte 0x%02x survives in JUnit output; want stripped/escaped\noutput:\n%s", b, out)
		}
	}

	// Assertion 3: U+FFFD (replacement character from invalid UTF-8 path) must
	// also be absent from the sanitized output (the control chars were stripped,
	// not replaced).
	if strings.ContainsRune(string(out), '�') {
		t.Errorf("U+FFFD replacement character found in JUnit output; control bytes should be stripped, not replaced\noutput:\n%s", out)
	}
}

// TestJUnitFromReports_InvalidUTF8Sanitized verifies that invalid UTF-8 bytes in
// failure fields are coerced (not silently corrupted to U+FFFD clusters in the
// XML output) (B-13).
func TestJUnitFromReports_InvalidUTF8Sanitized(t *testing.T) {
	// \x80 alone is not a valid UTF-8 start byte.
	invalidUTF8 := "boom\x80baz"
	rep := makeReport("linux", 0, 1, fail("a.test.js", "x", "assertion", invalidUTF8, invalidUTF8, ""))
	out, err := JUnitFromReports([]report.Report{rep})
	if err != nil {
		t.Fatalf("JUnitFromReports: %v", err)
	}
	// Must be well-formed XML.
	var v any
	if xmlErr := xml.Unmarshal(out, &v); xmlErr != nil {
		t.Errorf("invalid UTF-8 input produced malformed XML: %v\noutput:\n%s", xmlErr, out)
	}
	// The raw invalid byte must not survive.
	if strings.ContainsRune(string(out), rune(0x80)) {
		t.Errorf("raw invalid UTF-8 byte 0x80 survived in JUnit output\noutput:\n%s", out)
	}
}

// TestJUnitFromReports_ZeroDurationOmitsTimeAttr verifies that a failure with
// DurationMs==0 does not emit a time attribute (B-19 dead omitempty fix).
func TestJUnitFromReports_ZeroDurationOmitsTimeAttr(t *testing.T) {
	rep := makeReport("linux", 0, 1, fail("a.test.js", "x", "timeout", "timed out", "", ""))
	// DurationMs defaults to 0 in the fail() helper.
	out, err := JUnitFromReports([]report.Report{rep})
	if err != nil {
		t.Fatalf("JUnitFromReports: %v", err)
	}
	outStr := string(out)
	if strings.Contains(outStr, `time="0.000"`) {
		t.Errorf("zero-duration failure must not emit time=0.000 (dead omitempty); output:\n%s", outStr)
	}
	if strings.Contains(outStr, `time="`) {
		t.Errorf("zero-duration failure must not emit any time= attribute; output:\n%s", outStr)
	}
}

// TestJUnitFromReports_NonZeroDurationEmitsTimeAttr verifies that a failure with
// a real DurationMs emits the time attribute correctly.
func TestJUnitFromReports_NonZeroDurationEmitsTimeAttr(t *testing.T) {
	f := fail("a.test.js", "x", "timeout", "timed out", "", "")
	f.DurationMs = 1500.0
	rep := makeReport("linux", 0, 1, f)
	out, err := JUnitFromReports([]report.Report{rep})
	if err != nil {
		t.Fatalf("JUnitFromReports: %v", err)
	}
	if !strings.Contains(string(out), `time="1.500"`) {
		t.Errorf("non-zero duration must emit time=1.500; output:\n%s", out)
	}
}
