package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

// WatchdogPath is the contractual in-image path the Dockerfiles bake the
// watchdog to (see dockerfiles/*.Dockerfile).
const WatchdogPath = "/opt/gsd-test/watchdog.mjs"

// Entry scripts baked into the Tester Images. The entry script runs `npm ci`
// and `npm run build` (when a package.json is present) BEFORE exec-ing the
// watchdog, so the watchdog deadline times only the test phase (ADR-0021
// Decision 1). It is liberal about missing package.json / build script but
// aborts loudly if `npm ci` fails (Postel's robustness principle, applied
// visibly).
const (
	EntryScriptLinux   = "/opt/gsd-test/run-and-die.sh"
	EntryScriptWindows = "C:/opt/gsd-test/run-and-die.cmd"
)

func entryScript(target string) string {
	if target == "windows" {
		return EntryScriptWindows
	}
	return EntryScriptLinux
}

// envelope mirrors the JSON the watchdog (reporter/watchdog.mjs) prints. Field
// names are the watchdog's camelCase, distinct from report's snake_case.
type envelope struct {
	Outcome  string `json:"outcome"` // "completed" | "reaped"
	ExitCode *int   `json:"exitCode"`
	PerTest  []struct {
		File        string  `json:"file"`
		Name        string  `json:"name"`
		DurationMs  float64 `json:"durationMs"`
		Status      string  `json:"status"`
		ExitedClean bool    `json:"exitedClean"`
	} `json:"perTest"`
	HandleSamples []struct {
		File    string `json:"file"`
		Samples []struct {
			AtMs   int64               `json:"atMs"`
			Open   int                 `json:"open"`
			Leaked []string            `json:"leaked"`
			Stacks map[string][]string `json:"stacks"`
		} `json:"samples"`
	} `json:"handleSamples"`
	Kill *struct {
		Reason              string `json:"reason"`
		ReapedBy            string `json:"reapedBy"`
		EffectiveDeadlineMs int64  `json:"effectiveDeadlineMs"`
		ElapsedMs           int64  `json:"elapsedMs"`
		LastActiveTest      *struct {
			File string `json:"file"`
			Name string `json:"name"`
		} `json:"lastActiveTest"`
		InFlightTests []struct {
			File         string `json:"file"`
			Name         string `json:"name"`
			StartedMsAgo int64  `json:"startedMsAgo"`
		} `json:"inFlightTests"`
		SignalChain []string `json:"signalChain"`
		Granularity string   `json:"granularity"`
	} `json:"kill"`
}

// InContainerCommand builds the watchdog-wrapped test command appended after
// the image in the docker argv: the watchdog enforces the effective deadline
// and wraps the hardened node --test invocation (ADR-0021 §B/E). Under
// isolation=none it passes --granularity process so the kill record marks
// attribution best-effort (Decision 5).
func InContainerCommand(spec runspec.Spec, effectiveDeadlineMs int64) []string {
	// The entry script installs deps + builds, then exec-s the watchdog with
	// these args (so the deadline covers only the test phase).
	cmd := []string{entryScript(spec.Target), "--deadline-ms", fmt.Sprint(effectiveDeadlineMs)}
	if spec.Isolation == runspec.IsolationNone {
		cmd = append(cmd, "--granularity", "process")
	}
	cmd = append(cmd, "--")
	cmd = append(cmd, TestRunnerArgs(spec, effectiveDeadlineMs)...)
	return cmd
}

// Run executes the spec's tests in a disposable, resource-capped container via
// runner (docker over SSH in prod; local docker in tests), parses the watchdog
// envelope, and returns the per-OS Report. A reaped run becomes a loud
// OutcomeReaped report with the kill record attached (ADR-0021 Decision 6).
func Run(ctx context.Context, runner reaper.Runner, spec runspec.Spec, imageID string, deadlineEpochMs, effectiveDeadlineMs int64, startedAt time.Time) (report.Report, error) {
	args := DockerRunArgs(spec, imageID, deadlineEpochMs, "")
	args = append(args, InContainerCommand(spec, effectiveDeadlineMs)...)

	out, err := runner(ctx, args...)
	if err != nil {
		return report.Report{}, fmt.Errorf("dispatch: run container: %w", err)
	}
	return reportFromEnvelope(out, spec, imageID, startedAt)
}

