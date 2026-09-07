package reaper

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
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

// Fixed synthetic "now" for the safety-net tests below, chosen large enough
// that nowMs - SafetyNetGrace stays comfortably positive (avoids any
// int64-underflow edge cases while still using the deterministic synthetic-ms
// convention the rest of this package's tests use — never real wall-clock
// time.Now()).
const safetyNetNowMs = int64(1_700_000_000_000)

// safetyNetGraceMs is SafetyNetGrace expressed in the same millisecond units
// as every Container.DeadlineMs / nowMs value in this package.
var safetyNetGraceMs = int64(SafetyNetGrace / time.Millisecond)

// TestSweep_SafetyNet_LeavesRecentCrossBranchAlone verifies the safety net's
// diagnostic window: a different-branch container whose deadline passed only
// recently (well within SafetyNetGrace) is left alone by both tiers — an
// operator actively diagnosing it on another branch is not surprised
// mid-investigation.
func TestSweep_SafetyNet_LeavesRecentCrossBranchAlone(t *testing.T) {
	recentDeadline := safetyNetNowMs - int64(time.Hour/time.Millisecond) // 1h overdue, << 6h grace
	psOut := []byte(fmt.Sprintf("other-ctr\t%d\trun-x\tgsd-test-fix-bar-xxxxxxxx\tfix-bar\n", recentDeadline))
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, safetyNetNowMs, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 0 {
		t.Errorf("reaped = %+v, want none (within SafetyNetGrace diagnostic window)", reaped)
	}
	if len(f.killed) != 0 {
		t.Errorf("killed = %v, want none", f.killed)
	}
}

// TestSweep_SafetyNet_ReapsCrossBranchPastGrace verifies the safety-net tier
// itself: a different-branch container whose deadline passed more than
// SafetyNetGrace ago IS reaped even though branchSlug doesn't match — this is
// the fix for the cross-branch leak (ADR-0029 §3's branch scoping previously
// left such a container unreaped forever).
func TestSweep_SafetyNet_ReapsCrossBranchPastGrace(t *testing.T) {
	longOverdue := safetyNetNowMs - safetyNetGraceMs - int64(time.Hour/time.Millisecond) // grace + 1h overdue
	psOut := []byte(fmt.Sprintf("other-ctr\t%d\trun-y\tgsd-test-fix-bar-yyyyyyyy\tfix-bar\n", longOverdue))
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, safetyNetNowMs, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 1 || reaped[0].ID != "other-ctr" {
		t.Errorf("reaped = %+v, want exactly [other-ctr]", reaped)
	}
	if !reflect.DeepEqual(f.killed, []string{"other-ctr"}) {
		t.Errorf("killed = %v, want [other-ctr]", f.killed)
	}
}

// TestSweep_SafetyNet_MixedTiers exercises both tiers together in one Sweep
// call: a same-branch overdue container (caught by the branch-scoped tier), a
// cross-branch container overdue past SafetyNetGrace (caught by the safety
// net), and a cross-branch container only recently overdue (caught by
// neither) — asserting exactly the right subset is killed.
func TestSweep_SafetyNet_MixedTiers(t *testing.T) {
	sameBranchOverdue := safetyNetNowMs - 1000                                               // trivially overdue, same branch
	longCrossBranch := safetyNetNowMs - safetyNetGraceMs - int64(time.Hour/time.Millisecond) // past grace, other branch
	recentCrossBranch := safetyNetNowMs - int64(time.Hour/time.Millisecond)                  // within grace, other branch
	psOut := []byte(
		fmt.Sprintf("same-ctr\t%d\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n", sameBranchOverdue) +
			fmt.Sprintf("long-ctr\t%d\trun-b\tgsd-test-fix-bar-bbbbbbbb\tfix-bar\n", longCrossBranch) +
			fmt.Sprintf("recent-ctr\t%d\trun-c\tgsd-test-fix-baz-cccccccc\tfix-baz\n", recentCrossBranch),
	)
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, safetyNetNowMs, "fix-foo")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	gotIDs := make(map[string]bool, len(reaped))
	for _, c := range reaped {
		gotIDs[c.ID] = true
	}
	wantIDs := map[string]bool{"same-ctr": true, "long-ctr": true}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("reaped IDs = %v, want %v (recent-ctr must survive: neither same-branch nor past grace)", gotIDs, wantIDs)
	}

	gotKilled := make(map[string]bool, len(f.killed))
	for _, id := range f.killed {
		gotKilled[id] = true
	}
	if !reflect.DeepEqual(gotKilled, wantIDs) {
		t.Errorf("killed = %v, want %v", gotKilled, wantIDs)
	}
}

// TestSweep_SafetyNet_EmptyBranchSlugIsNoOpSuperset verifies that when
// branchSlug == "" (the existing unscoped operator escape hatch), unioning in
// the safety-net tier is a no-op: the branch-scoped tier already equals
// "everything overdue" (OwnedBy("") returns every container unfiltered), so
// adding the safety-net set changes nothing.
func TestSweep_SafetyNet_EmptyBranchSlugIsNoOpSuperset(t *testing.T) {
	longCrossBranch := safetyNetNowMs - safetyNetGraceMs - int64(time.Hour/time.Millisecond)
	psOut := []byte(
		fmt.Sprintf("a-ctr\t%d\trun-a\tgsd-test-fix-foo-aaaaaaaa\tfix-foo\n", safetyNetNowMs-1000) +
			fmt.Sprintf("b-ctr\t%d\trun-b\tgsd-test-fix-bar-bbbbbbbb\tfix-bar\n", longCrossBranch),
	)
	f := &fakeRunner{psOut: psOut}
	reaped, err := Sweep(context.Background(), f.run, safetyNetNowMs, "")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 2 {
		t.Errorf("reaped = %+v, want both (empty branchSlug already reaps everything overdue)", reaped)
	}
}

// Compile-time guard: ensure the error variable used by Sweep is in scope for
// the test file (silences unused-import if no test happens to trigger it).
var _ = errors.Join
