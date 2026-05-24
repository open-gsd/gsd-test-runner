// Package renderer consumes the pipeline.Event channel and renders
// human-readable progress + final Per-OS Reports to a writer (typically
// os.Stdout for the report banners and os.Stderr for live progress).
//
// Print-as-they-finish per
// docs/adr/0009-local-engine-top-level-orchestration.md. Each event is
// labeled with its OS so parallel pipelines interleave legibly.
package renderer

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// Mode controls renderer output format.
type Mode int

const (
	ModeTTY        Mode = iota // human-readable, per-OS prefixed lines
	ModeJSONEvents             // one Event JSON line per emitted event
)

// Renderer multiplexes Pipeline event channels (one per OS) onto a single
// output writer, formatting per Mode. Caller wires the lifecycle:
//
//	r := renderer.New(os.Stdout, renderer.ModeTTY)
//	r.Subscribe("linux", linuxEvents)
//	r.Subscribe("windows", windowsEvents)
//	// ...orchestrator dispatches pipelines and feeds events...
//	r.AddResult("linux", linuxReport, linuxErr)
//	r.AddResult("windows", windowsReport, windowsErr)
//	r.Wait()  // blocks until all subscribed channels close + final summary printed
type Renderer struct {
	out    io.Writer
	mode   Mode
	mu     sync.Mutex // guards writes to out

	eventsWG  sync.WaitGroup // tracks subscribed event-consumer goroutines
	results   map[string]osResult
	resultsMu sync.Mutex
}

type osResult struct {
	rep report.Report
	err error
}

// New constructs a Renderer.
func New(out io.Writer, mode Mode) *Renderer {
	return &Renderer{
		out:     out,
		mode:    mode,
		results: make(map[string]osResult),
	}
}

// Subscribe registers a Pipeline event channel for a given OS. The renderer
// drains the channel until it's closed (the Pipeline closes it on RunAll
// completion). Spawns a goroutine per subscription; Wait() blocks for all of
// them.
func (r *Renderer) Subscribe(osName string, events <-chan pipeline.Event) {
	r.eventsWG.Add(1)
	go r.consume(osName, events)
}

// AddResult records the per-OS Report + leg error from a completed Pipeline.
// The orchestrator calls this once per OS after Pipeline.RunAll returns.
// Used by the final summary block in tty mode.
func (r *Renderer) AddResult(osName string, rep report.Report, err error) {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()
	r.results[osName] = osResult{rep: rep, err: err}
}

// Wait blocks until all subscribed event channels have drained and prints
// the final per-OS summary block (tty mode only).
func (r *Renderer) Wait() {
	r.eventsWG.Wait()
	if r.mode == ModeTTY {
		r.printSummary()
	} else {
		r.printJSONResults()
	}
}

func (r *Renderer) consume(osName string, events <-chan pipeline.Event) {
	defer r.eventsWG.Done()
	for ev := range events {
		r.render(osName, ev)
	}
}

func (r *Renderer) render(osName string, ev pipeline.Event) {
	switch r.mode {
	case ModeJSONEvents:
		r.renderJSONEvent(osName, ev)
	case ModeTTY:
		r.renderTTY(osName, ev)
	}
}

func (r *Renderer) renderJSONEvent(osName string, ev pipeline.Event) {
	// Encode each event as a single JSON line. Include OS so downstream
	// consumers can demux. Use a stable schema.
	line := struct {
		OS     string `json:"os"`
		Kind   string `json:"kind"`
		Leg    string `json:"leg,omitempty"`
		Time   string `json:"time"`
		Line   string `json:"line,omitempty"`
		Stream string `json:"stream,omitempty"`
		Detail string `json:"detail,omitempty"`
	}{
		OS:     osName,
		Kind:   ev.Kind.String(),
		Leg:    legString(ev.Leg),
		Time:   ev.Time.Format("2006-01-02T15:04:05.000Z07:00"),
		Line:   ev.Line,
		Stream: ev.Stream,
		Detail: ev.Detail,
	}
	b, _ := json.Marshal(line)
	r.mu.Lock()
	fmt.Fprintln(r.out, string(b))
	r.mu.Unlock()
}

