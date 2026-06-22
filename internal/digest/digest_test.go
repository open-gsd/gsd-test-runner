package digest

import (
	"encoding/json"
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
	for _, name := range []string{"failures.json", "FAILURES.md", "failures/INDEX.md"} {
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
	if !strings.Contains(md, "truncated") || !strings.Contains(md, "full at failures.json#/0/stack") {
		t.Errorf("FAILURES.md missing truncation pointer:\n%s", md)
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