// RunCopyIn is the production run-and-die path: it copies the PR-merged
// worktree into a disposable container (dispatch.Exec, ADR-0002/D7), runs the
// watchdog-wrapped suite, and maps the envelope to a Report.
func RunCopyIn(ctx context.Context, runner reaper.Runner, spec runspec.Spec, imageID, worktreeDir string, deadlineEpochMs, effectiveDeadlineMs int64, startedAt time.Time) (report.Report, error) {
	out, err := Exec(ctx, runner, spec, imageID, worktreeDir, deadlineEpochMs, effectiveDeadlineMs)
	if err != nil {
		return report.Report{}, err
	}
	return reportFromEnvelope(out, spec, imageID, startedAt)
}

// reportFromEnvelope parses a watchdog JSON envelope and maps it to a Report.
func reportFromEnvelope(out []byte, spec runspec.Spec, imageID string, startedAt time.Time) (report.Report, error) {
	var env envelope
	if jerr := json.Unmarshal(out, &env); jerr != nil {
		return report.Report{}, fmt.Errorf("dispatch: parse watchdog envelope: %w", jerr)
	}

	rep := report.New(spec.Target, "", imageID, "", startedAt)
	for _, ts := range env.PerTest {
		rep.PerTest = append(rep.PerTest, report.TestStat{
			File: ts.File, Name: ts.Name, DurationMs: ts.DurationMs,
			Status: ts.Status, ExitedClean: ts.ExitedClean,
		})
	}
	for _, hs := range env.HandleSamples {
		samples := make([]report.HandleSample, 0, len(hs.Samples))
		for _, s := range hs.Samples {
			samples = append(samples, report.HandleSample{
				AtMs: s.AtMs, Open: s.Open, Leaked: s.Leaked, Stacks: s.Stacks,
			})
		}
		rep.HandleSamples = append(rep.HandleSamples, report.HandleSamples{File: hs.File, Samples: samples})
	}

	switch env.Outcome {
	case "reaped":
		rep.MarkReaped(endTime(startedAt, env), mapKill(env))
	case "completed":
		if env.ExitCode != nil && *env.ExitCode != 0 {
			rep.Kind = report.KindFail
			rep.Outcome = report.OutcomeFailed
			rep.DurationMs = float64(endTime(startedAt, env).Sub(startedAt).Milliseconds())
		} else {
			rep.Finalize(endTime(startedAt, env))
		}
	default:
		return report.Report{}, fmt.Errorf("dispatch: unknown watchdog outcome %q", env.Outcome)
	}
	return rep, nil
}

// endTime derives the run end from the watchdog's elapsed measurement when
// available, falling back to the start instant.
func endTime(startedAt time.Time, env envelope) time.Time {
	if env.Kill != nil && env.Kill.ElapsedMs > 0 {
		return startedAt.Add(time.Duration(env.Kill.ElapsedMs) * time.Millisecond)
	}
	return startedAt
}

// mapKill translates the watchdog's camelCase kill record into report's typed
// KillRecord.
func mapKill(env envelope) report.KillRecord {
	k := env.Kill
	kr := report.KillRecord{
		Reason:              report.KillReason(k.Reason),
		ReapedBy:            report.ReapedBy(k.ReapedBy),
		EffectiveDeadlineMs: k.EffectiveDeadlineMs,
		ElapsedMs:           k.ElapsedMs,
		SignalChain:         k.SignalChain,
		Granularity:         k.Granularity,
	}
	if k.LastActiveTest != nil {
		kr.LastActiveTest = &report.ActiveTest{File: k.LastActiveTest.File, Name: k.LastActiveTest.Name}
	}
	for _, f := range k.InFlightTests {
		kr.InFlightTests = append(kr.InFlightTests, report.InFlightTest{
			File: f.File, Name: f.Name, StartedMsAgo: f.StartedMsAgo,
		})
	}
	return kr
}
