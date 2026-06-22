package digest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// DigestSchemaVersion is the schema version of failures.json. It is independent
// of report.SchemaVersion (which governs the frozen Report shape, ADR-0013).
const DigestSchemaVersion = 1

// DefaultMaxEntries caps how many per-failure blocks/files are rendered; the
// summary still counts every failure.
const DefaultMaxEntries = 100

// OSCount is the per-OS pass/fail tally used by the digest and the verdict.
type OSCount struct {
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Total   int    `json:"total"`
	Outcome string `json:"outcome"`
}

// Summary is the top-level headline of the digest and verdict.
type Summary struct {
	Outcome        string             `json:"outcome"` // worst-of across reports
	PerOS          map[string]OSCount `json:"per_os"`
	TotalFailures  int                `json:"total_failures"`
	UniqueFailures int                `json:"unique_failures"`
	GeneratedAt    time.Time          `json:"generated_at"`
}

// FailureEntry is one unique failure (a Group) in failures.json. The Error,
// Stack, and Output fields carry the FULL untruncated text — failures.json is
// the "full at …" target the capped FAILURES.md points back into.
type FailureEntry struct {
	Index     int               `json:"index"` // 1-based, matches the FAILURES.md block
	Class     report.ErrorClass `json:"class"`
	File      string            `json:"file"`
	Line      int               `json:"line"` // best-effort; 0 if unknown
	Name      string            `json:"name"`
	Platforms []string          `json:"platforms"`
	Error     string            `json:"error"`
	Stack     string            `json:"stack"`
	Output    string            `json:"output"`
}

// FailuresDoc is the failures.json document.
type FailuresDoc struct {
	SchemaVersion int            `json:"schema_version"`
	Summary       Summary        `json:"summary"`
	Failures      []FailureEntry `json:"failures"`
}

// WriteOpts tunes WriteDigest. The zero value is valid (defaults + no
// per-failure files + wall-clock time).
type WriteOpts struct {
	Cap             CapOpts          // bounds error/stack/output blocks in the markdown
	MaxEntries      int              // <=0 → DefaultMaxEntries
	PerFailureFiles bool             // Option D: also write failures/NN-<slug>.md + INDEX.md
	Now             func() time.Time // injectable clock for deterministic tests
}

func (o WriteOpts) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o WriteOpts) maxEntries() int {
	if o.MaxEntries <= 0 {
		return DefaultMaxEntries
	}
	return o.MaxEntries
}

// Paths are the artifact paths WriteDigest produces (absolute, under dir).
type Paths struct {
	Dir          string
	FailuresJSON string
	FailuresMD   string
	FailuresDir  string // failures/ (Option D); empty string when not written
	IndexMD      string // failures/INDEX.md (Option D)
	JUnitXML     string // set by the caller after junit is drained (Option H)
	EventsJSONL  string // set by the caller after the JSONL is persisted (Option B)
}

// severity orders outcomes so the aggregate "worst-of" is well defined.
func severity(o string) int {
	switch report.Outcome(o) {
	case report.OutcomeInfraError:
		return 3
	case report.OutcomeReaped:
		return 2
	case report.OutcomeFailed:
		return 1
	default: // passed / empty
		return 0
	}
}

// Summarize builds the headline from the reports and their grouping.
func Summarize(reps []report.Report, groups []Group, now time.Time) Summary {
	s := Summary{
		Outcome:     string(report.OutcomePassed),
		PerOS:       make(map[string]OSCount, len(reps)),
		GeneratedAt: now.UTC(),
	}
	for _, rep := range reps {
		key := rep.OS
		if key == "" {
			key = rep.Bench
		}
		s.PerOS[key] = OSCount{
			Passed:  rep.Passed,
			Failed:  rep.Failed,
			Total:   rep.Total,
			Outcome: string(rep.Outcome),
		}
		s.TotalFailures += rep.Failed
		if severity(string(rep.Outcome)) > severity(s.Outcome) {
			s.Outcome = string(rep.Outcome)
		}
	}
	s.UniqueFailures = len(groups)
	return s
}

