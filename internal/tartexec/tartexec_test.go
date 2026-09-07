package tartexec

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// stubRunSSH replaces the package-level runSSH var with a fake that records
// the call and returns result. Returns the captured call (populated after
// the stubbed function has been invoked) and a restore func.
func stubRunSSH(result sshResult) (captured *struct {
	ctx  context.Context
	args []string
}, restore func()) {
	captured = &struct {
		ctx  context.Context
		args []string
	}{}
	orig := runSSH
	runSSH = func(ctx context.Context, sshArgs []string) sshResult {
		captured.ctx = ctx
		captured.args = sshArgs
		return result
	}
	return captured, func() { runSSH = orig }
}

// --- shellQuote / shellJoinQuoted ---

func TestShellQuote_KnownCorrectOutput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "''"},
		{"simple", "abc", "'abc'"},
		{"only single quote", "'", `''\'''`},
		{"dangerous injection", "'; rm -rf / #", `''\''; rm -rf / #'`},
		{"space", "a b", "'a b'"},
		{"semicolon", "a;b", "'a;b'"},
		{"command substitution", "$(whoami)", "'$(whoami)'"},
		{"unicode", "héllo wörld 世界", "'héllo wörld 世界'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellQuote(tc.input)
			if got != tc.want {
				t.Fatalf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestShellQuote_RealShellRoundTrip proves the quoting algorithm survives a
// real POSIX shell parse: for each adversarial input, `sh -c "printf '%s' "
// + shellQuote(input)` must echo back exactly the original input, unparsed
// and unexecuted. Skips if /bin/sh (or "sh" on PATH) isn't available rather
// than failing — this augments, not replaces, the known-correct-output
// assertions above.
func TestShellQuote_RealShellRoundTrip(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH, skipping real-shell round trip")
	}

	cases := []string{
		"",
		"abc",
		"'",
		"'; rm -rf / #",
		"a b",
		"a;b",
		"$(whoami)",
		"héllo wörld 世界",
		`a"b`,
		"a\\b",
		"a\nb",
	}

	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			quoted := shellQuote(input)
			cmd := exec.Command(shPath, "-c", "printf '%s' "+quoted)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("sh -c failed for input %q (quoted %q): %v", input, quoted, err)
			}
			if string(out) != input {
				t.Fatalf("round trip mismatch: input %q, quoted %q, shell produced %q", input, quoted, string(out))
			}
		})
	}
}

func TestShellJoinQuoted(t *testing.T) {
	got := shellJoinQuoted([]string{"clone", "ghcr.io/cirruslabs/macos-tahoe-base:latest", "my vm"})
	want := "'clone' 'ghcr.io/cirruslabs/macos-tahoe-base:latest' 'my vm'"
	if got != want {
		t.Fatalf("shellJoinQuoted = %q, want %q", got, want)
	}
}

// --- RunTart ---

func TestRunTart_ConstructsOuterSSHArgv(t *testing.T) {
	captured, restore := stubRunSSH(sshResult{Stdout: "ok", ExitCode: 0})
	defer restore()

	b := bench.Bench{Host: "some-host"}
	stdout, err := RunTart(context.Background(), b, []string{"list"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout, "ok")
	}

	wantArgs := []string{"some-host", "--", "tart 'list'"}
	if len(captured.args) != len(wantArgs) {
		t.Fatalf("sshArgs = %#v, want %#v", captured.args, wantArgs)
	}
	for i := range wantArgs {
		if captured.args[i] != wantArgs[i] {
			t.Fatalf("sshArgs[%d] = %q, want %q (full: %#v)", i, captured.args[i], wantArgs[i], captured.args)
		}
	}
}

func TestRunTart_QuotesArgsWithMetacharacters(t *testing.T) {
	captured, restore := stubRunSSH(sshResult{ExitCode: 0})
	defer restore()

	b := bench.Bench{Host: "bench-1"}
	args := []string{"exec", "vm-name", "echo 'it'\"'\"'s; $(whoami)"}
	_, err := RunTart(context.Background(), b, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	remoteCmd := captured.args[2]
	if !strings.HasPrefix(remoteCmd, "tart ") {
		t.Fatalf("remote command %q does not start with 'tart '", remoteCmd)
	}
	// The dangerous arg must survive as a single quoted literal, not be
	// split or left able to break out of its argument position.
	dangerousQuoted := shellQuote(args[2])
	if !strings.Contains(remoteCmd, dangerousQuoted) {
		t.Fatalf("remote command %q does not contain quoted dangerous arg %q", remoteCmd, dangerousQuoted)
	}
}

func TestRunTart_ErrorOnNonZeroExit(t *testing.T) {
	_, restore := stubRunSSH(sshResult{
		Stdout:   "partial",
		Stderr:   "boom",
		ExitCode: 7,
		RunErr:   &exec.ExitError{},
	})
	defer restore()

	b := bench.Bench{Host: "some-host"}
	args := []string{"list"}
	stdout, err := RunTart(context.Background(), b, args)

	if stdout != "partial" {
		t.Fatalf("stdout = %q, want %q", stdout, "partial")
	}

	var execErr *ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("err = %v (%T), want *ExecError", err, err)
	}
	if execErr.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", execErr.ExitCode)
	}
	if execErr.Stdout != "partial" {
		t.Errorf("Stdout = %q, want %q", execErr.Stdout, "partial")
	}
	if execErr.Stderr != "boom" {
		t.Errorf("Stderr = %q, want %q", execErr.Stderr, "boom")
	}
	if len(execErr.Args) != 1 || execErr.Args[0] != "list" {
		t.Errorf("Args = %#v, want [list]", execErr.Args)
	}
}

