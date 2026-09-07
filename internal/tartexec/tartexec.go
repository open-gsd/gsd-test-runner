// Package tartexec runs Tart (Cirrus Labs' Virtualization.framework-based
// macOS VM CLI) commands against a remote Bench, and runs commands inside a
// booted Tart guest VM, both over SSH.
//
// Tart is local-machine-only software: unlike Docker (DOCKER_HOST), there is
// no remote-daemon concept for Apple's Virtualization.framework. Reaching a
// remote Bench's Tart installation means literally SSHing into the Bench and
// running `tart` there (RunTart), rather than pointing a local CLI at a
// remote endpoint via an env var. Running a command inside the guest macOS
// VM itself requires a second, nested SSH hop from the Bench into the guest
// (Exec). See docs/adr/0030-macos-bench-via-tart.md Decision 4.
package tartexec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// DefaultGuestUser and DefaultGuestPassword are Cirrus Labs' own documented
// default SSH account for their prebuilt base images
// (ghcr.io/cirruslabs/macos-tahoe-base and siblings) — empirically confirmed
// working. A future custom gsd-tester-macos-tart image (ADR-0030 Decision 3)
// should rotate or disable this account rather than ship the base image's
// default unchanged; these constants exist so that swap is a one-line change
// here, not a hunt through call sites.
const (
	DefaultGuestUser     = "admin"
	DefaultGuestPassword = "admin"
)

// ExecError is returned by RunTart and Exec when the remote command exits
// non-zero. Args is the command that was run remotely — the tart args for
// RunTart, the in-guest command args for Exec — not the outer ssh argv.
type ExecError struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("tartexec: %s failed (exit=%d): %s",
		strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// sshResult carries the raw outcome of an ssh invocation for the runSSH
// seam. RunErr is nil on success, *exec.ExitError on non-zero remote exit,
// or another error if the local ssh process itself failed to run. Callers
// check ctx.Err() separately to distinguish cancellation from a genuine
// remote failure, matching dockerexec.Run's cancellation handling.
type sshResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	RunErr   error
}

// runSSH is the function used by RunTart and Exec to invoke the local ssh
// binary. Package-level var so tests can stub it without a real network.
var runSSH = func(ctx context.Context, sshArgs []string) sshResult {
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

	return sshResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		RunErr:   runErr,
	}
}

// RunTart executes `tart <args...>` on the given Bench's host over a
// single-hop SSH connection — Tart is local-machine-only software (Apple's
// Virtualization.framework has no remote-daemon concept the way Docker has
// DOCKER_HOST), so reaching a remote Bench's Tart installation means
// literally SSHing into it and running the command there, rather than
// pointing a local CLI at a remote endpoint via an env var. See ADR-0030
// Decision 4.
//
// Returns captured stdout on success. On non-zero exit returns
// (stdout, *ExecError). On ctx cancellation (pre or mid-exec) returns
// ("", ctx.Err()) directly, matching dockerexec.Run's contract.
func RunTart(ctx context.Context, b bench.Bench, args []string) (string, error) {
	remoteCmd := "tart " + shellJoinQuoted(args)
	sshArgs := []string{b.Host, "--", remoteCmd}

	res := runSSH(ctx, sshArgs)

	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	if res.RunErr == nil {
		return res.Stdout, nil
	}

	return res.Stdout, &ExecError{
		Args:     args,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}
}

// Exec runs a command inside the named Tart guest VM's macOS, on the given
// Bench, over a two-hop SSH transport: this process -> the Bench (via
// RunTart's SSH mechanism) -> the guest VM (resolved via `tart ip <vmName>`
// on the Bench, then a password-authenticated SSH hop using sshpass, which
// must be installed on the Bench — `brew install cirruslabs/cli/sshpass`).
// This is the ADR-0030 Decision 4 exec-transport path: `tart exec` was
// tried and found unreliable (fails silently in headless mode against a
// real Cirrus Labs image, root cause unconfirmed); SSH is what Cirrus
// Labs' own reference CI tooling (gitlab-tart-executor) uses, and is what
// this function does too.
//
// Same (string, error) / *ExecError / ctx-cancellation contract as RunTart.
func Exec(ctx context.Context, b bench.Bench, vmName, guestUser, guestPassword string, args []string) (string, error) {
	remoteCmd := fmt.Sprintf(
		"GUEST_IP=$(tart ip %s) && sshpass -p %s ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 %s@$GUEST_IP -- %s",
		shellQuote(vmName),
		shellQuote(guestPassword),
		shellQuote(guestUser),
		shellJoinQuoted(args),
	)
	sshArgs := []string{b.Host, "--", remoteCmd}

	res := runSSH(ctx, sshArgs)

	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	if res.RunErr == nil {
		return res.Stdout, nil
	}

	return res.Stdout, &ExecError{
		Args:     args,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}
}

