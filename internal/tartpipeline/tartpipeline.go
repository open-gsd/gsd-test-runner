// Package tartpipeline implements the Tart-backed macOS test pipeline
// (ADR-0030 Decision 6): a separate Pipeline type, not a bench.Runtime
// branch inside internal/pipeline's existing leg methods, because Tart
// composes the same 8 named legs in a materially different order (boot
// requires the worktree already copied in; the image-version sentinel
// requires a booted, reachable guest) than Docker does.
//
// tartpipeline reuses internal/pipeline's exported Event, Leg (+ its
// constants), LegError, and ErrLegSkipped types so the renderer treats a
// Tart run identically to a Docker run — see pipeline.ContainerIdentity's
// doc comment and ADR-0030 Decision 6 for the full "callable the same way"
// contract. It does NOT call any of pipeline.Pipeline's methods, and this
// PR does not modify internal/pipeline, internal/tartexec,
// internal/dockerexec, or internal/bench — only additive code here (plus a
// small additive branch in internal/runner/runner.go and a new
// internal/images/tart.go).
package tartpipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/images"
	"github.com/open-gsd/gsd-test-runner/internal/pipeline"
	"github.com/open-gsd/gsd-test-runner/internal/report"
	"github.com/open-gsd/gsd-test-runner/internal/tartexec"
)

// DefaultMemoryMB is the hard per-VM memory ceiling (ADR-0030 Decision 5)
// applied via `tart set --memory` before boot, playing the same role
// internal/dispatch.DefaultMemory ("2g") plays for Docker Benches — this is
// the direct fix for the recurring "runaway test OOMs the host Mac" failure
// mode described in ADR-0030's Context. Unlike DefaultMemory (a Docker
// --memory string like "2g"), `tart set --memory` takes a bare integer
// megabyte count, so this is an int, not a string.
//
// JUDGMENT CALL: 4096 (4GB) is not copied from an existing named constant —
// dispatch.DefaultMemory is Docker-specific ("2g") and has no direct Tart
// analogue to mirror numerically. 4096 is the exact value ADR-0030 Decision
// 5's Phase 1 hands-on spike used and confirmed working end-to-end
// (`tart set gsd-spike-vm --memory 4096` followed by `tart get --format
// json` confirming the cap took), so it's a real, previously-verified
// number rather than a guess. A real macOS guest carries more baseline
// memory overhead than a Linux container, hence higher than Docker's 2g.
const DefaultMemoryMB = 4096

// defaultIPWaitSeconds bounds the `tart ip <vm> --wait <seconds>` readiness
// probe StartContainer issues after booting. JUDGMENT CALL: no existing
// constant to mirror (Docker has no equivalent "wait for the container to
// become reachable" step — a container is reachable via `docker exec` the
// instant `docker run -d` returns). 60s is chosen to comfortably exceed a
// real macOS guest's boot time while still failing fast enough that a
// genuinely wedged VM doesn't stall the whole run.
const defaultIPWaitSeconds = 60

// mountTag is the tag passed to `tart run --dir <mountTag>:<path>`. Combined
// with guestMountBase below to derive the in-guest worktree path.
const mountTag = "work"

// guestMountBase is the in-guest path at which a `tart run --dir
// work:<path>` share surfaces on a macOS guest. CONFIRMED (not guessed):
// per ADR-0030 Decision 6 / Decision 4's research this session into `tart
// run --dir`'s mount convention, Tart's virtio-fs shares land under
// `/Volumes/My Shared Files/<tag>` inside a macOS guest.
const guestMountBase = `/Volumes/My Shared Files/work`

// sentinelPath is the in-guest path of the image-version sentinel file
// baked by dockerfiles/macos-tart-provision.sh (already merged), read by
// CheckImageVersion. This is the Tart analogue of the OCI
// sh.gsd-test.image-version label Docker's CheckImageVersion reads via
// `docker image inspect` — Tart has no confirmed way to read an OCI label
// back after pull/clone (ADR-0030 Decision 3), so the sentinel has to be an
// in-guest file, readable only once the guest is booted.
const sentinelPath = "/opt/gsd-test/image-version"

// guestJSONLPath is where RunTests points the Reporter's
// --test-reporter-destination: UNDER the mounted worktree directory, so the
// Drain leg's Bench->Workstation scp can pull it straight off the Bench's
// scratch directory (the SAME bytes, via the already-established
// virtio-fs share) without any extra guest->Bench transfer step. See
// Drain's doc comment for the reasoning this shortcut depends on.
const guestJSONLPath = guestMountBase + "/test-events.jsonl"

