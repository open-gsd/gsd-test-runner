package reaper

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestParsePS_IncludesNameAndBranch verifies the extended docker ps format
// from ADR-0029: ID, deadline, run-id, name, branch-slug — tab-separated, in
// that order. Missing fields (pre-ADR-0029 containers and Docker's own random
// names absent --name) decode as empty strings without error.
func TestParsePS_IncludesNameAndBranch(t *testing.T) {
	out := []byte("c1\t1000\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n" +
		"c2\t\trun-b\t\t\n" + // pre-ADR-0029: empty name and branch
		"\n" +
		"c3\t500\trun-c\tkeen_euclid\t\n") // Docker-assigned random name, no branch label
	got := parsePS(out)
	want := []Container{
		{ID: "c1", DeadlineMs: 1000, RunID: "run-a", Name: "gsd-test-fix-foo-aaaaaaaa", BranchSlug: "fix-foo"},
		{ID: "c2", DeadlineMs: 0, RunID: "run-b", Name: "", BranchSlug: ""},
		{ID: "c3", DeadlineMs: 500, RunID: "run-c", Name: "keen_euclid", BranchSlug: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePS =\n  %+v\nwant\n  %+v", got, want)
	}
}

// TestOwnedBy_Cases covers the branch-scoped ownership filter used by Sweep.
// The filter matches on the sh.gsd-test.branch label value (BranchSlug),
// NOT on parsing the human-readable Name, so it stays correct even when Docker
// substitutes a random name (pre-ADR-0029 containers, or any future runner
// that sets the label but not the name).
func TestOwnedBy_Cases(t *testing.T) {
	cs := []Container{
		{ID: "a", BranchSlug: "fix-foo"},
		{ID: "b", BranchSlug: "fix-bar"},
		{ID: "c", BranchSlug: ""}, // pre-ADR-0029 or branchless
		{ID: "d", BranchSlug: "fix-foo"},
	}
	tests := []struct {
		name       string
		branchSlug string
		wantIDs    []string
	}{
		{"empty slug returns all (operator escape hatch)", "", []string{"a", "b", "c", "d"}},
		{"slug match includes only matching", "fix-foo", []string{"a", "d"}},
		{"non-matching slug returns none", "fix-baz", nil},
		{"pre-ADR-0029 containers only included when slug empty", "fix-foo", []string{"a", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIDs []string
			for _, c := range OwnedBy(cs, tt.branchSlug) {
				gotIDs = append(gotIDs, c.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("OwnedBy(%q) IDs = %v, want %v", tt.branchSlug, gotIDs, tt.wantIDs)
			}
		})
	}
}

// TestSweep_BranchScoped_ReapsOnlyMatchingBranch is the ADR-0029 headline
// behavior: with a non-empty branchSlug, Sweep reaps only overdue containers
// whose branch label matches; containers from other branches — even when
// overdue — are left for their own invocations to reap.
func TestSweep_BranchScoped_ReapsOnlyMatchingBranch(t *testing.T) {
	// Three overdue containers: fix-foo (ours), fix-bar (other branch), and a
	// pre-ADR-0029 container with no branch label at all.
	psOut := []byte("foo-ctr\t500\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n" +
		"bar-ctr\t500\trun-b\tgsd-test-fix-bar-bbbbbbbb\tfix-bar\n" +
		"legacy\t500\trun-c\t\t\n")
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, 1000, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 || reaped[0].ID != "foo-ctr" {
		t.Errorf("reaped = %+v, want exactly [foo-ctr]", reaped)
	}
	if !reflect.DeepEqual(f.killed, []string{"foo-ctr"}) {
		t.Errorf("killed = %v, want [foo-ctr] — other-branch and pre-ADR-0029 containers must be left alone", f.killed)
	}
}

// TestSweep_EmptyBranchSlug_ReapsAllOverdue verifies the operator escape
// hatch: an empty branchSlug preserves the pre-ADR-0029 "reap every
// labeled+overdue container" behavior, so a future `gsd-test sweep` command
// (or manual operator use) can still clean up legacy leftovers.
func TestSweep_EmptyBranchSlug_ReapsAllOverdue(t *testing.T) {
	psOut := []byte("foo-ctr\t500\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n" +
		"bar-ctr\t500\trun-b\tgsd-test-fix-bar-bbbbbbbb\tfix-bar\n" +
		"legacy\t500\trun-c\t\t\n")
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, 1000, "")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 3 {
		t.Errorf("reaped = %+v, want all 3 (foo, bar, legacy)", reaped)
	}
}

// TestSweep_BranchScoped_LeavesFutureDeadlineAlone verifies that even when
// branch-scoping, the deadline filter still applies: a future-deadline
// container matching the branch is NOT reaped.
func TestSweep_BranchScoped_LeavesFutureDeadlineAlone(t *testing.T) {
	psOut := []byte("future-ctr\t10000\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n")
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, 1000, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 0 {
		t.Errorf("reaped = %+v, want none (deadline not yet passed)", reaped)
	}
	if len(f.killed) != 0 {
		t.Errorf("killed = %v, want none", f.killed)
	}
}

// Compile-time guard: ensure the error variable used by Sweep is in scope for
// the test file (silences unused-import if no test happens to trigger it).
var _ = errors.Join
