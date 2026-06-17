package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/runspec"
)

var startedAt = time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

func specFor(target string) runspec.Spec {
	return runspec.Spec{
		RunID: "run-1", Repo: "/work", Target: target,
		TestCommand: []string{"node", "--test"},
		Budget:      runspec.Budget{OverrunFactor: 1.5, HardCapMs: 3600000},
		Isolation:   runspec.IsolationProcess,
	}
}

// captureRunner returns canned stdout and records the args it was invoked with.
func captureRunner(stdout string, got *[]string) func(context.Context, ...string) ([]byte, error) {
	return func(_ context.Context, args ...string) ([]byte, error) {
		*got = append([]string{}, args...)
		return []byte(stdout), nil
	}
}

func TestRun_ReapedEnvelopeMapsToReapedReport(t *testing.T) {
	env := `{"outcome":"reaped","exitCode":null,"kill":{"reason":"estimate_overrun",` +
		`"reapedBy":"in_container","effectiveDeadlineMs":180000,"elapsedMs":181000,` +
		`"lastActiveTest":{"file":"db.test.js","name":"reconnects"},` +
		`"inFlightTests":[{"file":"db.test.js","name":"reconnects","startedMsAgo":175000}],` +
		`"signalChain":["SIGTERM@180000","SIGKILL@180010"]}}`
	var got []string
	rep, err := dispatch.Run(context.Background(), captureRunner(env, &got),
		specFor("linux"), "img:v2", 1_000_000, 180000, startedAt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outcome != report.OutcomeReaped {
		t.Errorf("Outcome = %q, want reaped", rep.Outcome)
	}
	if rep.Kill == nil || rep.Kill.Reason != report.KillReasonEstimateOverrun {
		t.Fatalf("Kill = %+v, want reason estimate_overrun", rep.Kill)
	}
	if rep.Kill.LastActiveTest == nil || rep.Kill.LastActiveTest.Name != "reconnects" {
		t.Errorf("Kill.LastActiveTest = %+v, want reconnects", rep.Kill.LastActiveTest)
	}
	if rep.Kill.ReapedBy != report.ReapedByInContainer {
		t.Errorf("Kill.ReapedBy = %q, want in_container", rep.Kill.ReapedBy)
	}
	// The assembled command must run the watchdog with the effective deadline,
	// wrapping the hardened node --test.
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, dispatch.EntryScriptLinux) {
		t.Errorf("command did not invoke the entry script: %v", got)
	}
	if !strings.Contains(joined, "--deadline-ms 180000") {
		t.Errorf("command missing effective deadline: %v", got)
	}
	if !strings.Contains(joined, "--test-force-exit") {
		t.Errorf("command missing hardened node --test flags: %v", got)
	}
}

func TestRun_CompletedEnvelopeMapsOutcome(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want report.Outcome
	}{
		{"pass", `{"outcome":"completed","exitCode":0}`, report.OutcomePassed},
		{"fail", `{"outcome":"completed","exitCode":1}`, report.OutcomeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			rep, err := dispatch.Run(context.Background(), captureRunner(tc.env, &got),
				specFor("linux"), "img:v2", 1_000_000, 60000, startedAt)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if rep.Outcome != tc.want {
				t.Errorf("Outcome = %q, want %q", rep.Outcome, tc.want)
			}
			if rep.Kill != nil {
				t.Errorf("completed run should have no kill record, got %+v", rep.Kill)
			}
		})
	}
}

func TestRun_NoneIsolationStampsGranularity(t *testing.T) {
	spec := specFor("linux")
	spec.Isolation = runspec.IsolationNone
	var got []string
	_, err := dispatch.Run(context.Background(), captureRunner(`{"outcome":"completed","exitCode":0}`, &got),
		spec, "img:v2", 1_000_000, 60000, startedAt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(strings.Join(got, " "), "--granularity process") {
		t.Errorf("isolation=none must pass --granularity process to the watchdog: %v", got)
	}
}

func TestInContainerCommand_EntryScriptThenWatchdog(t *testing.T) {
	got := dispatch.InContainerCommand(specFor("linux"), 180000)
	if got[0] != dispatch.EntryScriptLinux {
		t.Errorf("entry = %q, want %q (orchestrates npm ci/build then the watchdog)", got[0], dispatch.EntryScriptLinux)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--deadline-ms 180000") {
		t.Errorf("missing watchdog deadline: %v", got)
	}
	// The test command appears after the watchdog's "--" separator.
	if !strings.Contains(joined, "-- node --test") {
		t.Errorf("test command not wrapped after --: %v", got)
	}
}

func TestInContainerCommand_WindowsEntryScript(t *testing.T) {
	got := dispatch.InContainerCommand(specFor("windows"), 60000)
	if got[0] != dispatch.EntryScriptWindows {
		t.Errorf("entry = %q, want %q", got[0], dispatch.EntryScriptWindows)
	}
}

func TestRun_MapsPerTestTelemetry(t *testing.T) {
	env := `{"outcome":"completed","exitCode":0,"perTest":[` +
		`{"file":"a.test.js","name":"alpha","durationMs":12,"status":"passed","exitedClean":true},` +
		`{"file":"b.test.js","name":"beta","durationMs":2000,"status":"killed","exitedClean":false}]}`
	var got []string
	rep, err := dispatch.Run(context.Background(), captureRunner(env, &got),
		specFor("linux"), "img:v2", 1_000_000, 60000, startedAt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.PerTest) != 2 {
		t.Fatalf("PerTest len = %d, want 2: %+v", len(rep.PerTest), rep.PerTest)
	}
	if rep.PerTest[1].Status != "killed" || rep.PerTest[1].ExitedClean {
		t.Errorf("PerTest[1] = %+v, want killed/exitedClean=false", rep.PerTest[1])
	}
}
