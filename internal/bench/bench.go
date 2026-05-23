package bench

// Local is the Bench.Host value indicating the Local Engine should use
// the Dev Workstation's own docker daemon (no DOCKER_HOST env var).
const Local = "local"

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

	// OS is the Bench's OS family: "linux", "windows", or
	// "macos-container" (future per ADR-0001).
	OS string
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
