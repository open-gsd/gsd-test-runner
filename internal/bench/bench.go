package bench

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
