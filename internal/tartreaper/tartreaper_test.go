package tartreaper

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/tartexec"
)

// --- test seams -------------------------------------------------------

func stubRunTart(t *testing.T, fn func(ctx context.Context, b bench.Bench, args []string) (string, error)) {
	t.Helper()
	orig := runTart
	runTart = fn
	t.Cleanup(func() { runTart = orig })
}

func stubRunShell(t *testing.T, fn func(ctx context.Context, b bench.Bench, script string) (string, error)) {
	t.Helper()
	orig := runShell
	runShell = fn
	t.Cleanup(func() { runShell = orig })
}

func testBench() bench.Bench {
	return bench.Bench{Name: "tart-bench", Host: "tart-bench.local", OS: "macos", Runtime: bench.RuntimeTart}
}

// realTartListJSON is the CONFIRMED real `tart list --format json` shape
// from a live installation (docs/adr/0030-macos-bench-via-tart.md / this
// package's doc comment).
const realTartListJSON = `[
  {
    "Size" : 33,
    "Disk" : 50,
    "Running" : false,
    "State" : "stopped",
    "Name" : "ghcr.io/cirruslabs/macos-tahoe-base:latest",
    "Source" : "OCI",
    "Accessed" : "2026-09-07T00:47:21Z"
  }
]`

// --- List ---------------------------------------------------------

func TestList_ParsesRealShape_FiltersNonPrefixed(t *testing.T) {
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) == 3 && args[0] == "list" {
			return realTartListJSON, nil
		}
		return "", fmt.Errorf("unexpected tart args: %v", args)
	})
	stubRunShell(t, func(context.Context, bench.Bench, string) (string, error) {
		t.Fatal("readState should not be called for a non-gsd-tart-* entry")
		return "", nil
	})

	vms, err := List(context.Background(), testBench())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("expected the non-gsd-tart-* entry filtered out, got %+v", vms)
	}
}

func TestList_MatchingPrefix_ReadsState(t *testing.T) {
	listJSON := `[{"Name":"gsd-tart-linux-run1","Running":true,"State":"running","Source":"Local","Disk":10,"Size":5,"Accessed":"2026-09-07T00:00:00Z"}]`
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		return listJSON, nil
	})
	var gotScript string
	stubRunShell(t, func(_ context.Context, _ bench.Bench, script string) (string, error) {
		gotScript = script
		return `{"run_id":"run1","branch_slug":"fix-foo","deadline_ms":1000}`, nil
	})

	vms, err := List(context.Background(), testBench())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d: %+v", len(vms), vms)
	}
	v := vms[0]
	if v.Name != "gsd-tart-linux-run1" || !v.HasState || v.RunID != "run1" || v.BranchSlug != "fix-foo" || v.DeadlineMs != 1000 {
		t.Errorf("unexpected VM: %+v", v)
	}
	if gotScript == "" || gotScript[:4] != "cat " {
		t.Errorf("expected a `cat ...` script, got %q", gotScript)
	}
}

func TestList_MissingOrCorruptState_HasStateFalse_NoListError(t *testing.T) {
	listJSON := `[
		{"Name":"gsd-tart-a-run1","Source":"Local"},
		{"Name":"gsd-tart-b-run2","Source":"Local"}
	]`
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		return listJSON, nil
	})
	stubRunShell(t, func(_ context.Context, _ bench.Bench, script string) (string, error) {
		if contains(script, "gsd-tart-a-run1") {
			return "", errors.New("no such file")
		}
		// corrupt JSON for the second VM
		return "not json", nil
	})

	vms, err := List(context.Background(), testBench())
	if err != nil {
		t.Fatalf("List: expected no error even with missing/corrupt state, got %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}
	for _, v := range vms {
		if v.HasState {
			t.Errorf("expected HasState=false for %s, got true", v.Name)
		}
		if v.RunID != "" || v.BranchSlug != "" || v.DeadlineMs != 0 {
			t.Errorf("expected zero-value fields for %s, got %+v", v.Name, v)
		}
	}
}

func TestList_TartListFails_ReturnsError(t *testing.T) {
	stubRunTart(t, func(context.Context, bench.Bench, []string) (string, error) {
		return "", &tartexec.ExecError{Stderr: "ssh down", ExitCode: 255}
	})
	_, err := List(context.Background(), testBench())
	if err == nil {
		t.Fatal("expected error when tart list fails")
	}
}

func TestList_EmptyOutput_NoVMs(t *testing.T) {
	stubRunTart(t, func(context.Context, bench.Bench, []string) (string, error) {
		return "", nil
	})
	vms, err := List(context.Background(), testBench())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("expected no VMs, got %+v", vms)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Probe ---------------------------------------------------------

// realTartGetJSON is the CONFIRMED real `tart get <vmName> --format json`
// shape from a live installation (docs/adr/0030-macos-bench-via-tart.md
// Decision 5 / this package's doc comment) — a single JSON object, not an
// array.
const realTartGetJSON = `{
  "Display" : "1920x1200",
  "Memory" : 4096,
  "OS" : "darwin",
  "Disk" : 50,
  "Size" : 33,
  "DiskFormat" : "APFS",
  "State" : "running",
  "CPU" : 4,
  "Running" : true
}`

func TestProbe_ParsesRealShape_Running(t *testing.T) {
	var gotArgs []string
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		gotArgs = args
		return realTartGetJSON, nil
	})
	running, state, err := Probe(context.Background(), testBench(), "gsd-tart-macos-run1")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !running {
		t.Error("expected running=true")
	}
	if state != "running" {
		t.Errorf("expected state=running, got %q", state)
	}
	if len(gotArgs) != 4 || gotArgs[0] != "get" || gotArgs[1] != "gsd-tart-macos-run1" {
		t.Errorf("unexpected tart args: %v", gotArgs)
	}
}

