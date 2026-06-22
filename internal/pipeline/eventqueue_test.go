package pipeline

import (
	"fmt"
	"sync"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/images"
)

// TestEventQueue_FIFOAndNoDropUnderBurst pushes a large burst while a consumer
// drains and asserts every event arrives, in order. This is the core Option F
// guarantee: nothing is dropped under load (#84).
func TestEventQueue_FIFOAndNoDropUnderBurst(t *testing.T) {
	q := newEventQueue()
	const N = 10000

	got := make([]string, 0, N)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			e, ok := q.pop()
			if !ok {
				return
			}
			got = append(got, e.Line)
		}
	}()

	for i := 0; i < N; i++ {
		q.push(Event{Line: fmt.Sprintf("%d", i)})
	}
	q.close()
	wg.Wait()

	if len(got) != N {
		t.Fatalf("dropped events: got %d, want %d", len(got), N)
	}
	for i := 0; i < N; i++ {
		if got[i] != fmt.Sprintf("%d", i) {
			t.Fatalf("out of order at index %d: got %q", i, got[i])
		}
	}
}

// TestEventQueue_CloseFlushesRemaining verifies close drains what was queued and
// that a push after close is a no-op (a late tail-goroutine emit during
// shutdown must not panic or resurrect the queue).
func TestEventQueue_CloseFlushesRemaining(t *testing.T) {
	q := newEventQueue()
	q.push(Event{Line: "a"})
	q.push(Event{Line: "b"})
	q.close()
	q.push(Event{Line: "c"}) // after close: dropped silently, never delivered

	var got []string
	for {
		e, ok := q.pop()
		if !ok {
			break
		}
		got = append(got, e.Line)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected [a b] flushed, got %v", got)
	}
}

// TestEmit_NeverDropsUnderBurst_ThroughPump exercises the full
// emit -> queue -> pump -> channel path with an UNBUFFERED channel (a maximally
// slow consumer). Every emitted TestFail must still arrive — the regression
// Option F exists to prevent (the old non-blocking emit dropped on a full
// buffer).
func TestEmit_NeverDropsUnderBurst_ThroughPump(t *testing.T) {
	ch := make(chan Event) // unbuffered: the pump must block-and-deliver, not drop
	b := bench.Bench{Name: "burst", Host: "local", OS: "linux"}
	p := New(b, images.ImageID("gsd-tester-linux:dev"), "v", "/tmp/wt", nil, ch)

	const N = 5000
	go func() {
		for i := 0; i < N; i++ {
			p.emit(Event{Kind: EventTestFail, Line: fmt.Sprintf("%d", i)})
		}
		p.closeEvents()
	}()

	got := 0
	for range ch { // ranges until the pump closes ch
		got++
	}
	if got != N {
		t.Fatalf("emit dropped events under burst: got %d, want %d", got, N)
	}
}
