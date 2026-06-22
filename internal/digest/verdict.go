package digest

import (
	"encoding/json"
	"io"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// VerdictTopN bounds how many failures the verdict's top[] lists.
const VerdictTopN = 5

// VerdictTop is one entry in the verdict's top[] list.
type VerdictTop struct {
	Class report.ErrorClass `json:"class"`
	File  string            `json:"file"`
	Line  int               `json:"line"`
	Name  string            `json:"name"`
}

// VerdictArtifacts points at the on-disk evidence for this run.
type VerdictArtifacts struct {
	Dir          string `json:"dir,omitempty"`
	FailuresJSON string `json:"failures_json,omitempty"`
	FailuresMD   string `json:"failures_md,omitempty"`
	JUnitXML     string `json:"junit_xml,omitempty"`
	EventsJSONL  string `json:"events_jsonl,omitempty"`
}

// VerdictLine is the single structured line emitted as the last thing on stdout
// in every mode and outcome (Option C). The "verdict" Type discriminator keeps
// it distinct from the per-event ModeJSONEvents lines (keyed by "kind") and the
// per-OS result lines (keyed by "type":"result").
type VerdictLine struct {
	Type           string             `json:"type"` // always "verdict"
	Outcome        string             `json:"outcome"`
	PerOS          map[string]OSCount `json:"per_os"`
	UniqueFailures int                `json:"unique_failures"`
	TotalFailures  int                `json:"total_failures"`
	Top            []VerdictTop       `json:"top"`
	Artifacts      VerdictArtifacts   `json:"artifacts"`
}

// Verdict builds the verdict line from the reports and the artifact paths. It
// generalizes the run-and-die watchdog envelope to the standard path.
func Verdict(reps []report.Report, p Paths) VerdictLine {
	groups := GroupFailures(reps)
	summary := Summarize(reps, groups, time.Now())

	v := VerdictLine{
		Type:           "verdict",
		Outcome:        summary.Outcome,
		PerOS:          summary.PerOS,
		UniqueFailures: summary.UniqueFailures,
		TotalFailures:  summary.TotalFailures,
		Top:            []VerdictTop{},
		Artifacts: VerdictArtifacts{
			Dir:          p.Dir,
			FailuresJSON: p.FailuresJSON,
			FailuresMD:   p.FailuresMD,
			JUnitXML:     p.JUnitXML,
			EventsJSONL:  p.EventsJSONL,
		},
	}
	for i, g := range groups {
		if i >= VerdictTopN {
			break
		}
		v.Top = append(v.Top, VerdictTop{
			Class: g.Key.ErrorClass,
			File:  g.Sample.File,
			Line:  deriveLine(g.Sample.File, g.Sample.Stack),
			Name:  g.Sample.Name,
		})
	}
	return v
}

// WriteLine marshals the verdict as a single compact JSON line (with a trailing
// newline) — the last thing written to stdout.
func (v VerdictLine) WriteLine(w io.Writer) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