// Reporter placeholder substitution, mirroring internal/pipeline's
// unexported reporterPathPlaceholder/reporterDestPlaceholder/
// defaultReporterPath/defaultTestCommandArgs. Duplicated rather than
// exported from internal/pipeline per the brief's guidance (small, pure,
// safer to duplicate than to widen that package's public surface for one
// reuse — internal/pipeline is out of scope for modification in this PR).
const (
	reporterPathPlaceholder = "{{REPORTER_PATH}}"
	reporterDestPlaceholder = "{{REPORTER_DEST}}"
	defaultReporterPath     = "/opt/gsd-test/reporter.mjs"
)

var defaultTestCommandArgs = []string{
	"node",
	"--test",
	"--test-reporter={{REPORTER_PATH}}",
	"--test-reporter-destination={{REPORTER_DEST}}",
}

// Package-level stubbable vars over internal/tartexec, per the brief's
// testability constraint: tartpipeline's leg methods call these vars, never
// tartexec.* directly, so tests never need real SSH/network (mirrors
// internal/pipeline's dockerRun/dockerCp/... pattern and
// internal/tartexec's own runSSH/runSCP pattern).
var (
	runTart       = tartexec.RunTart
	execGuest     = tartexec.Exec
	copyToBench   = tartexec.CopyToBench
	copyFromBench = tartexec.CopyFromBench
)

// bootResult mirrors tartexec's own unexported sshResult shape. Duplicated
// here (not imported — it's unexported in tartexec) for runBootDetached's
// use; see runBootDetached's doc comment for why this leg needs its own
// raw SSH invocation instead of going through tartexec.RunTart.
type bootResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	RunErr   error
}

