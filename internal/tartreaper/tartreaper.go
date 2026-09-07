// Package tartreaper implements the Tart-side analogue of internal/reaper's
// Tier-2 "reap on next contact" sweep (ADR-0021 Decision 2), for Tart VMs
// started by internal/tartpipeline instead of Docker containers.
//
// Tart has no label/tag mechanism at all: `tart get <name> --format json` on
// a real, live-hardware-confirmed clone returns only Display, Memory, OS,
// Disk, Size, DiskFormat, State, CPU, Running — no metadata field (see
// docs/adr/0030-macos-bench-via-tart.md's reaper decision). So there is no
// way to attribute a leaked VM to a branch/run/deadline the way a Docker
// --label does. internal/tartpipeline.Pipeline.StartContainer works around
// this with a side-channel: it REQUIRES (not best-effort) writing a small
// JSON state file to the Bench, under StateDir, keyed by VM name, before it
// is willing to consider the VM "started" for cleanup purposes.
//
// BY-CONSTRUCTION INVARIANT this package depends on: because that state-file
// write is required, not best-effort, and tartpipeline.Pipeline.Cleanup
// unconditionally stops+deletes the VM (in the SAME run, immediately) if any
// later StartContainer sub-step fails, a Tart VM can only ever be left
// registered on the Bench past its own pipeline's lifetime if the state-file
// write SUCCEEDED. Equivalently: any gsd-tart-* VM found with NO readable
// state file did not leak via the normal path this design defends against —
// it either predates this feature, reflects external tampering, or reflects
// a race this design deliberately accepts. Sweep therefore NEVER touches
// such a VM automatically (Overdue unconditionally excludes HasState=false
// entries); an operator who wants one gone must `tart stop`/`tart delete` it
// by hand.
package tartreaper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/reaper"
	"github.com/open-gsd/gsd-test-runner/internal/tartexec"
)

// StateDir is the Bench-side directory holding one JSON state file per Tart
// VM (named "<vmName>.json"), written by internal/tartpipeline.StartContainer
// and read here. This is the single source of truth for the path — both
// tartpipeline and tartreaper import it from here so the two packages can
// never drift apart on where the state file lives.
const StateDir = "/tmp/gsd-test-tart-state"

// vmNamePrefix is the exact prefix internal/tartpipeline.deriveVMName always
// produces ("gsd-tart-" + cell/runID), duplicated here as a literal (rather
// than imported — tartpipeline does not export it, and importing tartpipeline
// from tartreaper would invert the intended dependency direction, since
// tartpipeline is the one that will import tartreaper's StateDir) with this
// comment as the cross-reference. List filters on this prefix, never on the
// JSON `Source` field (Source is confirmed present in real `tart list`
// output but its value for a cloned run VM is unconfirmed — likely "Local" —
// so filtering on Name is the only unambiguous signal).
const vmNamePrefix = "gsd-tart-"

// VM is a Tart VM observed on a Bench via `tart list`, combined with its
// (possibly absent) state-file contents.
type VM struct {
	Name       string
	RunID      string
	BranchSlug string
	DeadlineMs int64
	// HasState is false when the state file was missing or failed to parse.
	// RunID/BranchSlug/DeadlineMs are zero values in that case, and Overdue
	// unconditionally excludes such VMs — see the package doc comment.
	HasState bool
}

// runTart is a package-level stubbable var over tartexec.RunTart, mirroring
// internal/tartpipeline's own runTart var pattern, so List/Sweep are testable
// without real SSH/network.
var runTart = tartexec.RunTart

// runShell is a package-level stubbable var over tartexec.RunShell, mirroring
// runTart above, used for reading (`cat`) and removing (`rm -f`) state files.
var runShell = tartexec.RunShell

// tartListEntry is the shape of one element of `tart list --format json`'s
// output array — CONFIRMED (not guessed) real captured output, per this
// package's doc comment and docs/adr/0030-macos-bench-via-tart.md: Name,
// Running, State, Source, Disk, Size, Accessed. Only Name matters to List;
// the rest are decoded for documentation/completeness and possible future
// use, not consulted here.
type tartListEntry struct {
	Name     string `json:"Name"`
	Running  bool   `json:"Running"`
	State    string `json:"State"`
	Source   string `json:"Source"`
	Disk     int64  `json:"Disk"`
	Size     int64  `json:"Size"`
	Accessed string `json:"Accessed"`
}

// stateFile is the JSON shape internal/tartpipeline.StartContainer writes to
// StateDir/<vmName>.json.
type stateFile struct {
	RunID      string `json:"run_id"`
	BranchSlug string `json:"branch_slug"`
	DeadlineMs int64  `json:"deadline_ms"`
}