// runSCP is the function used by CopyToBench and CopyFromBench to invoke
// the local scp binary. Package-level var so tests can stub it without a
// real network, mirroring runSSH — file transfer is a structurally
// different subprocess seam from remote command execution (no remote
// command, no meaningful stdout capture), so it gets its own var rather
// than reusing runSSH, even though both shell out to the same OpenSSH
// binary family and share the sshResult result type.
var runSCP = func(ctx context.Context, scpArgs []string) sshResult {
	cmd := exec.CommandContext(ctx, "scp", scpArgs...)
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

	return sshResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		RunErr:   runErr,
	}
}

// CopyToBench copies a local file or directory to the given Bench's host,
// one hop (this process -> Bench), via scp. Tart is local-machine-only
// software with no DOCKER_HOST-equivalent transparent remote-daemon
// tunneling (that's precisely why RunTart and Exec exist — they SSH into
// the Bench to run `tart` there instead), so getting a file onto the
// Bench's filesystem (e.g. a PR-merged worktree, before it can be
// --dir-mounted into a guest at `tart run` time) needs its own explicit
// transfer step rather than something a local CLI can tunnel implicitly.
// See docs/adr/0030-macos-bench-via-tart.md Decision 6.
//
// Uses `-r` unconditionally: both plain files and directories need to
// work through this one code path (the worktree copy-in is always a
// directory), and `-r` is a no-op-safe superset for a single file with
// OpenSSH's scp.
//
// Returns nil on success. On non-zero scp exit returns *ExecError, with
// Args set to the two logical paths involved ([]string{localPath,
// remotePath}), not the raw scp argv. On ctx cancellation (pre or
// mid-transfer) returns ctx.Err() directly, matching RunTart's contract.
func CopyToBench(ctx context.Context, b bench.Bench, localPath, remotePath string) error {
	scpArgs := []string{"-r", localPath, b.Host + ":" + remotePath}

	res := runSCP(ctx, scpArgs)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if res.RunErr == nil {
		return nil
	}

	return &ExecError{
		Args:     []string{localPath, remotePath},
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}
}

// CopyFromBench copies a file or directory from the given Bench's host to
// a local path, one hop (Bench -> this process), via scp. The
// Drain-equivalent primitive for a Tart-backed pipeline: pulling results
// (e.g. a JSONL results file) back off the Bench once a guest run has
// finished, for the same reason CopyToBench exists — Tart has no
// DOCKER_HOST-equivalent remote-daemon tunneling for a local CLI to ride.
// See docs/adr/0030-macos-bench-via-tart.md Decision 6.
//
// Uses `-r` unconditionally for the same reason as CopyToBench: one code
// path handles both the (always-a-file) JSONL drain-back and any
// directory case, without the caller needing to specify which.
//
// Same (nil on success) / *ExecError (Args = []string{remotePath,
// localPath}, the two logical paths, not the raw scp argv) /
// ctx-cancellation contract as CopyToBench.
func CopyFromBench(ctx context.Context, b bench.Bench, remotePath, localPath string) error {
	scpArgs := []string{"-r", b.Host + ":" + remotePath, localPath}

	res := runSCP(ctx, scpArgs)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if res.RunErr == nil {
		return nil
	}

	return &ExecError{
		Args:     []string{remotePath, localPath},
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}
}

// shellQuote returns s wrapped for safe inclusion as a single literal
// argument on a POSIX shell command line: wrapped in single quotes, with
// any embedded single quote replaced by '\'' (close the quote, emit an
// escaped literal quote, reopen the quote). Used to build the remote
// command strings executed over SSH in RunTart and Exec — args cross a
// shell boundary on the far end, so naive concatenation would let a
// crafted arg (containing e.g. `;`, `$(...)`, or a space) break out of its
// argument position or be reinterpreted by the remote shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoinQuoted quotes each element of args via shellQuote and joins them
// with spaces, producing a single shell-safe command-line fragment.
func shellJoinQuoted(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}