// runSSHRaw is the package-level test seam for runBootDetached's one-off SSH
// invocation. Mirrors tartexec's own runSSH var pattern exactly (same
// exec.CommandContext("ssh", ...) shape, same stdout/stderr capture, same
// exit-code derivation) so this stays recognizable as "the same kind of
// thing tartexec already does," just not reachable through tartexec's
// current exported API.
var runSSHRaw = func(ctx context.Context, sshArgs []string) bootResult {
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	runErr := cmd.Run()
	exitCode := -1
	switch {
	case runErr == nil:
		exitCode = 0
	default:
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	return bootResult{Stdout: stdoutBuf.String(), Stderr: stderrBuf.String(), ExitCode: exitCode, RunErr: runErr}
}

// shellQuote and shellJoinQuoted duplicate tartexec's unexported helpers of
// the same name (byte-for-byte identical behavior: wrap in single quotes,
// escape embedded single quotes). Needed here because runBootDetached and
// the in-guest shell commands built by NpmCI/Build/RunTests construct their
// own remote command strings directly (bypassing tartexec.RunTart/Exec's
// argv-based quoting for the boot-detach case, and needing local
// `sh -c "..."` quoting for the PATH-prefixed in-guest commands in the
// other case) — duplicated rather than exported from tartexec, which this
// PR does not modify.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoinQuoted(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// Pipeline executes the Tart-backed macOS test pipeline against one Bench,
// mirroring internal/pipeline.Pipeline's external shape closely enough that
// internal/runner's single call site becomes a bench.Runtime-conditional
// constructor choice — see ADR-0030 Decision 6.
type Pipeline struct {
	bench           bench.Bench
	image           images.ImageID
	expectedVersion string
	work            string // path to the PR-merged worktree on the Workstation
	nodeMajor       string
	testCommand     []string
	ident           pipeline.ContainerIdentity
	events          chan<- pipeline.Event
	closeOnce       sync.Once

	memoryMB      int
	ipWaitSeconds int
	vmName        string
	scratchPath   string // Bench-side scratch dir, e.g. /tmp/gsd-test-tart-<runID>

	vmStarted   bool // true once StartContainer's boot succeeds; gates Cleanup
	drainedPath string

	result report.Report
}

// New constructs a Pipeline. Mirrors pipeline.New's signature with two
// additions: nodeMajor (JUDGMENT CALL — see the package doc on why Tart
// needs this threaded explicitly, unlike Docker) in place of testCommand's
// neighboring position, and no build-fallback options (Tart has none in
// this PR; see internal/images/tart.go's EnsurePresentTart doc comment).
//
// ident's RunID/Cell derive this Pipeline's Tart VM name and Bench-side
// scratch directory (vmName/scratchPath below) — see those functions' doc
// comments for the naming/uniqueness judgment call.
func New(b bench.Bench, img images.ImageID, expectedVersion string, worktreePath string, testCommand []string, nodeMajor string, events chan<- pipeline.Event, ident pipeline.ContainerIdentity) *Pipeline {
	return &Pipeline{
		bench:           b,
		image:           img,
		expectedVersion: expectedVersion,
		work:            worktreePath,
		nodeMajor:       nodeMajor,
		testCommand:     testCommand,
		ident:           ident,
		events:          events,
		memoryMB:        DefaultMemoryMB,
		ipWaitSeconds:   defaultIPWaitSeconds,
		vmName:          deriveVMName(ident),
		scratchPath:     deriveScratchPath(ident),
		result:          report.New(b.OS, b.Name, string(img), expectedVersion, time.Now().UTC()),
	}
}

// deriveVMName picks a Tart VM name from ident.RunID/Cell.
//
// JUDGMENT CALL: Tart VM names only need to be locally unique on the Bench
// (no `docker ps`-style human-legibility requirement the ADR-0029 branch-
// slug naming exists for, and — see Cleanup's doc comment — no Tier-2
// reaper support for Tart VMs in this first cut, so there's no
// label-matching concern either). Rather than reusing
// runspec.BuildContainerName's full branch-slug + 63-byte-ceiling
// machinery (built for Docker's --name constraints, which Tart's `tart
// clone <name>` does not share), this uses a simpler
// "gsd-tart-<cell>-<runID>" shape: RunID + Cell is sufficient to avoid
// collisions between concurrent cells of the same run on one Bench. Falls
// back to "noid" when RunID is empty (zero ContainerIdentity, as in tests),
// matching runspec.BuildContainerName's own "noid" fallback for the same
// case.
func deriveVMName(ident pipeline.ContainerIdentity) string {
	runID := ident.RunID
	if runID == "" {
		runID = "noid"
	}
	if ident.Cell != "" {
		return fmt.Sprintf("gsd-tart-%s-%s", ident.Cell, runID)
	}
	return fmt.Sprintf("gsd-tart-%s", runID)
}

// deriveScratchPath picks the Bench-side scratch directory CopyWorktree
// copies the worktree into and StartContainer's boot mounts into the guest.
// Must be unique per run (JUDGMENT CALL, per the brief: Tart gives no
// per-run filesystem isolation the way a fresh Docker container does, so
// the scratch path itself is what prevents concurrent runs on one Bench
// from colliding) — derived from the same RunID/Cell as deriveVMName for
// the same collision-avoidance reasoning.
func deriveScratchPath(ident pipeline.ContainerIdentity) string {
	runID := ident.RunID
	if runID == "" {
		runID = "noid"
	}
	if ident.Cell != "" {
		return fmt.Sprintf("/tmp/gsd-test-tart-%s-%s", ident.Cell, runID)
	}
	return fmt.Sprintf("/tmp/gsd-test-tart-%s", runID)
}

// nodePathPrefix builds the `export PATH=...` shell fragment every in-guest
// command is prefixed with. CONFIRMED gotcha (this session's hands-on
// testing): non-interactive SSH sessions on macOS get a minimal PATH that
// does not include Homebrew's node@<major> keg-opt path, even though the
// keg is installed (the Tester Image is baked per-Node-major per
// dockerfiles/macos-tart.pkr.hcl's node_version var) — so every exec needs
// this prefix explicitly rather than relying on a login-shell profile.
func (p *Pipeline) nodePathPrefix() string {
	return fmt.Sprintf("export PATH=/opt/homebrew/opt/node@%s/bin:$PATH", p.nodeMajor)
}

// guestShellCommand wraps cmd (already a complete shell command string) with
// the PATH prefix and a cd into the mounted worktree, returning the argv
// for execGuest ("sh", "-c", "...").
func (p *Pipeline) guestShellCommand(cmd string) []string {
	full := fmt.Sprintf("%s && cd %s && %s", p.nodePathPrefix(), shellQuote(guestMountBase), cmd)
	return []string{"sh", "-c", full}
}

// runTestsCommandArgs mirrors internal/pipeline's unexported
// runTestsCommandArgs method: substitutes the reporter path/destination
// placeholders into either p.testCommand or defaultTestCommandArgs.
// Duplicated logic (see the reporterPathPlaceholder consts' doc comment)
// rather than an exported pipeline helper.
func (p *Pipeline) runTestsCommandArgs() []string {
	command := p.testCommand
	if len(command) == 0 {
		command = defaultTestCommandArgs
	}
	replacer := strings.NewReplacer(
		reporterPathPlaceholder, defaultReporterPath,
		reporterDestPlaceholder, guestJSONLPath,
	)
	args := make([]string, len(command))
	for i, part := range command {
		args[i] = replacer.Replace(part)
	}
	return args
}

// CopyWorktree copies the PR-merged worktree from the Dev Workstation onto
// the Bench's filesystem (scratchPath) via scp, BEFORE StartContainer boots
// the guest — Tart's --dir share must be attached at `tart run` boot time
// (ADR-0030 Decision 6), so copy-in has to happen first, unlike Docker
// where CopyWorktree runs against an already-running container. RunAll
// calls this leg before StartContainer for that reason; both still emit
// their own named leg's events, so the renderer/report shape matches
// Docker's regardless of call order (ADR-0030 Decision 6's "callable the
// same way" requirement).
func (p *Pipeline) CopyWorktree(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegCopyWorktree, func(_ context.Context) (string, error) {
		if p.work == "" {
			return "", &CopyInError{Cause: errors.New("worktreePath is empty")}
		}
		if err := copyToBench(ctx, p.bench, p.work, p.scratchPath); err != nil {
			return "", &CopyInError{Cause: err}
		}
		return "", nil
	})
}

