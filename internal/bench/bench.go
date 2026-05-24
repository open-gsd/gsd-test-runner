package bench

import (
	"fmt"
	"strings"
)

// Local is the Bench.Host value indicating the Local Engine should use
// the Dev Workstation's own docker daemon (no DOCKER_HOST env var).
const Local = "local"

// Runtime constants select the container CLI binary used by a Bench.
// Set Bench.Runtime to one of these values.
const (
	// RuntimeDocker is the default runtime for Linux and Windows Benches.
	// Invokes the "docker" binary.
	RuntimeDocker = "docker"

	// RuntimeContainer is the Apple Containers runtime for macOS 26+ Benches.
	// Invokes the "container" binary (Apple's native container CLI, distinct
	// from Docker). See ADR-0020.
	RuntimeContainer = "container"
)

// Bench is a remote SSH-reachable machine that runs containerized
// test suites on behalf of a Dev Workstation. One Bench per target
// OS family.
//
// This is a minimal skeleton; real connectivity (SSH config path,
// credentials, container-runtime detection) will grow into this
// struct as implementation progresses.
type Bench struct {
	// Name is the human-readable label, e.g. "bench-linux-1".
	// Appears in event streams and error messages.
	Name string

	// Host is the SSH alias (resolved through ~/.ssh/config) or
	// the literal "local" for benches that are the Dev Workstation
	// itself (uncommon — see CONTEXT.md "Out of scope").
	Host string

	// OS is the Bench's OS family: "linux", "windows", or "macos"
	// (Apple Containers per ADR-0020).
	OS string

	// Runtime selects the container CLI binary for this Bench.
	// Use RuntimeDocker (default, empty string maps to "docker") for Linux
	// and Windows Benches. Use RuntimeContainer ("container") for macOS
	// Benches running Apple Containers (macOS 26+). See ADR-0020 dec 2.
	Runtime string
}

// DockerHost returns the DOCKER_HOST environment variable value for
// reaching this Bench's docker daemon. Returns "" when the bench is
// local (no env var needed).
func (b Bench) DockerHost() string {
	if b.Host == "" || b.Host == Local {
		return ""
	}
	return "ssh://" + b.Host
}

// RuntimeBin returns the CLI binary name for the Bench's container runtime.
// An empty Runtime field defaults to "docker" for backward compatibility.
// RuntimeContainer returns "container" (Apple Containers, macOS 26+).
func (b Bench) RuntimeBin() string {
	if b.Runtime == RuntimeContainer {
		return "container"
	}
	return "docker"
}

// BenchDockerError is returned by any package that invokes docker against
// a Bench and gets a non-image-related infrastructure failure (Bench
// unreachable, daemon down, SSH refused). Replaces the previously-duplicated
// definitions in internal/pipeline and internal/images.
type BenchDockerError struct {
	Bench    string   // bench.Bench.Name
	Args     []string
	Stderr   string
	ExitCode int
}

func (e *BenchDockerError) Error() string {
	return fmt.Sprintf("bench %s: docker %s failed (exit=%d): %s",
		e.Bench, strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}