func TestRunTart_CtxCancellationReturnsCtxErrDirectly(t *testing.T) {
	_, restore := stubRunSSH(sshResult{Stdout: "should be ignored", ExitCode: 0})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling RunTart

	b := bench.Bench{Host: "some-host"}
	stdout, err := RunTart(ctx, b, []string{"list"})

	if stdout != "" {
		t.Errorf("stdout = %q, want empty string on cancellation", stdout)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// --- Exec ---

func TestExec_ConstructsTwoHopCommand_SimpleArgs(t *testing.T) {
	captured, restore := stubRunSSH(sshResult{Stdout: "hi", ExitCode: 0})
	defer restore()

	b := bench.Bench{Host: "bench-1"}
	stdout, err := Exec(context.Background(), b, "gsd-tester", DefaultGuestUser, DefaultGuestPassword, []string{"sw_vers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "hi" {
		t.Fatalf("stdout = %q, want %q", stdout, "hi")
	}

	if captured.args[0] != "bench-1" || captured.args[1] != "--" {
		t.Fatalf("outer sshArgs = %#v, want [bench-1 -- ...]", captured.args)
	}

	remoteCmd := captured.args[2]
	want := "GUEST_IP=$(tart ip 'gsd-tester') && sshpass -p 'admin' ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 'admin'@$GUEST_IP -- 'sw_vers'"
	if remoteCmd != want {
		t.Fatalf("remote command =\n%q\nwant\n%q", remoteCmd, want)
	}
}

func TestExec_QuotesArgsAndVMName(t *testing.T) {
	captured, restore := stubRunSSH(sshResult{ExitCode: 0})
	defer restore()

	b := bench.Bench{Host: "bench-1"}
	vmName := "vm with space; $(rm -rf /)"
	args := []string{"echo", "it's a test; $(whoami)"}
	_, err := Exec(context.Background(), b, vmName, DefaultGuestUser, DefaultGuestPassword, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	remoteCmd := captured.args[2]

	wantVMFragment := "tart ip " + shellQuote(vmName)
	if !strings.Contains(remoteCmd, wantVMFragment) {
		t.Fatalf("remote command %q does not contain quoted vmName fragment %q", remoteCmd, wantVMFragment)
	}

	wantArgFragment := shellQuote(args[0]) + " " + shellQuote(args[1])
	if !strings.Contains(remoteCmd, wantArgFragment) {
		t.Fatalf("remote command %q does not contain quoted args fragment %q", remoteCmd, wantArgFragment)
	}
}

func TestExec_CustomGuestCredentials(t *testing.T) {
	captured, restore := stubRunSSH(sshResult{ExitCode: 0})
	defer restore()

	b := bench.Bench{Host: "bench-1"}
	_, err := Exec(context.Background(), b, "vm-1", "custom-user", "custom-pass; $(id)", []string{"true"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	remoteCmd := captured.args[2]
	if !strings.Contains(remoteCmd, "sshpass -p "+shellQuote("custom-pass; $(id)")) {
		t.Fatalf("remote command %q does not contain quoted custom password", remoteCmd)
	}
	if !strings.Contains(remoteCmd, shellQuote("custom-user")+"@$GUEST_IP") {
		t.Fatalf("remote command %q does not contain quoted custom user @$GUEST_IP", remoteCmd)
	}
}

func TestExec_ErrorOnNonZeroExit(t *testing.T) {
	_, restore := stubRunSSH(sshResult{
		Stdout:   "out",
		Stderr:   "guest command failed",
		ExitCode: 3,
		RunErr:   &exec.ExitError{},
	})
	defer restore()

	b := bench.Bench{Host: "bench-1"}
	args := []string{"false"}
	stdout, err := Exec(context.Background(), b, "vm-1", DefaultGuestUser, DefaultGuestPassword, args)

	if stdout != "out" {
		t.Fatalf("stdout = %q, want %q", stdout, "out")
	}

	var execErr *ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("err = %v (%T), want *ExecError", err, err)
	}
	if execErr.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", execErr.ExitCode)
	}
	if execErr.Stderr != "guest command failed" {
		t.Errorf("Stderr = %q, want %q", execErr.Stderr, "guest command failed")
	}
	if len(execErr.Args) != 1 || execErr.Args[0] != "false" {
		t.Errorf("Args = %#v, want [false]", execErr.Args)
	}
}

func TestExec_CtxCancellationReturnsCtxErrDirectly(t *testing.T) {
	_, restore := stubRunSSH(sshResult{Stdout: "should be ignored", ExitCode: 0})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := bench.Bench{Host: "bench-1"}
	stdout, err := Exec(ctx, b, "vm-1", DefaultGuestUser, DefaultGuestPassword, []string{"true"})

	if stdout != "" {
		t.Errorf("stdout = %q, want empty string on cancellation", stdout)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExecError_ErrorMessage(t *testing.T) {
	e := &ExecError{
		Args:     []string{"list"},
		Stdout:   "out",
		Stderr:   "  boom  \n",
		ExitCode: 7,
	}
	got := e.Error()
	if !strings.Contains(got, "list") || !strings.Contains(got, "exit=7") || !strings.Contains(got, "boom") {
		t.Fatalf("Error() = %q, missing expected substrings", got)
	}
	if strings.Contains(got, "  boom  \n") {
		t.Fatalf("Error() = %q, stderr should be trimmed", got)
	}
}