// StartContainer clones the Tart VM from the Tester Image, applies the
// ADR-0030 Decision 5 memory ceiling, boots it detached with the scratch
// directory --dir-mounted (CopyWorktree must have already populated it —
// see CopyWorktree's doc comment), then waits for the guest to become
// SSH-reachable via `tart ip --wait`. All four sub-steps are reported under
// this one leg (ADR-0030 Decision 6: "StartContainer and CopyWorktree are
// not just reorderable for Tart, they're the same operation" — clone/set/
// boot/wait-ip is the Tart analogue of Docker's single `docker run -d`).
func (p *Pipeline) StartContainer(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegStartContainer, func(ctx context.Context) (string, error) {
		if _, err := runTart(ctx, p.bench, []string{"clone", string(p.image), p.vmName}); err != nil {
			return "", &VMStartError{Stage: "clone", VMName: p.vmName, Cause: err}
		}
		// vmStarted flips true here (not after boot) so Cleanup still attempts
		// stop+delete if a later sub-step (set-memory/boot/wait-ip) fails —
		// clone already left a VM registered on the Bench that needs removing.
		p.vmStarted = true

		if _, err := runTart(ctx, p.bench, []string{"set", p.vmName, "--memory", strconv.Itoa(p.memoryMB)}); err != nil {
			return "", &VMStartError{Stage: "set_memory", VMName: p.vmName, Cause: err}
		}

		if err := p.bootDetached(ctx); err != nil {
			return "", &VMStartError{Stage: "boot", VMName: p.vmName, Cause: err}
		}

		out, err := runTart(ctx, p.bench, []string{"ip", p.vmName, "--wait", strconv.Itoa(p.ipWaitSeconds)})
		if err != nil {
			return "", &VMStartError{Stage: "wait_ip", VMName: p.vmName, Cause: err}
		}
		if strings.TrimSpace(out) == "" {
			return "", &VMStartError{Stage: "wait_ip", VMName: p.vmName, Cause: errors.New("tart ip returned empty output (guest never became reachable)")}
		}
		return "", nil
	})
}