func TestProbe_ParsesRealShape_NotRunning(t *testing.T) {
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		return `{"State":"stopped","Running":false}`, nil
	})
	running, state, err := Probe(context.Background(), testBench(), "gsd-tart-macos-run1")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if running {
		t.Error("expected running=false")
	}
	if state != "stopped" {
		t.Errorf("expected state=stopped, got %q", state)
	}
}

func TestProbe_ExitCode2_ReturnsVMNotFoundError(t *testing.T) {
	stubRunTart(t, func(context.Context, bench.Bench, []string) (string, error) {
		return "", &tartexec.ExecError{ExitCode: 2, Stderr: `the specified VM "gsd-tart-macos-run1" does not exist`}
	})
	_, _, err := Probe(context.Background(), testBench(), "gsd-tart-macos-run1")
	var nfe *VMNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *VMNotFoundError, got %T: %v", err, err)
	}
	if nfe.Name != "gsd-tart-macos-run1" {
		t.Errorf("expected Name=gsd-tart-macos-run1, got %q", nfe.Name)
	}
}

func TestProbe_OtherFailure_ReturnsGenericError(t *testing.T) {
	stubRunTart(t, func(context.Context, bench.Bench, []string) (string, error) {
		return "", &tartexec.ExecError{ExitCode: 255, Stderr: "ssh down"}
	})
	_, _, err := Probe(context.Background(), testBench(), "gsd-tart-macos-run1")
	if err == nil {
		t.Fatal("expected an error")
	}
	var nfe *VMNotFoundError
	if errors.As(err, &nfe) {
		t.Fatal("expected a generic error, not *VMNotFoundError, for a non-exit-2 failure")
	}
}

// --- Overdue / OwnedBy ---------------------------------------------------------

func TestOverdue_ExcludesStatelessAndFutureAndUnset(t *testing.T) {
	vms := []VM{
		{Name: "a", HasState: true, DeadlineMs: 500},
		{Name: "b", HasState: true, DeadlineMs: 5000},
		{Name: "c", HasState: false, DeadlineMs: 500}, // stateless — never reaped even though deadline looks overdue
		{Name: "d", HasState: true, DeadlineMs: 0},    // unset deadline
	}
	got := Overdue(vms, 1000)
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("Overdue = %+v, want [a]", got)
	}
}

func TestOwnedBy_EmptySlug_ReturnsOnlyHasStateEntries(t *testing.T) {
	vms := []VM{
		{Name: "a", HasState: true, BranchSlug: "fix-foo"},
		{Name: "b", HasState: false, BranchSlug: ""},
	}
	got := OwnedBy(vms, "")
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("OwnedBy(\"\") = %+v, want [a] (stateless VMs excluded even under the empty-slug escape hatch)", got)
	}
}

func TestOwnedBy_MatchingSlug(t *testing.T) {
	vms := []VM{
		{Name: "a", HasState: true, BranchSlug: "fix-foo"},
		{Name: "b", HasState: true, BranchSlug: "fix-bar"},
	}
	got := OwnedBy(vms, "fix-foo")
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("OwnedBy(fix-foo) = %+v, want [a]", got)
	}
}

// --- Sweep ---------------------------------------------------------

// fakeSweepEnv wires runTart/runShell together for Sweep-level tests: list
// output plus a per-VM state map, recording stop/delete/rm calls.
type fakeSweepEnv struct {
	listJSON  string
	states    map[string]string // vmName -> raw state JSON (or "" to simulate missing)
	stopErr   map[string]error
	deleteErr map[string]error

	stopped []string
	deleted []string
	rmed    []string
}

