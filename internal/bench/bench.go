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

	// RuntimeTart is the Tart runtime for macOS Benches. Invokes the "tart"
	// binary (Cirrus Labs' Virtualization.framework-based CLI), which runs a
	// real macOS guest VM — unlike RuntimeContainer (Apple Containers), which
	// only supports Linux guests and was confirmed and rejected for macOS use
	// in ADR-0030. See ADR-0030.
	RuntimeTart = "tart"
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
	// and Windows Benches. RuntimeContainer ("container") is reserved and
	// unused — Apple Containers only supports Linux guests, not macOS (see
	// ADR-0020, ADR-0030). Use RuntimeTart ("tart") for macOS Benches
	// running Tart, a real macOS-native guest runtime (ADR-0030).
	Runtime string

	// Platform optionally pins the OCI platform for container runs, e.g.
	// "linux/amd64" or "linux/arm64". Empty means runtime default platform
	// selection.
	Platform string

	// Capacity is the max number of Tester containers this Bench runs
	// concurrently. 0 means "unset" -> the runner defaults it to the Bench's
	// own CPU count (docker info NCPU), floored to 1 (enhancement #108). Set
	// explicitly to bound a Bench that also does other work.
	Capacity int
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

// RuntimeBin returns the CLI binary name for this Bench. Defaults to
// "docker" if Runtime is empty.
//
// "container" (Apple Containers) is reserved for a future macOS path that
// requires macOS 26 + a macos-26 GH Actions runner — see ADR-0020 amendment
// (2026-05-24). Until then, macOS Benches use runtime="docker" with Docker
// Desktop or colima providing the container runtime on the Mac.
//
// "tart" (RuntimeTart) is the ADR-0030 path that IS meant to be used for
// macOS-native Bench execution, and is still under active implementation:
// as of this change (#134 Phase 2), only config/selection plumbing exists —
// no actual "tart" binary invocation is wired up anywhere in the codebase
// yet.
func (b Bench) RuntimeBin() string {
	switch b.Runtime {
	case RuntimeContainer:
		return "container"
	case RuntimeTart:
		return "tart"
	default:
		return "docker"
	}
}

// BenchDockerError is returned by any package that invokes docker against
// a Bench and gets a non-image-related infrastructure failure (Bench
// unreachable, daemon down, SSH refused). Replaces the previously-duplicated
// definitions in internal/pipeline and internal/images.
type BenchDockerError struct {
	Bench    string // bench.Bench.Name
	Args     []string
	Stderr   string
	ExitCode int
}

func (e *BenchDockerError) Error() string {
	return fmt.Sprintf("bench %s: docker %s failed (exit=%d): %s",
		e.Bench, strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}