// bootDetached issues `tart run <vm> --no-graphics --dir work:<scratchPath>`
// on the Bench as a backgrounded, detached remote process.
//
// JUDGMENT CALL (the brief's option (a), not (b)): `tart run` blocks until
// the VM is stopped, and tartexec.RunTart's contract always waits for the
// remote command to exit — RunTart's implementation builds the remote
// command as the literal string "tart " + shellJoinQuoted(args), with no
// flag or parameter to request backgrounding. Since internal/tartexec is
// out of scope for modification in this PR (merged, stable, other things
// may depend on its exact current contract), this method shells its own
// one-off SSH command directly — same "ssh <host> -- <quoted command>"
// shape RunTart itself uses internally, same runSSH-style stubbable var
// pattern (runSSHRaw above) — rather than trying to force RunTart's
// existing signature to do something it can't express.
//
// This was chosen over option (b) (fire the RunTart call in a goroutine
// and don't wait on it) because: (1) it keeps StartContainer's leg
// completion meaning "the boot command was successfully issued AND
// accepted by the remote shell," not "a goroutine was launched with
// unknown outcome"; (2) it avoids a background goroutine outliving the leg
// whose lifetime/cancellation semantics would need separate reasoning
// (what happens to it if ctx is later canceled? does Cleanup's stop/delete
// race it?); (3) `nohup ... & disown` is the standard, well-understood way
// to background-and-detach a process over a single non-interactive SSH
// command — this is a normal shell idiom, not a workaround. Readiness is
// still confirmed by the following `tart ip --wait` call, exactly as
// option (b) would have needed too.
//
// All three of the backgrounded process's file descriptors are explicitly
// redirected (`< /dev/null > /dev/null 2>&1`), not just stdout/stderr: a
// classic SSH gotcha is that the outer `ssh` invocation hangs waiting for
// the remote session's file descriptors to close, even with `&`/`disown`,
// if the backgrounded child still inherits a pipe connected to the SSH
// session's own stdin. Omitting `< /dev/null` reproduces exactly that hang.
func (p *Pipeline) bootDetached(ctx context.Context) error {
	// --dir's value is a single "tag:path" argument (not two), so it is
	// quoted as one unit rather than quoting mountTag and p.scratchPath
	// separately.
	remoteCmd := fmt.Sprintf(
		"nohup tart run %s --no-graphics --dir %s < /dev/null > /dev/null 2>&1 & disown",
		shellQuote(p.vmName),
		shellQuote(mountTag+":"+p.scratchPath),
	)
	sshArgs := []string{p.bench.Host, "--", remoteCmd}
	res := runSSHRaw(ctx, sshArgs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if res.RunErr == nil {
		return nil
	}
	return &tartexec.ExecError{
		Args:     []string{"run", p.vmName, "--no-graphics", "--dir", mountTag + ":" + p.scratchPath},
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}
}

// CheckImageVersion runs AFTER the guest is booted and reachable (ADR-0030
// Decision 6: Tart has no confirmed way to read an OCI label back after
// pull/clone, unlike Docker's pre-boot `docker image inspect`, so the
// version sentinel must be an in-guest file read via SSH exec). Reads
// sentinelPath and compares it to p.expectedVersion.
//
// JUDGMENT CALL: on a mismatch, this reuses images.ImageVersionMismatch
// (Bench/Image/Want/Got all map directly onto what's available here) rather
// than inventing a Tart-specific mismatch type — the same reasoning
// internal/pipeline's checkImageVersionWork already applies for Docker,
// kept consistent here. A failure to even READ the sentinel (guest
// unreachable, file missing, ssh error) is a materially different failure
// class from "read fine but doesn't match," so that path gets its own typed
// error (SentinelReadError) instead of overloading ImageVersionMismatch's
// Got="" case for something that isn't really "checked and empty."
func (p *Pipeline) CheckImageVersion(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegCheckImageVersion, func(ctx context.Context) (string, error) {
		out, err := execGuest(ctx, p.bench, p.vmName, tartexec.DefaultGuestUser, tartexec.DefaultGuestPassword, []string{"cat", sentinelPath})
		if err != nil {
			return "", &SentinelReadError{VMName: p.vmName, Cause: err}
		}
		got := strings.TrimSpace(out)
		if p.expectedVersion != "" && got != p.expectedVersion {
			return "", &images.ImageVersionMismatch{
				Bench: p.bench.Name,
				Image: string(p.image),
				Want:  p.expectedVersion,
				Got:   got,
			}
		}
		return "", nil
	})
}

// hasBuildScript probes package.json under the mounted worktree inside the
// guest and reports whether a "build" script is defined, mirroring
// internal/pipeline's hasBuildScript. Implemented via a small inline `node
// -e` (Node is confirmed present and on PATH via nodePathPrefix — the image
// bakes it per Node major) rather than `cat package.json` + local JSON
// parsing, since a `cat`+parse round-trip needs no fewer moving parts here
// and this keeps the exit-code convention (0 = has build script, 1 = not)
// self-contained in the guest command rather than requiring a second exec.
func (p *Pipeline) hasBuildScript(ctx context.Context) (bool, error) {
	script := `process.exit(require('./package.json').scripts && require('./package.json').scripts.build ? 0 : 1)`
	args := p.guestShellCommand(fmt.Sprintf("node -e %s", shellQuote(script)))
	_, err := execGuest(ctx, p.bench, p.vmName, tartexec.DefaultGuestUser, tartexec.DefaultGuestPassword, args)
	if err == nil {
		return true, nil
	}
	var ee *tartexec.ExecError
	if errors.As(err, &ee) && ee.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

// execAndCapture runs a guest command via execGuest and emits its captured
// output as EventChildOutput lines AFTER the command returns (no live
// streaming — see RunTests's doc comment for why: this PR explicitly does
// not implement the JSONL-tail-for-real-time-events goroutine
// internal/pipeline.tailJSONLForLiveEvents provides for Docker, since a
// two-hop SSH Exec is not a streaming primitive). Output is split on
// newlines to match the granularity pipeline.streamAndCapture's
// line-by-line emission produces, even though here it all arrives at once.
func (p *Pipeline) execAndCapture(ctx context.Context, leg pipeline.Leg, verb string, args []string) error {
	out, err := execGuest(ctx, p.bench, p.vmName, tartexec.DefaultGuestUser, tartexec.DefaultGuestPassword, args)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		p.emit(pipeline.Event{Kind: pipeline.EventChildOutput, Leg: leg, Line: line, Stream: "stdout"})
	}
	if err != nil {
		var ee *tartexec.ExecError
		if errors.As(err, &ee) {
			for _, line := range strings.Split(ee.Stderr, "\n") {
				if line == "" {
					continue
				}
				p.emit(pipeline.Event{Kind: pipeline.EventChildOutput, Leg: leg, Line: line, Stream: "stderr"})
			}
			return &GuestExecError{Verb: verb, Stdout: ee.Stdout, Stderr: ee.Stderr, ExitCode: ee.ExitCode}
		}
		return &GuestExecError{Verb: verb, Cause: err}
	}
	return nil
}