func (f *fakeSweepEnv) install(t *testing.T) {
	t.Helper()
	stubRunTart(t, func(_ context.Context, _ bench.Bench, args []string) (string, error) {
		if len(args) == 0 {
			return "", nil
		}
		switch args[0] {
		case "list":
			return f.listJSON, nil
		case "stop":
			name := args[1]
			f.stopped = append(f.stopped, name)
			if err, ok := f.stopErr[name]; ok {
				return "", err
			}
			return "", nil
		case "delete":
			name := args[1]
			f.deleted = append(f.deleted, name)
			if err, ok := f.deleteErr[name]; ok {
				return "", err
			}
			return "", nil
		}
		return "", nil
	})
	stubRunShell(t, func(_ context.Context, _ bench.Bench, script string) (string, error) {
		if len(script) >= 4 && script[:4] == "cat " {
			for name, raw := range f.states {
				if contains(script, name) {
					if raw == "" {
						return "", errors.New("no such file")
					}
					return raw, nil
				}
			}
			return "", errors.New("no such file")
		}
		// rm -f ...
		f.rmed = append(f.rmed, script)
		return "", nil
	})
}

func vmListJSON(names ...string) string {
	out := "["
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`{"Name":%q,"Source":"Local"}`, n)
	}
	return out + "]"
}

func TestSweep_BranchScopedTier(t *testing.T) {
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-mine-run1", "gsd-tart-other-run2"),
		states: map[string]string{
			"gsd-tart-mine-run1":  `{"run_id":"run1","branch_slug":"fix-foo","deadline_ms":500}`,
			"gsd-tart-other-run2": `{"run_id":"run2","branch_slug":"fix-bar","deadline_ms":500}`,
		},
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), 1000, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 || reaped[0].Name != "gsd-tart-mine-run1" {
		t.Errorf("reaped = %+v, want exactly [gsd-tart-mine-run1]", reaped)
	}
	if !reflect.DeepEqual(f.stopped, []string{"gsd-tart-mine-run1"}) {
		t.Errorf("stopped = %v, want [gsd-tart-mine-run1]", f.stopped)
	}
	if !reflect.DeepEqual(f.deleted, []string{"gsd-tart-mine-run1"}) {
		t.Errorf("deleted = %v, want [gsd-tart-mine-run1]", f.deleted)
	}
}

var safetyNetGraceMs = int64(reaper.SafetyNetGrace / time.Millisecond)

const safetyNetNowMs = int64(1_700_000_000_000)

func TestSweep_SafetyNetTier_ReapsCrossBranchPastGrace(t *testing.T) {
	longOverdue := safetyNetNowMs - safetyNetGraceMs - int64(time.Hour/time.Millisecond)
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-other-run1"),
		states: map[string]string{
			"gsd-tart-other-run1": fmt.Sprintf(`{"run_id":"run1","branch_slug":"fix-bar","deadline_ms":%d}`, longOverdue),
		},
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), safetyNetNowMs, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 || reaped[0].Name != "gsd-tart-other-run1" {
		t.Errorf("reaped = %+v, want [gsd-tart-other-run1]", reaped)
	}
}

func TestSweep_SafetyNetTier_LeavesRecentCrossBranchAlone(t *testing.T) {
	recentDeadline := safetyNetNowMs - int64(time.Hour/time.Millisecond)
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-other-run1"),
		states: map[string]string{
			"gsd-tart-other-run1": fmt.Sprintf(`{"run_id":"run1","branch_slug":"fix-bar","deadline_ms":%d}`, recentDeadline),
		},
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), safetyNetNowMs, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 0 {
		t.Errorf("reaped = %+v, want none (within SafetyNetGrace diagnostic window)", reaped)
	}
}

func TestSweep_UnionDedup_BothTiersSameVM(t *testing.T) {
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-mine-run1"),
		states: map[string]string{
			// Overdue in both branch-scoped and safety-net tiers.
			"gsd-tart-mine-run1": fmt.Sprintf(`{"run_id":"run1","branch_slug":"fix-foo","deadline_ms":%d}`, safetyNetNowMs-safetyNetGraceMs-1000),
		},
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), safetyNetNowMs, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 {
		t.Errorf("expected exactly 1 deduped VM, got %+v", reaped)
	}
	if len(f.stopped) != 1 {
		t.Errorf("expected stop called exactly once (deduped), got %v", f.stopped)
	}
}

func TestSweep_StatelessVMsNeverSwept_EvenWithEmptyBranchSlug(t *testing.T) {
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-unlabeled-run1"),
		states:   map[string]string{ /* no state for this VM: missing file */ },
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), safetyNetNowMs, "")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 0 {
		t.Errorf("reaped = %+v, want none — a stateless VM must never be swept, even with branchSlug=\"\"", reaped)
	}
	if len(f.stopped) != 0 {
		t.Errorf("stopped = %v, want none", f.stopped)
	}
}

