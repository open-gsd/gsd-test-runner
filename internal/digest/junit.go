package digest

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// JUnit XML (Option H, #84). Generated in Go from the reports we already have so
// both the multi-OS standard path and the single-OS run-and-die path emit
// identical junit without depending on node's reporter plumbing (run-and-die
// runs under docker --rm with stdout reserved for the watchdog envelope, so a
// post-hoc docker cp is impossible there). One <testsuite> per OS; failed tests
// are <testcase><failure>; passing tests are counted in the attrs but not listed
// individually (the digest keeps only failures). Deterministic: suites sorted by
// OS, failures in parse order.

type junitTestsuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Name     string           `xml:"name,attr"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Suites   []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Cases    []junitTestcase `xml:"testcase"`
}

type junitTestcase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr,omitempty"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Type    string `xml:"type,attr"`
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// sanitizeXML coerces s into a string that is safe to embed in XML 1.0:
//   - invalid UTF-8 sequences are replaced by the Unicode replacement character.
//   - control characters illegal in XML 1.0 (all C0/C1 except HT, LF, CR) are
//     stripped. ESC (\x1b), NUL (\x00), BEL (\x07) and the rest of the range
//     U+0001–U+0008, U+000B–U+000C, U+000E–U+001F, U+007F, U+0080–U+009F
//     are all removed.
func sanitizeXML(s string) string {
	// First pass: replace invalid UTF-8.
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	// Second pass: strip XML-1.0-illegal control characters.
	return strings.Map(func(r rune) rune {
		// XML 1.0 legal characters: #x9 | #xA | #xD | [#x20-#xD7FF] | [#xE000-#xFFFD] | [#x10000-#x10FFFF]
		switch {
		case r == 0x09 || r == 0x0A || r == 0x0D:
			return r // tab, LF, CR — always legal
		case r < 0x20:
			return -1 // C0 controls (incl. NUL, BEL, ESC, etc.) — strip
		case r == 0x7F:
			return -1 // DEL — strip
		case r >= 0x80 && r <= 0x9F && unicode.IsControl(r):
			return -1 // C1 controls — strip
		case r == 0xFFFE || r == 0xFFFF:
			return -1 // XML-forbidden non-characters
		default:
			return r
		}
	}, s)
}

// JUnitFromReports renders a deterministic JUnit XML document from reps.
func JUnitFromReports(reps []report.Report) ([]byte, error) {
	sorted := make([]report.Report, len(reps))
	copy(sorted, reps)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].OS != sorted[j].OS {
			return sorted[i].OS < sorted[j].OS
		}
		return sorted[i].Bench < sorted[j].Bench
	})

	root := junitTestsuites{Name: "gsd-test"}
	for _, rep := range sorted {
		name := rep.OS
		if name == "" {
			name = rep.Bench
		}
		ts := junitTestsuite{Name: name, Tests: rep.Total, Failures: rep.Failed}
		for _, f := range rep.Failures {
			tc := junitTestcase{
				Name:      sanitizeXML(f.Name),
				Classname: sanitizeXML(f.File),
				Failure: &junitFailure{
					Type:    string(f.ErrorClass),
					Message: sanitizeXML(f.Error),
					Body:    sanitizeXML(f.Stack),
				},
			}
			// B-19: omitempty on Time is dead when the string is always assigned.
			// Only assign Time when there is a real non-zero duration.
			if f.DurationMs > 0 {
				tc.Time = fmt.Sprintf("%.3f", f.DurationMs/1000)
			}
			ts.Cases = append(ts.Cases, tc)
		}
		root.Tests += rep.Total
		root.Failures += rep.Failed
		root.Suites = append(root.Suites, ts)
	}

	body, err := xml.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("digest: marshal junit: %w", err)
	}
	out := append([]byte(xml.Header), body...)
	return append(out, '\n'), nil
}