// NpmCI runs `npm ci` inside the guest, under the mounted worktree.
func (p *Pipeline) NpmCI(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegNpmCI, func(ctx context.Context) (string, error) {
		args := p.guestShellCommand("npm ci")
		return "", p.execAndCapture(ctx, pipeline.LegNpmCI, "npm ci", args)
	})
}

// Build runs `npm run build` inside the guest, mirroring
// internal/pipeline's Build: skipped (ErrLegSkipped) when package.json has
// no "build" script.
func (p *Pipeline) Build(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegBuild, func(ctx context.Context) (string, error) {
		hasScript, err := p.hasBuildScript(ctx)
		if err != nil {
			return "", &GuestExecError{Verb: "npm run build", Cause: err}
		}
		if !hasScript {
			return "no build script defined in package.json", pipeline.ErrLegSkipped
		}
		args := p.guestShellCommand("npm run build")
		return "", p.execAndCapture(ctx, pipeline.LegBuild, "npm run build", args)
	})
}

// RunTests executes the test suite inside the guest.
//
// KNOWN LIMITATION (explicitly out of scope for this PR, per the brief):
// unlike internal/pipeline.Pipeline.RunTests, this does NOT run a
// concurrent JSONL-tail goroutine to emit live EventTestPass/EventTestFail
// events as tests complete. tartexec.Exec is a two-hop SSH request/response
// primitive (issue one command, get one result back) — it is not a
// streaming primitive the way dockerexec.Stream is, so mirroring Docker's
// live-tail behavior would require a materially harder mechanism (e.g. a
// second, concurrent `tail -F` Exec call racing the main test run, or
// polling). Only the final aggregate result is available: Drain+Parse
// after RunTests completes. Renderer consumers watching for real-time
// per-test events will see nothing from a Tart run until the whole suite
// finishes, then the full captured stdout/stderr as one EventChildOutput
// burst (via execAndCapture) followed by Drain/Parse's aggregate
// pass/fail/total counts. This is a real UX gap versus Docker, not a bug.
//
// Test-process exit code 1 (tests failed) is NOT a leg error — mirrors
// internal/pipeline's exit-1 downgrade so the Parse leg surfaces failures
// via Report.Failures instead of a false leg-infra failure.
func (p *Pipeline) RunTests(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegRunTests, func(ctx context.Context) (string, error) {
		testArgs := p.runTestsCommandArgs()
		args := p.guestShellCommand(shellJoinQuoted(testArgs))
		err := p.execAndCapture(ctx, pipeline.LegRunTests, "test runner", args)
		if err != nil {
			var ge *GuestExecError
			if errors.As(err, &ge) && ge.ExitCode == 1 {
				return "", nil
			}
			return "", err
		}
		return "", nil
	})
}

// Drain pulls the JSONL results file from the Bench to the Dev Workstation
// via CopyFromBench (scp).
//
// This relies on a confirmed-by-construction shortcut: RunTests points the
// Reporter's --test-reporter-destination at guestJSONLPath, which is UNDER
// the guest's --dir-mounted worktree (guestMountBase) — the same virtio-fs
// share whose Bench-side backing directory is p.scratchPath. So the
// Reporter's guest-side write is ALREADY visible on the Bench's filesystem
// at p.scratchPath+"/test-events.jsonl" the instant it's written; no
// separate guest->Bench transfer step (that would need an Exec-based copy)
// is needed here, only the one remaining Bench->Workstation hop. This only
// holds because guestJSONLPath is deliberately kept under guestMountBase —
// if RunTests's reporter destination were ever changed to a guest-local
// (non-mounted) path, this leg would need an Exec-based extraction step
// added first.
func (p *Pipeline) Drain(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegDrain, func(_ context.Context) (string, error) {
		f, err := os.CreateTemp("", "gsd-test-tart-jsonl-*.log")
		if err != nil {
			return "", &DrainError{Stage: "create_temp", Cause: err}
		}
		localPath := f.Name()
		f.Close()

		remotePath := p.scratchPath + "/test-events.jsonl"
		if err := copyFromBench(ctx, p.bench, remotePath, localPath); err != nil {
			return localPath, &DrainError{Stage: "scp", Cause: err}
		}

		p.drainedPath = localPath
		return "", nil
	})
}

