package reaper

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParsePS(t *testing.T) {
	out := []byte("c1\t1000\trun-a\nc2\t\trun-b\n\nc3\tnotanumber\trun-c\n")
	got := parsePS(out)
	want := []Container{
		{ID: "c1", DeadlineMs: 1000, RunID: "run-a"},
		{ID: "c2", DeadlineMs: 0, RunID: "run-b"},
		{ID: "c3", DeadlineMs: 0, RunID: "run-c"},
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
	f := &fakeRunner{psOut: []byte("past\t500\trun-a\nfuture\t5000\trun-b\n")}
	reaped, err := Sweep(context.Background(), f.run, 1000)
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
	_, err := Sweep(context.Background(), f.run, 1000)
	if err == nil {
		t.Fatal("Sweep: want error when list fails, got nil")
	}
}
