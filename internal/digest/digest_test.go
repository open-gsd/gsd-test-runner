package digest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// --- shared fixtures ---

func fail(file, name, errClass, msg, stack, output string) report.FailedTest {
	return report.FailedTest{
		File:       file,
		Name:       name,
		Error:      msg,
		ErrorClass: report.ErrorClass(errClass),
		Stack:      stack,
		Output:     output,
	}
}

func makeReport(os string, passed, total int, fails ...report.FailedTest) report.Report {
	r := report.New(os, "bench-"+os, "img:"+os, "v1", time.Unix(0, 0).UTC())
	r.Passed = passed
	r.Total = total
	r.Failed = len(fails)
	r.Failures = fails
	r.Finalize(time.Unix(0, 0).UTC())
	return r
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1700000000, 0).UTC() }
}

// --- tests ---

func TestWriteDigest_Deterministic(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 5, 7, fail("a.test.js", "x", "assertion", "boom", "at a (a.test.js:3:1)", "out")),
		makeReport("windows", 6, 7, fail("a.test.js", "x", "assertion", "boom", "at a (a.test.js:3:1)", "out")),
	}
	opts := WriteOpts{Now: fixedClock(), PerFailureFiles: true}

	d1, d2 := t.TempDir(), t.TempDir()
	if _, err := WriteDigest(d1, reps, opts); err != nil {
		t.Fatalf("WriteDigest d1: %v", err)
	}
	if _, err := WriteDigest(d2, reps, opts); err != nil {
		t.Fatalf("WriteDigest d2: %v", err)
	}
	for _, name := range []string{"failures.json", "FAILURES.md", "junit.xml", "failures/INDEX.md"} {
		b1 := mustRead(t, filepath.Join(d1, name))
		b2 := mustRead(t, filepath.Join(d2, name))
		if string(b1) != string(b2) {
			t.Errorf("%s not deterministic:\n--- d1 ---\n%s\n--- d2 ---\n%s", name, b1, b2)
		}
	}
}

func TestWriteDigest_FailuresJSONRoundTrip(t *testing.T) {
	bigStack := strings.Repeat("frame\n", 200) // exceeds the default line cap
	reps := []report.Report{
		makeReport("linux", 1, 2, fail("a.test.js", "x", "timeout", "timed out", bigStack, "captured")),
	}
	dir := t.TempDir()
	if _, err := WriteDigest(dir, reps, WriteOpts{Now: fixedClock()}); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}

	var doc FailuresDoc
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "failures.json")), &doc); err != nil {
		t.Fatalf("failures.json does not round-trip: %v", err)
	}
	if doc.SchemaVersion != DigestSchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, DigestSchemaVersion)
	}
	if len(doc.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(doc.Failures))
	}
	// failures.json must keep the FULL untruncated stack (it is the "full at" target).
	if doc.Failures[0].Stack != bigStack {
		t.Errorf("failures.json truncated the stack; want full %d bytes, got %d", len(bigStack), len(doc.Failures[0].Stack))
	}
	if doc.Summary.Outcome != string(report.OutcomeFailed) {
		t.Errorf("outcome = %q, want failed", doc.Summary.Outcome)
	}
}

func TestWriteDigest_MarkdownTruncationPointer(t *testing.T) {
	bigStack := strings.Repeat("frame\n", 200)
	reps := []report.Report{makeReport("linux", 0, 1, fail("a.test.js", "x", "throw", "boom", bigStack, ""))}
	dir := t.TempDir()
	if _, err := WriteDigest(dir, reps, WriteOpts{Now: fixedClock()}); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	md := string(mustRead(t, filepath.Join(dir, "FAILURES.md")))
	if !strings.HasPrefix(md, "# Test failures — FAILED") {
		t.Errorf("FAILURES.md missing headline, got:\n%s", firstLines(md, 3))
	}
	// RFC-6901: FailuresDoc.Failures marshals under the top-level "failures" key,
	// so the pointer must be /failures/<idx>/<field>, NOT /<idx>/<field>.
	wantPtr := "full at failures.json#/failures/0/stack"
	if !strings.Contains(md, "truncated") || !strings.Contains(md, wantPtr) {
		t.Errorf("FAILURES.md missing or malformed truncation pointer (want %q):\n%s", wantPtr, md)
	}
}