func TestSweep_AlreadyGone_ExitCode2_Tolerated(t *testing.T) {
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-a-run1", "gsd-tart-b-run2"),
		states: map[string]string{
			"gsd-tart-a-run1": `{"run_id":"run1","branch_slug":"fix-foo","deadline_ms":500}`,
			"gsd-tart-b-run2": `{"run_id":"run2","branch_slug":"fix-foo","deadline_ms":500}`,
		},
		stopErr: map[string]error{
			"gsd-tart-a-run1": &tartexec.ExecError{ExitCode: 2, Stderr: `the specified VM "gsd-tart-a-run1" does not exist`},
		},
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), 1000, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: expected nil error (exit-2 tolerated), got %v", err)
	}
	if len(reaped) != 2 {
		t.Errorf("reaped = %+v, want both VMs still reported", reaped)
	}
	// Both VMs must still have been attempted (stop called for both).
	if len(f.stopped) != 2 {
		t.Errorf("stopped = %v, want both a and b attempted despite a's exit-2", f.stopped)
	}
	if len(f.deleted) != 2 {
		t.Errorf("deleted = %v, want both a and b's delete still attempted", f.deleted)
	}
}

func TestSweep_GenuineFailure_ReportedViaErrorsJoin_OthersStillAttempted(t *testing.T) {
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-a-run1", "gsd-tart-b-run2"),
		states: map[string]string{
			"gsd-tart-a-run1": `{"run_id":"run1","branch_slug":"fix-foo","deadline_ms":500}`,
			"gsd-tart-b-run2": `{"run_id":"run2","branch_slug":"fix-foo","deadline_ms":500}`,
		},
		stopErr: map[string]error{
			"gsd-tart-a-run1": &tartexec.ExecError{ExitCode: 1, Stderr: "genuine failure"},
		},
	}
	f.install(t)

	reaped, err := Sweep(context.Background(), testBench(), 1000, "fix-foo")
	if err == nil {
		t.Fatal("Sweep: expected a non-nil error for the genuine (non-exit-2) failure")
	}
	if len(reaped) != 2 {
		t.Errorf("reaped = %+v, want both VMs still reported in the union", reaped)
	}
	// b must still have been attempted despite a's genuine failure.
	foundB := false
	for _, n := range f.stopped {
		if n == "gsd-tart-b-run2" {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("stopped = %v, want gsd-tart-b-run2 still attempted after a's failure", f.stopped)
	}
}

func TestSweep_RemovesStateFileAfterStopDelete(t *testing.T) {
	f := &fakeSweepEnv{
		listJSON: vmListJSON("gsd-tart-a-run1"),
		states: map[string]string{
			"gsd-tart-a-run1": `{"run_id":"run1","branch_slug":"fix-foo","deadline_ms":500}`,
		},
	}
	f.install(t)

	if _, err := Sweep(context.Background(), testBench(), 1000, "fix-foo"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	found := false
	for _, s := range f.rmed {
		if contains(s, "rm -f") && contains(s, "gsd-tart-a-run1.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an `rm -f ...gsd-tart-a-run1.json` call, got rmed=%v", f.rmed)
	}
}

func TestSweep_ListError_Propagates(t *testing.T) {
	stubRunTart(t, func(context.Context, bench.Bench, []string) (string, error) {
		return "", &tartexec.ExecError{Stderr: "ssh down", ExitCode: 255}
	})
	stubRunShell(t, func(context.Context, bench.Bench, string) (string, error) {
		return "", nil
	})
	_, err := Sweep(context.Background(), testBench(), 1000, "")
	if err == nil {
		t.Fatal("Sweep: want error when list fails")
	}
}

// --- unionByName ---------------------------------------------------------

func TestUnionByName_DedupsByName(t *testing.T) {
	a := []VM{{Name: "x"}, {Name: "y"}}
	b := []VM{{Name: "y"}, {Name: "z"}}
	got := unionByName(a, b)
	var names []string
	for _, v := range got {
		names = append(names, v.Name)
	}
	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("unionByName names = %v, want %v", names, want)
	}
}

// --- isAlreadyGone ---------------------------------------------------------

func TestIsAlreadyGone_ExitCode2(t *testing.T) {
	if !isAlreadyGone(&tartexec.ExecError{ExitCode: 2}) {
		t.Error("expected exit code 2 to be treated as already-gone")
	}
}

func TestIsAlreadyGone_OtherExitCode(t *testing.T) {
	if isAlreadyGone(&tartexec.ExecError{ExitCode: 1}) {
		t.Error("expected exit code 1 to NOT be treated as already-gone")
	}
}

func TestIsAlreadyGone_NonExecError(t *testing.T) {
	if isAlreadyGone(errors.New("some other error")) {
		t.Error("expected a non-ExecError to NOT be treated as already-gone")
	}
}
