package reaper

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Runner executes a docker CLI invocation (args after the `docker` binary) and
// returns its stdout. In production this is backed by internal/dockerexec,
// which shells docker over SSH to the Bench (ADR-0014); in tests it is faked or
// pointed at the local docker daemon.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// psFormat emits one line per run container: ID, deadline label, run-id label,
// tab-separated. .Label yields an empty string when the label is absent.
var psFormat = fmt.Sprintf(`{{.ID}}\t{{.Label %q}}\t{{.Label %q}}`, LabelDeadline, LabelRunID)

// List returns the run containers currently present on the Bench, identified by
// the run-id label. Containers without a parseable deadline label report
// DeadlineMs == 0 (and are therefore never reaped by Sweep).
func List(ctx context.Context, run Runner) ([]Container, error) {
	out, err := run(ctx, "ps", "--no-trunc",
		"--filter", "label="+LabelRunID,
		"--format", psFormat)
	if err != nil {
		return nil, fmt.Errorf("reaper: list containers: %w", err)
	}
	return parsePS(out), nil
}

// parsePS turns `docker ps` tab-separated output into Containers. Blank lines
// are skipped; a missing or non-numeric deadline becomes 0.
func parsePS(out []byte) []Container {
	var cs []Container
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		var c Container
		c.ID = fields[0]
		if len(fields) > 1 {
			if ms, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil {
				c.DeadlineMs = ms
			}
		}
		if len(fields) > 2 {
			c.RunID = fields[2]
		}
		cs = append(cs, c)
	}
	return cs
}

// Kill force-terminates a container by ID. `docker kill` tears down the whole
// container and every process in it identically on Linux and Windows — the
// cross-platform kill primitive from ADR-0021 Decision 4.
func Kill(ctx context.Context, run Runner, id string) error {
	if _, err := run(ctx, "kill", id); err != nil {
		return fmt.Errorf("reaper: kill %s: %w", id, err)
	}
	return nil
}

// Sweep lists run containers, selects those past their deadline at nowMs, kills
// each, and returns the reaped containers. This is the "reap on next contact"
// backstop: any container whose in-container watchdog wedged is still bounded
// because the next Engine contact with the Bench kills it (ADR-0021 D2).
func Sweep(ctx context.Context, run Runner, nowMs int64) ([]Container, error) {
	containers, err := List(ctx, run)
	if err != nil {
		return nil, err
	}
	overdue := Overdue(containers, nowMs)
	for _, c := range overdue {
		if err := Kill(ctx, run, c.ID); err != nil {
			return overdue, err
		}
	}
	return overdue, nil
}
