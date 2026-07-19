package reaper

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParsePS(t *testing.T) {
	// Pre-ADR-0029 psFormat was ID/deadline/run-id; modern psFormat adds Name
	// and BranchSlug. parsePS tolerates both, populating the new fields as the
	// extra columns appear.
	out := []byte("c1\t1000\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n" +
		"c2\t\trun-b\t\t\n" +
		"c3\tnotanumber\trun-c\tkeen_euclid\t\n")
	got := parsePS(out)
	want := []Container{
		{ID: "c1", DeadlineMs: 1000, RunID: "run-a", Name: "gsd-test-fix-foo-aaaaaaaa", BranchSlug: "fix-foo"},
		{ID: "c2", DeadlineMs: 0, RunID: "run-b", Name: "", BranchSlug: ""},
		{ID: "c3", DeadlineMs: 0, RunID: "run-c", Name: "keen_euclid", BranchSlug: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePS = %+v, want %+v", got, want)
	}
}

// fakeRunner records calls and returns canned output for `ps`.
type fakeRunner struct {
	psOut  []byte
	psErr  error
	killed []string
}

func (f *fakeRunner) run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "ps" {
		return f.psOut, f.psErr
	}
	if len(args) >= 2 && args[0] == "kill" {
		f.killed = append(f.killed, args[len(args)-1])
		return nil, nil
	}
	return nil, nil
}

func TestSweep_KillsOnlyOverdue(t *testing.T) {
	f := &fakeRunner{psOut: []byte("past\t500\trun-a\t\t\nfuture\t5000\trun-b\t\t\n")}
	reaped, err := Sweep(context.Background(), f.run, 1000, "")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 || reaped[0].ID != "past" {
		t.Errorf("reaped = %+v, want [past]", reaped)
	}
	if !reflect.DeepEqual(f.killed, []string{"past"}) {
		t.Errorf("killed = %v, want [past]", f.killed)
	}
}

func TestSweep_ListErrorPropagates(t *testing.T) {
	f := &fakeRunner{psErr: errors.New("ssh down")}
	_, err := Sweep(context.Background(), f.run, 1000, "")
	if err == nil {
		t.Fatal("Sweep: want error when list fails, got nil")
	}
}

// TestSweep_ContinuesPastAlreadyGoneContainer verifies that a Kill error for a
// container that is no longer present is treated as benign — Sweep continues to
// kill remaining overdue containers and returns nil error (#104).
func TestSweep_ContinuesPastAlreadyGoneContainer(t *testing.T) {
	// Two overdue containers: A (already gone) and B (still needs killing).
	listOut := []byte("containerA\t500\trun-a\t\t\ncontainerB\t500\trun-b\t\t\n")

	var killCalls []string

	run := func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "ps":
			// Distinguish the List call (has --format) from the isRunning call
			// (has --filter id=...).
			for _, a := range args {
				if strings.HasPrefix(a, "id=containerA") {
					// isRunning check for A: return empty (already gone).
					return []byte(""), nil
				}
				if strings.HasPrefix(a, "id=containerB") {
					// isRunning check for B: should not be called (kill B succeeds).
					return []byte("containerB"), nil
				}
			}
			// List call — return both containers as overdue.
			return listOut, nil
		case "kill":
			id := args[len(args)-1]
			killCalls = append(killCalls, id)
			if id == "containerA" {
				return nil, errors.New("exit status 1")
			}
			return nil, nil
		}
		return nil, nil
	}

	reaped, err := Sweep(context.Background(), run, 1000, "")
	if err != nil {
		t.Fatalf("Sweep: want nil error, got: %v", err)
	}
	if len(reaped) != 2 {
		t.Errorf("reaped len = %d, want 2; reaped = %+v", len(reaped), reaped)
	}
	// Verify kill B was actually attempted (sweep did NOT abort after A's error).
	var killedB bool
	for _, id := range killCalls {
		if id == "containerB" {
			killedB = true
		}
	}
	if !killedB {
		t.Errorf("kill containerB was not called; killCalls = %v", killCalls)
	}
}

// TestSweep_ReturnsErrorWhenKillFailsAndStillRunning verifies that a Kill error
// for a container that is still present is propagated as a genuine failure, and
// the overdue slice is still returned (#104).
func TestSweep_ReturnsErrorWhenKillFailsAndStillRunning(t *testing.T) {
	listOut := []byte("containerA\t500\trun-a\t\t\n")

	run := func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "ps":
			for _, a := range args {
				if strings.HasPrefix(a, "id=containerA") {
					// isRunning check for A: still running.
					return []byte("containerA"), nil
				}
			}
			return listOut, nil
		case "kill":
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}

	reaped, err := Sweep(context.Background(), run, 1000, "")
	if err == nil {
		t.Fatal("Sweep: want non-nil error when kill fails and container still running, got nil")
	}
	if len(reaped) != 1 || reaped[0].ID != "containerA" {
		t.Errorf("reaped = %+v, want [{containerA ...}]", reaped)
	}
}