// List returns every gsd-tart-*-prefixed VM currently present on the Bench
// (per `tart list --format json`), each combined with its state-file contents
// when readable. A missing or corrupt state file for one VM sets that VM's
// HasState=false — it never fails the whole List call, so one bad/missing
// state file can't abort the sweep for every other VM on the Bench.
func List(ctx context.Context, b bench.Bench) ([]VM, error) {
	out, err := runTart(ctx, b, []string{"list", "--format", "json"})
	if err != nil {
		return nil, fmt.Errorf("tartreaper: list VMs: %w", err)
	}

	var entries []tartListEntry
	trimmed := strings.TrimSpace(out)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, fmt.Errorf("tartreaper: parse tart list output: %w", err)
		}
	}

	vms := make([]VM, 0, len(entries))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, vmNamePrefix) {
			continue
		}
		vm := VM{Name: e.Name}
		if sf, ok := readState(ctx, b, e.Name); ok {
			vm.RunID = sf.RunID
			vm.BranchSlug = sf.BranchSlug
			vm.DeadlineMs = sf.DeadlineMs
			vm.HasState = true
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// readState reads and parses StateDir/<vmName>.json via runShell. Returns
// ok=false on any read or parse failure (missing file, unreadable, invalid
// JSON) — deliberately swallowed here, not propagated, per List's doc
// comment: a single bad state file must never abort the whole sweep.
func readState(ctx context.Context, b bench.Bench, vmName string) (stateFile, bool) {
	path := StateDir + "/" + vmName + ".json"
	out, err := runShell(ctx, b, "cat "+shellQuote(path))
	if err != nil {
		return stateFile{}, false
	}
	var sf stateFile
	if err := json.Unmarshal([]byte(out), &sf); err != nil {
		return stateFile{}, false
	}
	return sf, true
}

// shellQuote duplicates tartexec's unexported helper of the same name
// (byte-for-byte identical: wrap in single quotes, escape embedded single
// quotes). Needed here because readState and Sweep build their own remote
// shell command strings for tartexec.RunShell, the same reason
// internal/tartpipeline duplicates it — see that package's shellQuote doc
// comment.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tartGetEntry is the shape of `tart get <name> --format json`'s single-object
// output — CONFIRMED (not guessed) real captured output, per this package's
// doc comment and docs/adr/0030-macos-bench-via-tart.md Decision 5:
// Display, Memory, OS, Disk, Size, DiskFormat, State, CPU, Running. Only
// Running/State matter to Probe; a smaller struct (not tartListEntry, whose
// Name/Source/Accessed fields `tart get` doesn't even return) is used here
// since the two commands' output shapes are only partially overlapping.
type tartGetEntry struct {
	Running bool   `json:"Running"`
	State   string `json:"State"`
}

// VMNotFoundError is returned by Probe when the named VM no longer exists on
// the Bench at all (Tart's confirmed "does not exist" signal — exit code 2,
// the same isAlreadyGone match stop/delete already rely on). Distinct from a
// generic probe failure so a caller can errors.As it specifically and word
// its message as "gone," not just "probe failed" — a VM that has vanished
// entirely (reaped, crashed and cleaned up) is a materially different, more
// alarming signal than one that is merely State != "running".
type VMNotFoundError struct {
	Name string
}

func (e *VMNotFoundError) Error() string {
	return fmt.Sprintf("tartreaper: VM %q does not exist", e.Name)
}

// Probe runs `tart get <vmName> --format json` on the Bench (via the runTart
// stubbable var, the same seam List/Sweep already use) and reports whether
// the VM is currently running, plus its raw State string. This is a
// targeted, single-VM liveness check — cheaper and more directly aimed at
// "is this one VM still alive" than List's `tart list`, which enumerates
// every VM on the Bench.
//
// On success, returns (running, state, nil). On failure, isAlreadyGone
// (exit code 2) is distinguished from any other failure: a gone VM returns
// (false, "", *VMNotFoundError), any other error returns (false, "", err)
// wrapping the underlying error generically.
func Probe(ctx context.Context, b bench.Bench, vmName string) (running bool, state string, err error) {
	out, err := runTart(ctx, b, []string{"get", vmName, "--format", "json"})
	if err != nil {
		if isAlreadyGone(err) {
			return false, "", &VMNotFoundError{Name: vmName}
		}
		return false, "", fmt.Errorf("tartreaper: probe %s: %w", vmName, err)
	}

	var entry tartGetEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
		return false, "", fmt.Errorf("tartreaper: parse tart get output for %s: %w", vmName, err)
	}
	return entry.Running, entry.State, nil
}

// Overdue returns, in input order, the VMs whose deadline is at or before
// nowMs — mirroring reaper.Overdue exactly, with one addition: VMs with
// HasState=false are unconditionally excluded first (an unlabeled VM has no
// deadline to be "overdue" against — see the package doc comment; this is
// deliberate, by-construction-safe behavior, not a gap to fill later).
func Overdue(vms []VM, nowMs int64) []VM {
	var out []VM
	for _, v := range vms {
		if !v.HasState {
			continue
		}
		if v.DeadlineMs == 0 {
			continue
		}
		if v.DeadlineMs <= nowMs {
			out = append(out, v)
		}
	}
	return out
}

