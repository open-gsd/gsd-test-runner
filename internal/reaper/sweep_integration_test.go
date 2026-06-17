package reaper

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// localDockerRunner is a Runner backed by the local `docker` binary — the same
// shape internal/dockerexec provides over SSH, but pointed at the host daemon
// so the reaper can be exercised end-to-end.
func localDockerRunner(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).Output()
}

// requireDocker skips the test when no docker daemon is reachable, keeping
// `go test ./...` green on machines without Docker (the harness's own posture:
// the Dev Workstation orchestrates, Benches run containers).
func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker not available: %v", err)
	}
}

// TestSweep_RealDocker_ReapsOverdueContainer proves the docker ps label format
// and `docker kill` path against a live daemon (ADR-0021 Decision 2/4).
func TestSweep_RealDocker_ReapsOverdueContainer(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	runID := "gsd-test-reaper-it-" + strings.ReplaceAll(t.Name(), "/", "-")
	deadline := time.Now().Add(-time.Minute).UnixMilli() // already overdue

	idOut, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"--label", LabelRunID+"="+runID,
		"--label", LabelDeadline+"="+strconv.FormatInt(deadline, 10),
		"alpine:3", "sleep", "300").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(idOut))
	// Best-effort cleanup if the assertions below fail before the reap.
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	reaped, err := Sweep(ctx, localDockerRunner, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	var found bool
	for _, c := range reaped {
		if strings.HasPrefix(id, c.ID) || strings.HasPrefix(c.ID, id) {
			found = true
		}
	}
	if !found {
		t.Errorf("Sweep did not report our container %s as reaped; got %+v", id, reaped)
	}

	// The container must actually be gone from the daemon.
	psOut, _ := exec.Command("docker", "ps", "-q", "--no-trunc", "--filter", "id="+id).Output()
	if strings.TrimSpace(string(psOut)) != "" {
		t.Errorf("container %s still running after Sweep", id)
	}
}
