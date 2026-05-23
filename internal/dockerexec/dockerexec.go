package dockerexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// ExecError is returned by Run when docker exits non-zero.
type ExecError struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("docker %s failed (exit=%d): %s",
		strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// Run executes `docker <args...>` against the given Bench's docker daemon.
// Returns captured stdout on success. On non-zero exit returns (stdout, *ExecError).
// On ctx cancellation (pre or mid-exec) returns ("", ctx.Err()) directly —
// unifies the dual cancellation path per ADR-0014 dec 4.
func Run(ctx context.Context, b bench.Bench, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if host := b.DockerHost(); host != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+host)
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	stdout := stdoutBuf.String()
	if runErr == nil {
		return stdout, nil
	}

	exitCode := -1
	if ee, ok := runErr.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	}
	return stdout, &ExecError{
		Args:     args,
		Stdout:   stdout,
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}
}