// TestWriteDigest_TruncationPointerResolvesRFC6901 verifies that the pointer
// emitted in FAILURES.md is a valid RFC-6901 JSON Pointer that resolves back to
// the full text stored in failures.json.
func TestWriteDigest_TruncationPointerResolvesRFC6901(t *testing.T) {
	// Build a stack large enough to trigger truncation.
	const lineCount = 200
	bigStack := strings.Repeat("frame line\n", lineCount)
	reps := []report.Report{makeReport("linux", 0, 1, fail("a.test.js", "x", "throw", "boom", bigStack, "big output\n"+strings.Repeat("out\n", 200)))}
	dir := t.TempDir()
	if _, err := WriteDigest(dir, reps, WriteOpts{Now: fixedClock()}); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}

	// Read failures.json and unmarshal.
	var doc FailuresDoc
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "failures.json")), &doc); err != nil {
		t.Fatalf("failures.json parse: %v", err)
	}

	// Manually resolve RFC-6901 pointers for the first failure's stack and output.
	// The pointer form is /failures/<idx>/<field>. idx is 0-based (f.Index-1).
	if len(doc.Failures) == 0 {
		t.Fatal("no failures in failures.json")
	}
	entry := doc.Failures[0]

	// Confirm the pointer arithmetic: index 0 in the failures array.
	if entry.Index != 1 {
		t.Fatalf("expected Index=1, got %d", entry.Index)
	}

	// The emitted pointer is failures.json#/failures/0/stack.
	// Resolve it: doc.Failures[0].Stack must equal the full bigStack.
	resolvedStack := doc.Failures[0].Stack
	if resolvedStack != bigStack {
		t.Errorf("RFC-6901 /failures/0/stack resolved to %d bytes, want %d (pointer broken)",
			len(resolvedStack), len(bigStack))
	}

	// The emitted pointer for output is failures.json#/failures/0/output.
	resolvedOutput := doc.Failures[0].Output
	if resolvedOutput == "" {
		t.Errorf("RFC-6901 /failures/0/output is empty; want non-empty (pointer broken)")
	}

	// Confirm FAILURES.md actually contains the correct pointer string.
	md := string(mustRead(t, filepath.Join(dir, "FAILURES.md")))
	for _, wantPtr := range []string{"failures.json#/failures/0/stack", "failures.json#/failures/0/output"} {
		if !strings.Contains(md, wantPtr) {
			t.Errorf("FAILURES.md does not contain RFC-6901 pointer %q", wantPtr)
		}
	}
	// Confirm OLD broken form is absent.
	for _, badPtr := range []string{"failures.json#/0/stack", "failures.json#/0/output"} {
		if strings.Contains(md, badPtr) {
			t.Errorf("FAILURES.md still contains malformed (non-RFC-6901) pointer %q", badPtr)
		}
	}
}

// TestSummarize_PerOSAggregation verifies that two reports sharing the same OS
// key are aggregated (not overwritten) in PerOS, and that the per-OS counts are
// internally consistent with TotalFailures (B-2).
func TestSummarize_PerOSAggregation(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 3, 5, fail("a.test.js", "x", "assertion", "boom1", "", ""), fail("a.test.js", "y", "assertion", "boom2", "", "")),
		makeReport("linux", 1, 4, fail("b.test.js", "z", "timeout", "timed out", "", "")),
	}
	groups := GroupFailures(reps)
	now := time.Unix(1700000000, 0).UTC()
	s := Summarize(reps, groups, now)

	// TotalFailures must be sum of both reports' Failed counts.
	if s.TotalFailures != 3 {
		t.Errorf("TotalFailures = %d, want 3", s.TotalFailures)
	}

	// PerOS must have exactly one key (both reports share "linux").
	if len(s.PerOS) != 1 {
		t.Errorf("len(PerOS) = %d, want 1", len(s.PerOS))
	}

	linux, ok := s.PerOS["linux"]
	if !ok {
		t.Fatal("PerOS[linux] missing")
	}
	// Aggregated: Passed=3+1=4, Failed=2+1=3, Total=5+4=9
	if linux.Passed != 4 {
		t.Errorf("linux.Passed = %d, want 4", linux.Passed)
	}
	if linux.Failed != 3 {
		t.Errorf("linux.Failed = %d, want 3 (was overwriting instead of aggregating)", linux.Failed)
	}
	if linux.Total != 9 {
		t.Errorf("linux.Total = %d, want 9", linux.Total)
	}
	// TotalFailures must equal sum of all PerOS Failed values.
	var perOSFailed int
	for _, c := range s.PerOS {
		perOSFailed += c.Failed
	}
	if perOSFailed != s.TotalFailures {
		t.Errorf("PerOS failed sum = %d, TotalFailures = %d; inconsistent", perOSFailed, s.TotalFailures)
	}
	// Outcome: worst-of both reports; both are OutcomeFailed.
	if s.Outcome != string(report.OutcomeFailed) {
		t.Errorf("Outcome = %q, want failed", s.Outcome)
	}
}