// OwnedBy returns, in input order, the HasState=true VMs whose BranchSlug
// matches branchSlug — mirroring reaper.OwnedBy exactly. An empty branchSlug
// returns every HasState=true VM (the same operator-escape-hatch semantics
// reaper.OwnedBy documents); no separate unscoped-magic handling is needed
// for HasState=false VMs since Overdue already excludes them unconditionally.
func OwnedBy(vms []VM, branchSlug string) []VM {
	var out []VM
	for _, v := range vms {
		if !v.HasState {
			continue
		}
		if branchSlug == "" || v.BranchSlug == branchSlug {
			out = append(out, v)
		}
	}
	return out
}

// Sweep lists gsd-tart-* VMs and reaps the union of two tiers, exactly
// mirroring reaper.Sweep's two-tier shape:
//
//  1. Branch-scoped: VMs owned by branchSlug (empty branchSlug = every
//     HasState=true VM) whose deadline has passed at nowMs.
//  2. Safety net: ANY HasState=true VM (any branch, unfiltered by
//     branchSlug) whose deadline passed more than reaper.SafetyNetGrace ago.
//     reaper.SafetyNetGrace is imported and reused directly from
//     internal/reaper, not redefined here, so the grace period can never
//     silently drift between the two runtimes' reapers.
//
// The two sets are unioned, deduped by VM.Name. For each VM in the union:
// `tart stop <name>` then `tart delete <name>` (matching
// tartpipeline.Cleanup's exact stop-then-delete order), then a best-effort
// `rm -f` of its state file (errors discarded). Tolerates Tart's confirmed
// "already gone" signal — stop/delete exiting 2 — by matching
// *tartexec.ExecError.ExitCode == 2 (not stderr text, which is fragile) and
// suppressing that specific error, continuing to the next VM. A genuine
// failure (any other exit code, or a non-*tartexec.ExecError error) is
// collected and returned via errors.Join, matching reaper.Sweep's exact
// error-collection shape: all remaining VMs in the batch are still attempted
// even if one fails.
func Sweep(ctx context.Context, b bench.Bench, nowMs int64, branchSlug string) ([]VM, error) {
	vms, err := List(ctx, b)
	if err != nil {
		return nil, err
	}
	owned := OwnedBy(vms, branchSlug)
	branchScoped := Overdue(owned, nowMs)
	safetyNet := Overdue(vms, nowMs-int64(reaper.SafetyNetGrace/time.Millisecond))
	overdue := unionByName(branchScoped, safetyNet)

	var errs []error
	for _, v := range overdue {
		if err := stopAndDelete(ctx, b, v.Name); err != nil {
			errs = append(errs, err)
		}
		statePath := StateDir + "/" + v.Name + ".json"
		_, _ = runShell(ctx, b, "rm -f "+shellQuote(statePath))
	}
	if len(errs) > 0 {
		return overdue, errors.Join(errs...)
	}
	return overdue, nil
}

// stopAndDelete runs `tart stop <name>` then `tart delete <name>`, tolerating
// Tart's confirmed "already gone" signal (exit code 2) on either call by
// suppressing that specific error and proceeding. Any other failure is
// returned.
func stopAndDelete(ctx context.Context, b bench.Bench, name string) error {
	if _, err := runTart(ctx, b, []string{"stop", name}); err != nil && !isAlreadyGone(err) {
		return fmt.Errorf("tartreaper: stop %s: %w", name, err)
	}
	if _, err := runTart(ctx, b, []string{"delete", name}); err != nil && !isAlreadyGone(err) {
		return fmt.Errorf("tartreaper: delete %s: %w", name, err)
	}
	return nil
}

// isAlreadyGone reports whether err is Tart's confirmed "does not exist"
// signal: a *tartexec.ExecError with ExitCode == 2. Matched on exit code, not
// stderr text (text matching is fragile) — see the package doc comment for
// the empirical confirmation (`tart stop`/`tart delete <name-that-does-not-
// exist>` both exit 2; delete additionally prints a stderr message).
func isAlreadyGone(err error) bool {
	var ee *tartexec.ExecError
	if !errors.As(err, &ee) {
		return false
	}
	return ee.ExitCode == 2
}

// unionByName returns a, followed by any VMs in b whose Name is not already
// present in a, preserving each input slice's own relative order — the exact
// dedup shape reaper.unionByID uses, adapted to VM.Name (Tart VMs have no
// separate ID the way Docker containers do; Name is the unique key).
func unionByName(a, b []VM) []VM {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a))
	out := make([]VM, 0, len(a)+len(b))
	for _, v := range a {
		seen[v.Name] = true
		out = append(out, v)
	}
	for _, v := range b {
		if seen[v.Name] {
			continue
		}
		seen[v.Name] = true
		out = append(out, v)
	}
	return out
}
