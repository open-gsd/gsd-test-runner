package digest

import (
	"encoding/xml"
	"fmt"
	"sort"

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
			ts.Cases = append(ts.Cases, junitTestcase{
				Name:      f.Name,
				Classname: f.File,
				Time:      fmt.Sprintf("%.3f", f.DurationMs/1000),
				Failure: &junitFailure{
					Type:    string(f.ErrorClass),
					Message: f.Error,
					Body:    f.Stack,
				},
			})
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
