package pipeline

import (
	"fmt"
	"sync"
)

// defaultEventQueueCap is the default high-water mark for the bounded event
// queue (B-7 fix). When q.items reaches this cap, the oldest EventChildOutput
// entries are evicted and a single EventDroppedOutput marker is injected so the
// consumer knows output was dropped. Failure/result events are never evicted.
// Configurable for tests via newEventQueueWithCap.
const defaultEventQueueCap = 50_000

// eventQueue is a mutex-guarded, bounded FIFO drained by one pump goroutine.
// Producers (emit) never block; if the queue exceeds its high-water cap the
// oldest EventChildOutput events are dropped (bounded-with-marker strategy) and
// a single synthetic EventDroppedOutput marker is emitted so consumers know.
// Failure events (EventTestFail, EventLegFailure, EventDroppedOutput) are never
// dropped — failure-first is lossless. This replaces the prior unbounded queue
// that could grow to OOM under a stalled consumer (#84 B-7 fix, ADR-0017
// amendment). The "bounded backpressure" comment in the old code was misleading
// — producers never actually blocked; this implementation makes the bound real.
type eventQueue struct {
	mu        sync.Mutex
	cond      *sync.Cond
	items     []Event
	closed    bool
	cap       int  // high-water mark; 0 means use defaultEventQueueCap
	dropped   int  // count of EventChildOutput events evicted since last marker
	hasMarker bool // true if a marker for this drop episode is already in items
}

func newEventQueue() *eventQueue {
	return newEventQueueWithCap(defaultEventQueueCap)
}

func newEventQueueWithCap(cap int) *eventQueue {
	q := &eventQueue{cap: cap}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// isLossless reports whether event kind e must never be dropped.
// EventChildOutput is the only droppable kind; all others are lossless.
func isLosslessKind(k EventKind) bool {
	return k != EventChildOutput
}

// push appends e. It is a no-op once the queue is closed (no producers after
// close), so a late tail-goroutine emit during shutdown can never panic.
// B-7 fix: when q.items would exceed the cap, the oldest EventChildOutput
// events are evicted and a synthetic EventDroppedOutput marker is injected
// (once per drop episode). Lossless events (non-EventChildOutput) are always
// appended unconditionally.
func (q *eventQueue) push(e Event) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}

	hwm := q.cap
	if hwm <= 0 {
		hwm = defaultEventQueueCap
	}

	// For droppable events: if we would exceed the cap, evict the oldest
	// EventChildOutput items to make room for both e and a marker (if needed).
	if !isLosslessKind(e.Kind) && len(q.items) >= hwm {
		// Evict from the front: scan for oldest EventChildOutput to remove.
		// We need at least one free slot; keep removing until we have room.
		for len(q.items) >= hwm {
			removed := false
			for i, item := range q.items {
				if item.Kind == EventChildOutput {
					// Shift left.
					copy(q.items[i:], q.items[i+1:])
					q.items[len(q.items)-1] = Event{}
					q.items = q.items[:len(q.items)-1]
					q.dropped++
					removed = true
					break
				}
			}
			if !removed {
				// No EventChildOutput items to evict; just append (lossless-only
				// backpressure doesn't apply — failure events are always kept).
				break
			}
		}
		// Inject a marker if there isn't already one pending.
		if q.dropped > 0 && !q.hasMarker {
			marker := Event{
				Kind:   EventDroppedOutput,
				Leg:    e.Leg,
				OS:     e.OS,
				Detail: fmt.Sprintf("%d output lines dropped due to slow consumer", q.dropped),
			}
			q.items = append(q.items, marker)
			q.hasMarker = true
		} else if q.dropped > 0 && q.hasMarker {
			// Update the existing marker's count.
			for i := len(q.items) - 1; i >= 0; i-- {
				if q.items[i].Kind == EventDroppedOutput {
					q.items[i].Detail = fmt.Sprintf("%d output lines dropped due to slow consumer", q.dropped)
					break
				}
			}
		}
	}

	q.items = append(q.items, e)
	q.mu.Unlock()
	q.cond.Signal()
}

// close marks the queue closed and wakes the pump so it can drain and exit.
// Idempotent.
func (q *eventQueue) close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop returns the next event, blocking until one is available. It returns
// ok=false exactly once the queue is both closed and fully drained.
func (q *eventQueue) pop() (Event, bool) {
	q.mu.Lock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		q.mu.Unlock()
		return Event{}, false
	}
	e := q.items[0]
	q.items[0] = Event{} // release the reference so the backing array can shrink
	q.items = q.items[1:]
	// When the marker is consumed, reset the drop-episode state so the next
	// burst of drops gets its own fresh marker with an accurate count.
	if e.Kind == EventDroppedOutput {
		q.dropped = 0
		q.hasMarker = false
	}
	q.mu.Unlock()
	return e, true
}
