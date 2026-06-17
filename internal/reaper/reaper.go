// Package reaper implements the Tier-2 external reaper from ADR-0021
// Decision 2. The Local Engine itself is the reaper: it labels every run
// container with a deadline and, on its next contact with a Bench, kills any
// labeled container whose deadline has passed ("reap on next contact"). This
// gives daemon-like durability with no resident process on the Bench.
//
// This file holds the pure selection logic (Overdue). The Docker-backed sweep
// that lists and kills containers over SSH lives in sweep.go.
package reaper

// Container labels (reverse-DNS, matching the image-version sentinel
// convention from ADR-0011).
const (
	LabelRunID    = "sh.gsd-test.run-id"
	LabelDeadline = "sh.gsd-test.deadline"
)

// Container is a labeled run container observed on a Bench.
type Container struct {
	ID         string
	RunID      string
	DeadlineMs int64 // epoch ms from the sh.gsd-test.deadline label; 0 == unset
}

// Overdue returns, in input order, the containers whose deadline is at or
// before nowMs. Containers with an unset deadline (DeadlineMs == 0) are never
// reaped — only runs that carry an explicit deadline are subject to the sweep.
func Overdue(containers []Container, nowMs int64) []Container {
	var out []Container
	for _, c := range containers {
		if c.DeadlineMs == 0 {
			continue
		}
		if c.DeadlineMs <= nowMs {
			out = append(out, c)
		}
	}
	return out
}