// buildDoc assembles the FailuresDoc from reports.
func buildDoc(reps []report.Report, now time.Time) (FailuresDoc, []Group) {
	groups := GroupFailures(reps)
	entries := make([]FailureEntry, 0, len(groups))
	for i, g := range groups {
		entries = append(entries, FailureEntry{
			Index:     i + 1,
			Class:     g.Key.ErrorClass,
			File:      g.Sample.File,
			Line:      deriveLine(g.Sample.File, g.Sample.Stack),
			Name:      g.Sample.Name,
			Platforms: g.Platforms,
			Error:     g.Sample.Error,
			Stack:     g.Sample.Stack,
			Output:    g.Sample.Output,
		})
	}
	doc := FailuresDoc{
		SchemaVersion: DigestSchemaVersion,
		Summary:       Summarize(reps, groups, now),
		Failures:      entries,
	}
	return doc, groups
}

// WriteDigest builds the grouped, capped digest from reps and writes
// failures.json (full text) + FAILURES.md (capped) into dir. With
// opts.PerFailureFiles it also writes failures/NN-<slug>.md + failures/INDEX.md.
// It is deterministic: stable ordering and an injectable clock.
func WriteDigest(dir string, reps []report.Report, opts WriteOpts) (Paths, error) {
	doc, _ := buildDoc(reps, opts.now())

	paths := Paths{
		Dir:          dir,
		FailuresJSON: filepath.Join(dir, "failures.json"),
		FailuresMD:   filepath.Join(dir, "FAILURES.md"),
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return paths, fmt.Errorf("digest: create dir %s: %w", dir, err)
	}

	jsonBytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return paths, fmt.Errorf("digest: marshal failures.json: %w", err)
	}
	jsonBytes = append(jsonBytes, '\n')
	if err := os.WriteFile(paths.FailuresJSON, jsonBytes, 0o644); err != nil {
		return paths, fmt.Errorf("digest: write failures.json: %w", err)
	}

	if err := os.WriteFile(paths.FailuresMD, []byte(renderFailuresMD(doc, opts)), 0o644); err != nil {
		return paths, fmt.Errorf("digest: write FAILURES.md: %w", err)
	}

	if opts.PerFailureFiles && len(doc.Failures) > 0 {
		paths.FailuresDir = filepath.Join(dir, "failures")
		paths.IndexMD = filepath.Join(paths.FailuresDir, "INDEX.md")
		if err := writePerFailureFiles(paths.FailuresDir, doc, opts); err != nil {
			return paths, err
		}
	}
	return paths, nil
}