// Parse converts the JSON Lines stream into structured test events and
// aggregates them into the Pipeline's Report. Identical logic to
// internal/pipeline.Pipeline.Parse (see parse.go's doc comment for why the
// small parseJSONL helper is duplicated rather than imported).
func (p *Pipeline) Parse(ctx context.Context) error {
	return p.runLeg(ctx, pipeline.LegParse, func(_ context.Context) (string, error) {
		if p.drainedPath == "" {
			return "", &ParseError{Cause: errors.New("drain leg did not run or produced no file")}
		}
		f, err := os.Open(p.drainedPath)
		if err != nil {
			return "", &ParseError{Cause: err}
		}
		defer f.Close()

		passed, total, failures, err := parseJSONL(f)
		if err != nil {
			return "", &ParseError{Cause: err}
		}

		p.result.Total = total
		p.result.Passed = passed
		p.result.Failed = len(failures)
		p.result.Failures = failures
		return "", nil
	})
}

// Report returns the per-OS final result. Only meaningful after a
// successful Parse (or after RunAll returns nil).
func (p *Pipeline) Report() report.Report { return p.result }

// DrainedPath returns the local path of the JSONL results file pulled from
// the Bench by Drain (empty until Drain succeeds).
func (p *Pipeline) DrainedPath() string { return p.drainedPath }

// Cleanup stops and deletes the Tart VM if StartContainer got far enough to
// register one (p.vmStarted), mirroring pipeline.Pipeline.Cleanup's
// deferred, best-effort, error-discarding shape.
//
// JUDGMENT CALL: this does NOT also `rm -rf` the Bench-side scratch
// directory (p.scratchPath). Chosen because: the scratch path is already
// per-run-unique (deriveScratchPath), so leaving it behind cannot collide
// with a future run the way a leaked Docker container with a fixed name
// would; and unlike a running VM (which holds memory/CPU/disk-clone
// resources the ADR-0030 Decision 5 memory cap exists specifically to
// bound), an idle directory under /tmp is comparatively low-cost to leave
// for a future cleanup pass. This is NOT parity with Docker's --rm-based
// automatic container cleanup, and there is currently no Tier-2-reaper-style
// sweep for either leaked Tart VMs or their scratch directories — see the
// package's known-limitations list in the PR description.
//
// KNOWN LIMITATION: unlike Docker's ADR-0029 --name/--label scheme (which
// the Tier-2 reaper in internal/reaper sweeps by branch + deadline), Tart
// VMs started by this Pipeline carry no equivalent reaper-visible identity.
// A run that crashes before RunAll's deferred Cleanup fires (process killed,
// host rebooted) leaks a running Tart VM with no automated sweep to catch
// it. This first cut deliberately does not implement Tart VM naming/
// labeling parity with ADR-0029 (see deriveVMName's doc comment) or a
// Tart-aware reaper sweep — both are real gaps flagged for follow-up, not
// silently dropped.
func (p *Pipeline) Cleanup(ctx context.Context) {
	if !p.vmStarted {
		return
	}
	_, _ = runTart(ctx, p.bench, []string{"stop", p.vmName})
	_, _ = runTart(ctx, p.bench, []string{"delete", p.vmName})
}

// RunAll executes all 8 legs, short-circuiting on the first LegError.
// Returns the Report and nil on success, or a zero Report and the LegError
// of the first failed leg.
//
// Leg CALL order deliberately differs from internal/pipeline's (which runs
// CheckImageVersion, StartContainer, CopyWorktree, ...): here it's
// CopyWorktree, StartContainer, CheckImageVersion, ... — see CopyWorktree's
// and CheckImageVersion's doc comments for why. Every leg still emits its
// own named EventLegStart/Success/Failure, so the SET of 8 leg names and
// their event shape matches Docker's; only the order of which leg's events
// appear first differs, exactly as ADR-0030 Decision 6 anticipates
// ("the renderer doesn't care about call order, only which named leg an
// event belongs to").
func (p *Pipeline) RunAll(ctx context.Context) (report.Report, error) {
	defer p.closeEvents()
	defer func() {
		p.Cleanup(context.Background())
	}()

	legs := []func(context.Context) error{
		p.CopyWorktree,
		p.StartContainer,
		p.CheckImageVersion,
		p.NpmCI,
		p.Build,
		p.RunTests,
		p.Drain,
		p.Parse,
	}
	for _, run := range legs {
		if err := run(ctx); err != nil {
			return report.Report{}, err
		}
	}
	p.result.Finalize(time.Now().UTC())
	return p.result, nil
}