func (r *Renderer) renderTTY(osName string, ev pipeline.Event) {
	prefix := fmt.Sprintf("[%s]", osName)
	var s string
	switch ev.Kind {
	case pipeline.EventLegStart:
		s = fmt.Sprintf("%s START %s\n", prefix, legString(ev.Leg))
	case pipeline.EventLegSuccess:
		s = fmt.Sprintf("%s OK    %s\n", prefix, legString(ev.Leg))
	case pipeline.EventLegFailure:
		s = fmt.Sprintf("%s FAIL  %s: %s\n", prefix, legString(ev.Leg), ev.Detail)
	case pipeline.EventLegSkipped:
		s = fmt.Sprintf("%s SKIP  %s: %s\n", prefix, legString(ev.Leg), ev.Detail)
	case pipeline.EventChildOutput:
		// Indent + prefix with stream marker for readability.
		tag := "1"
		if ev.Stream == "stderr" {
			tag = "2"
		}
		s = fmt.Sprintf("%s   |%s %s\n", prefix, tag, ev.Line)
	case pipeline.EventTestPass:
		s = fmt.Sprintf("%s   ✓ %s\n", prefix, ev.Line)
	case pipeline.EventTestFail:
		s = fmt.Sprintf("%s   ✗ %s\n", prefix, ev.Line)
	default:
		return
	}
	r.mu.Lock()
	fmt.Fprint(r.out, s)
	r.mu.Unlock()
}

func (r *Renderer) printSummary() {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()

	osList := make([]string, 0, len(r.results))
	for osName := range r.results {
		osList = append(osList, osName)
	}
	sort.Strings(osList)

	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Fprintln(r.out, "")
	fmt.Fprintln(r.out, "─── Summary ───")
	for _, osName := range osList {
		result := r.results[osName]
		if result.err != nil {
			fmt.Fprintf(r.out, "%-10s INCONCLUSIVE: %v\n", osName, result.err)
			continue
		}
		if result.rep.Kind == report.KindPass {
			fmt.Fprintf(r.out, "%-10s PASS  %d/%d tests\n", osName, result.rep.Passed, result.rep.Total)
		} else {
			fmt.Fprintf(r.out, "%-10s FAIL  %d/%d tests (%d failures)\n", osName, result.rep.Passed, result.rep.Total, len(result.rep.Failures))
			for _, f := range result.rep.Failures {
				fmt.Fprintf(r.out, "  ✗ %s\n", f.Name)
				if f.Error != "" {
					for _, line := range strings.Split(f.Error, "\n") {
						fmt.Fprintf(r.out, "      %s\n", line)
					}
				}
			}
		}
	}
}

func (r *Renderer) printJSONResults() {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()

	osList := make([]string, 0, len(r.results))
	for osName := range r.results {
		osList = append(osList, osName)
	}
	sort.Strings(osList)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Emit one final "result" line per OS — Report wrapped with OS for demux.
	for _, osName := range osList {
		result := r.results[osName]
		entry := struct {
			OS     string         `json:"os"`
			Type   string         `json:"type"`
			Report *report.Report `json:"report,omitempty"`
			Error  string         `json:"error,omitempty"`
		}{
			OS:   osName,
			Type: "result",
		}
		if result.err != nil {
			entry.Error = result.err.Error()
		} else {
			r2 := result.rep
			entry.Report = &r2
		}
		b, _ := json.Marshal(entry)
		fmt.Fprintln(r.out, string(b))
	}
}

// legString returns the string representation of a Leg using the
// pipeline package's Leg.String() method.
func legString(leg pipeline.Leg) string {
	return leg.String()
}