// renderFailuresMD renders the human/agent digest: headline first, then one
// bounded block per unique failure with Option-E truncation pointers.
func renderFailuresMD(doc FailuresDoc, opts WriteOpts) string {
	var b strings.Builder
	s := doc.Summary
	fmt.Fprintf(&b, "# Test failures — %s\n\n", strings.ToUpper(s.Outcome))

	fmt.Fprintf(&b, "%d failures, %d unique", s.TotalFailures, s.UniqueFailures)
	if parts := perOSParts(s.PerOS); len(parts) > 0 {
		fmt.Fprintf(&b, " · %s", strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")

	if len(doc.Failures) == 0 {
		b.WriteString("No failures. ✓\n")
		return b.String()
	}

	shown := doc.Failures
	if max := opts.maxEntries(); len(shown) > max {
		shown = shown[:max]
	}
	for _, f := range shown {
		writeFailureBlock(&b, f, opts, "## ")
	}
	if n := len(doc.Failures) - len(shown); n > 0 {
		fmt.Fprintf(&b, "… %d more failures omitted; see failures.json\n", n)
	}
	return b.String()
}

func perOSParts(perOS map[string]OSCount) []string {
	names := make([]string, 0, len(perOS))
	for k := range perOS {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		c := perOS[n]
		parts = append(parts, fmt.Sprintf("%s %d/%d failed", n, c.Failed, c.Total))
	}
	return parts
}

// writeFailureBlock renders one failure section, shared by FAILURES.md and the
// per-failure files (Option D). headingPrefix is "## " or "# ".
func writeFailureBlock(b *strings.Builder, f FailureEntry, opts WriteOpts, headingPrefix string) {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	fmt.Fprintf(b, "%s%d · %s · %s · %q\n", headingPrefix, f.Index, f.Class, loc, f.Name)
	if len(f.Platforms) > 0 {
		fmt.Fprintf(b, "Platforms: %s\n", strings.Join(f.Platforms, ", "))
	}
	b.WriteString("\n")

	ref := func(field string) string {
		return fmt.Sprintf("failures.json#/%d/%s", f.Index-1, field)
	}
	if f.Error != "" {
		capped, _ := Cap(f.Error, errorCapOpts(opts))
		b.WriteString("Error:\n")
		writeIndented(b, capped)
		b.WriteString("\n")
	}
	if f.Stack != "" {
		capped, meta := Cap(f.Stack, stackCapOpts(opts))
		b.WriteString("Stack:\n")
		writeIndented(b, capped)
		if p := Pointer(meta, ref("stack")); p != "" {
			fmt.Fprintf(b, "    %s\n", p)
		}
		b.WriteString("\n")
	}
	if f.Output != "" {
		capped, meta := Cap(f.Output, outputCapOpts(opts))
		b.WriteString("Output (last lines):\n")
		writeIndented(b, capped)
		if p := Pointer(meta, ref("output")); p != "" {
			fmt.Fprintf(b, "    %s\n", p)
		}
		b.WriteString("\n")
	}
}

func writeIndented(b *strings.Builder, s string) {
	for _, ln := range strings.Split(s, "\n") {
		fmt.Fprintf(b, "    %s\n", ln)
	}
}

func errorCapOpts(o WriteOpts) CapOpts {
	c := o.Cap
	if c.MaxLines <= 0 {
		c.MaxLines = 5 // the error message is usually one line; allow a few
	}
	c.Tail = false
	return c
}

func stackCapOpts(o WriteOpts) CapOpts {
	c := o.Cap
	c.Tail = false
	return c
}

func outputCapOpts(o WriteOpts) CapOpts {
	c := o.Cap
	c.Tail = true // captured output's actionable lines are at the end
	return c
}

// --- Option D: per-failure files + index ---

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// writePerFailureFiles writes one bounded file per failure plus an INDEX.md
// table of contents into failuresDir (Option D).
func writePerFailureFiles(failuresDir string, doc FailuresDoc, opts WriteOpts) error {
	if err := os.MkdirAll(failuresDir, 0o755); err != nil {
		return fmt.Errorf("digest: create failures dir: %w", err)
	}
	max := opts.maxEntries()

	var idx strings.Builder
	idx.WriteString("# Failure index\n\n")
	fmt.Fprintf(&idx, "%d failures, %d unique\n\n", doc.Summary.TotalFailures, doc.Summary.UniqueFailures)
	idx.WriteString("| # | class | location | name | platforms | file |\n")
	idx.WriteString("|---|-------|----------|------|-----------|------|\n")

	for _, f := range doc.Failures {
		if f.Index > max {
			break
		}
		fname := fmt.Sprintf("%02d-%s.md", f.Index, slugify(f.File, f.Name))
		var fb strings.Builder
		writeFailureBlock(&fb, f, opts, "# ")
		if err := os.WriteFile(filepath.Join(failuresDir, fname), []byte(fb.String()), 0o644); err != nil {
			return fmt.Errorf("digest: write %s: %w", fname, err)
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(&idx, "| %d | %s | %s | %s | %s | [%s](%s) |\n",
			f.Index, f.Class, loc, mdEscape(f.Name), strings.Join(f.Platforms, ", "), fname, fname)
	}
	if err := os.WriteFile(filepath.Join(failuresDir, "INDEX.md"), []byte(idx.String()), 0o644); err != nil {
		return fmt.Errorf("digest: write INDEX.md: %w", err)
	}
	return nil
}

// slugify builds a filesystem-safe slug from file+name. The NN- prefix the
// caller prepends guarantees uniqueness even when two slugs collide.
func slugify(file, name string) string {
	s := strings.ToLower(file + "-" + name)
	s = reSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "failure"
	}
	return s
}

func mdEscape(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