// TestSummarize_PerOSWorstOfOutcome verifies that the aggregated OSCount.Outcome
// is the worst-of across same-OS reports (B-2).
func TestSummarize_PerOSWorstOfOutcome(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 2, 3, fail("a.test.js", "x", "assertion", "boom", "", "")),
	}
	// Manually set outcomes on the two reports.
	reps[0].Outcome = report.OutcomeFailed

	// Add a second linux report with infra_error.
	rep2 := makeReport("linux", 0, 1, fail("b.test.js", "y", "assertion", "kaboom", "", ""))
	rep2.Outcome = report.OutcomeInfraError
	reps = append(reps, rep2)

	s := Summarize(reps, GroupFailures(reps), time.Unix(0, 0).UTC())

	linux := s.PerOS["linux"]
	// infra_error is worse than failed → PerOS["linux"].Outcome must be infra_error.
	if linux.Outcome != string(report.OutcomeInfraError) {
		t.Errorf("linux.Outcome = %q, want infra_error (worst-of)", linux.Outcome)
	}
}

func TestWriteDigest_PassRun(t *testing.T) {
	reps := []report.Report{makeReport("linux", 7, 7)}
	dir := t.TempDir()
	paths, err := WriteDigest(dir, reps, WriteOpts{Now: fixedClock(), PerFailureFiles: true})
	if err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	md := string(mustRead(t, filepath.Join(dir, "FAILURES.md")))
	if !strings.Contains(md, "PASSED") || !strings.Contains(md, "No failures") {
		t.Errorf("pass-run FAILURES.md unexpected:\n%s", md)
	}
	// No failures → no per-failure dir.
	if paths.FailuresDir != "" {
		t.Errorf("expected no failures dir on a pass run, got %q", paths.FailuresDir)
	}
	if _, err := os.Stat(filepath.Join(dir, "failures")); !os.IsNotExist(err) {
		t.Errorf("failures/ dir should not exist on a pass run")
	}
}

func TestWriteDigest_PerFailureFiles(t *testing.T) {
	reps := []report.Report{
		makeReport("linux", 0, 2,
			fail("a.test.js", "alpha", "assertion", "a", "", ""),
			fail("b.test.js", "beta", "throw", "b", "", ""),
		),
	}
	dir := t.TempDir()
	if _, err := WriteDigest(dir, reps, WriteOpts{Now: fixedClock(), PerFailureFiles: true}); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "failures"))
	if err != nil {
		t.Fatalf("read failures dir: %v", err)
	}
	var mdFiles, hasIndex int
	for _, e := range entries {
		if e.Name() == "INDEX.md" {
			hasIndex++
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") {
			mdFiles++
		}
	}
	if hasIndex != 1 {
		t.Errorf("expected INDEX.md, got %d", hasIndex)
	}
	if mdFiles != 2 {
		t.Errorf("expected 2 per-failure files (one per unique failure), got %d", mdFiles)
	}
	if _, err := os.Stat(filepath.Join(dir, "failures", "01-a-test-js-alpha.md")); err != nil {
		t.Errorf("expected deterministic slug file 01-a-test-js-alpha.md: %v", err)
	}
}

// TestWriteDigest_IndexMDOmissionMarker verifies that INDEX.md appends an
// "… N more omitted" marker when the number of unique failures exceeds MaxEntries
// (mirrors renderFailuresMD behaviour, B-17).
func TestWriteDigest_IndexMDOmissionMarker(t *testing.T) {
	// Create MaxEntries+3 distinct failures.
	const max = 5
	var fails []report.FailedTest
	for i := range max + 3 {
		fails = append(fails, fail(
			fmt.Sprintf("file%02d.test.js", i), "x", "assertion", "boom", "", "",
		))
	}
	reps := []report.Report{makeReport("linux", 0, len(fails), fails...)}
	dir := t.TempDir()
	opts := WriteOpts{Now: fixedClock(), PerFailureFiles: true, MaxEntries: max}
	if _, err := WriteDigest(dir, reps, opts); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	idx := string(mustRead(t, filepath.Join(dir, "failures", "INDEX.md")))
	// Header must still say the full unique count.
	if !strings.Contains(idx, fmt.Sprintf("%d unique", max+3)) {
		t.Errorf("INDEX.md header missing full unique count:\n%s", idx)
	}
	// Must have an omission marker for the 3 truncated entries.
	wantMarker := "3 more omitted"
	if !strings.Contains(idx, wantMarker) {
		t.Errorf("INDEX.md missing omission marker %q (B-17):\n%s", wantMarker, idx)
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ file, name, want string }{
		{"a.test.js", "suite > does X", "a-test-js-suite-does-x"},
		{"", "", "failure"},
		{strings.Repeat("x", 80), "y", strings.Repeat("x", 60)},
	}
	for _, tc := range cases {
		if got := slugify(tc.file, tc.name); got != tc.want {
			t.Errorf("slugify(%q,%q) = %q, want %q", tc.file, tc.name, got, tc.want)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
