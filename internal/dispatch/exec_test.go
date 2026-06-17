package dispatch_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
)

// seqRunner returns canned outputs in call order and records every invocation.
type seqRunner struct {
	outs  [][]byte
	calls [][]string
	i     int
}

func (s *seqRunner) run(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{}, args...))
	var out []byte
	if s.i < len(s.outs) {
		out = s.outs[s.i]
	}
	s.i++
	return out, nil
}

func TestExec_CopyInSequence(t *testing.T) {
	r := &seqRunner{outs: [][]byte{
		[]byte("cid123\n"), // create -> container id
		nil,                // cp -> no output
		[]byte(`{"outcome":"completed","exitCode":0}`), // start -a -> envelope
	}}

	out, err := dispatch.Exec(context.Background(), r.run,
		specFor("linux"), "img:v2", "/tmp/worktree", 1_000_000, 60000)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(out) != `{"outcome":"completed","exitCode":0}` {
		t.Errorf("Exec returned %q, want the start envelope", out)
	}
	if len(r.calls) != 3 {
		t.Fatalf("expected 3 docker calls (create, cp, start), got %d: %v", len(r.calls), r.calls)
	}

	// 1) create --rm with caps/labels/image + watchdog command, NOT auto-started.
	create := strings.Join(r.calls[0], " ")
	if r.calls[0][0] != "create" {
		t.Errorf("call 0 verb = %q, want create", r.calls[0][0])
	}
	if !strings.Contains(create, "--rm") || !strings.Contains(create, "img:v2") ||
		!strings.Contains(create, dispatch.EntryScriptLinux) {
		t.Errorf("create args incomplete: %v", r.calls[0])
	}

	// 2) copy the worktree contents into /work (copy-in, not bind-mount; ADR-0002).
	wantCp := []string{"cp", "/tmp/worktree/.", "cid123:/work"}
	if !reflect.DeepEqual(r.calls[1], wantCp) {
		t.Errorf("cp call = %v, want %v", r.calls[1], wantCp)
	}

	// 3) start -a the container by id and stream its stdout.
	wantStart := []string{"start", "-a", "cid123"}
	if !reflect.DeepEqual(r.calls[2], wantStart) {
		t.Errorf("start call = %v, want %v", r.calls[2], wantStart)
	}
}
