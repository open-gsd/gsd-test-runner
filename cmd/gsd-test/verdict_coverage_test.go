package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestRunSubmit_InconclusivePathEmitsVerdict pins the ADR-0023 Option C contract
// for the inconclusive (early-return) path inside runSubmit: when the spec file
// contains invalid JSON, runSubmit returns exitInconclusive (2). The contract
// requires that a machine-readable verdict line is still emitted to stdout in
// EVERY outcome — including this error path.
//
// This test is expected to be RED today, confirming the gap.
func TestRunSubmit_InconclusivePathEmitsVerdict(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Write an invalid JSON spec file.
	dir := t.TempDir()
	specPath := dir + "/bad.json"
	if err := os.WriteFile(specPath, []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("write bad spec: %v", err)
	}

	// Capture stdout via os.Pipe() — required because runSubmit takes *os.File.
	rStdout, wStdout, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	rStderr, wStderr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stderr: %v", err)
	}

	code := runSubmit([]string{"--spec-file", specPath}, wStdout, wStderr)

	// Close write ends so readers reach EOF.
	wStdout.Close()
	wStderr.Close()

	captured, readErr := io.ReadAll(rStdout)
	rStdout.Close()
	rStderr.Close() // drain stderr too; ignore its content

	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}

	if code != exitInconclusive {
		t.Errorf("exit code = %d, want %d (exitInconclusive)", code, exitInconclusive)
	}

	// DESIRED BEHAVIOR (currently failing = RED):
	// stdout must contain at least one line that JSON-parses with "type":"verdict".
	stdoutStr := string(captured)
	lines := strings.Split(strings.TrimRight(stdoutStr, "\n"), "\n")
	var foundVerdict bool
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			if m["type"] == "verdict" {
				foundVerdict = true
				break
			}
		}
	}
	if !foundVerdict {
		t.Errorf("no verdict line found in stdout on inconclusive/early-return path\ncaptured stdout: %q", stdoutStr)
	}
}