// legExitCode maps a pipeline.Leg to its documented exit code
// (pipeline.ExitCode* constants), reusing internal/pipeline's exported
// exit-code table rather than inventing a parallel one — the exit codes are
// leg-identity metadata, not Docker-specific, so there's no reason for Tart
// runs to report different numbers for the same named leg. Returns 0 for an
// unrecognized Leg (defensive; should be unreachable since tartpipeline only
// ever passes its own 8 known Leg constants to runLeg).
func legExitCode(leg pipeline.Leg) int {
	switch leg {
	case pipeline.LegCheckImageVersion:
		return pipeline.ExitCodeCheckImageVersion
	case pipeline.LegCopyWorktree:
		return pipeline.ExitCodeCopyWorktree
	case pipeline.LegStartContainer:
		return pipeline.ExitCodeStartContainer
	case pipeline.LegNpmCI:
		return pipeline.ExitCodeNpmCI
	case pipeline.LegBuild:
		return pipeline.ExitCodeBuild
	case pipeline.LegRunTests:
		return pipeline.ExitCodeRunTests
	case pipeline.LegDrain:
		return pipeline.ExitCodeDrain
	case pipeline.LegParse:
		return pipeline.ExitCodeParse
	}
	return 0
}

// runLeg mirrors internal/pipeline.Pipeline.runLeg's exact
// LegStart/ctx-check/work/LegSuccess/LegFailure/ErrLegSkipped protocol, so
// the event sequence/shape a Tart run produces is indistinguishable (beyond
// which leg's events appear first — see RunAll's doc comment) from a Docker
// run's. Duplicated rather than exported from internal/pipeline (it is
// small and pipeline-instance-specific — it closes over p.emit — so there
// is nothing meaningful to share beyond the protocol shape, which this
// comment documents explicitly).
func (p *Pipeline) runLeg(ctx context.Context, leg pipeline.Leg, work func(context.Context) (string, error)) error {
	p.emit(pipeline.Event{Kind: pipeline.EventLegStart, OS: p.bench.OS, Time: time.Now(), Leg: leg})
	if err := ctx.Err(); err != nil {
		legErr := &pipeline.LegError{Leg: leg, Cause: err, ExitCode: legExitCode(leg)}
		p.emit(pipeline.Event{Kind: pipeline.EventLegFailure, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: legErr.Error()})
		return legErr
	}
	diagPath, err := work(ctx)
	if errors.Is(err, pipeline.ErrLegSkipped) {
		p.emit(pipeline.Event{Kind: pipeline.EventLegSkipped, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: diagPath})
		return nil
	}
	if err != nil {
		var legErr *pipeline.LegError
		if !errors.As(err, &legErr) {
			legErr = &pipeline.LegError{Leg: leg, Cause: err, ExitCode: legExitCode(leg), DiagPath: diagPath}
			err = legErr
		}
		p.emit(pipeline.Event{Kind: pipeline.EventLegFailure, OS: p.bench.OS, Time: time.Now(), Leg: leg, Detail: legErr.Error()})
		return err
	}
	p.emit(pipeline.Event{Kind: pipeline.EventLegSuccess, OS: p.bench.OS, Time: time.Now(), Leg: leg})
	return nil
}

// emit and closeEvents are a deliberately SIMPLER mechanism than
// internal/pipeline's bounded eventQueue + single-pump-goroutine apparatus.
//
// SIMPLIFICATION, DOCUMENTED (per the brief's instruction not to
// under-build silently): pipeline.Pipeline's queue+pump machinery exists to
// give backpressure/bounded-memory protection against a slow consumer under
// HIGH-FREQUENCY concurrent emission — specifically the JSONL-tail
// goroutine emitting a live EventTestPass/EventTestFail per completed test
// while the main leg goroutine is ALSO emitting EventChildOutput lines,
// two producers racing one channel. tartpipeline has no live-tail goroutine
// (see RunTests's doc comment on that explicit scope cut) and therefore
// only ever emits from ONE goroutine (whichever calls RunAll), sequentially,
// one event at a time. There is no concurrent-producer race to guard
// against, so a direct (blocking) channel send is correct and sufficient
// here. The one real behavioral difference from pipeline.Pipeline: a slow
// or stalled events consumer WOULD block a leg here (no bounded queue to
// absorb backpressure), where pipeline.Pipeline's producers never block.
// Given internal/runner's renderer subscribes each stream with a 128-buffer
// channel and drains it continuously, this is judged an acceptable
// simplification for this PR's event volume (no per-test live events), not
// a silent under-build — flagged here explicitly per the brief.
func (p *Pipeline) emit(e pipeline.Event) {
	if p.events == nil {
		return
	}
	p.events <- e
}

// closeEvents closes p.events exactly once. Safe to call multiple times
// (RunAll's defer is the only caller in production; tests may call it too
// for cleanup, mirroring pipeline.Pipeline.closeEvents's idempotency, though
// the mechanism here is sync.Once rather than a queue-close flag).
func (p *Pipeline) closeEvents() {
	if p.events == nil {
		return
	}
	p.closeOnce.Do(func() { close(p.events) })
}
