package dockerexec

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// LineHandler is called once per line of subprocess output. Two handlers
// passed to Stream — one for stdout, one for stderr — let callers distinguish
// streams (for EventChildOutput's Stream field). Lines are passed without
// trailing newlines.
type LineHandler func(line string)

// Stream runs `docker <args...>` on the bench, calling stdoutLine for each
// line of stdout and stderrLine for each line of stderr in real time.
// Returns the same error shape as Run: nil on success, *ExecError on non-zero
// exit, ctx.Err() on cancellation.
//
// Stdout/stderr are streamed line-by-line via bufio.Scanner. Stream blocks
// until the subprocess exits and the line-reader goroutines drain.
func Stream(ctx context.Context, b bench.Bench, args []string, stdoutLine, stderrLine LineHandler) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if host := b.DockerHost(); host != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+host)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{}, 2)
	go scanLines(stdoutPipe, stdoutLine, done)
	go scanLines(stderrPipe, stderrLine, done)

	runErr := cmd.Wait()
	<-done
	<-done // wait for both scanners to drain

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr == nil {
		return nil
	}

	exitCode := -1
	if ee, ok := runErr.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	}
	return &ExecError{Args: args, ExitCode: exitCode}
}

func scanLines(r io.Reader, handler LineHandler, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		if handler != nil {
			handler(scanner.Text())
		}
	}
}
