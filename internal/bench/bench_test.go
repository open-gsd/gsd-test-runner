package bench

import (
	"strings"
	"testing"
)

func TestBenchDockerError_Error(t *testing.T) {
	cases := []struct {
		name     string
		err      *BenchDockerError
		wantSubs []string
	}{
		{
			name: "basic fields",
			err: &BenchDockerError{
				Bench:    "bench-linux-1",
				Args:     []string{"image", "inspect", "ghcr.io/foo:v1"},
				Stderr:   "Cannot connect to the Docker daemon",
				ExitCode: 1,
			},
			wantSubs: []string{"bench-linux-1", "image inspect ghcr.io/foo:v1", "exit=1", "Cannot connect to the Docker daemon"},
		},
		{
			name: "trims stderr whitespace",
			err: &BenchDockerError{
				Bench:    "bench-2",
				Args:     []string{"pull"},
				Stderr:   "  daemon not running  \n",
				ExitCode: 125,
			},
			wantSubs: []string{"bench-2", "pull", "exit=125", "daemon not running"},
		},
		{
			name: "empty args",
			err: &BenchDockerError{
				Bench:    "bench-3",
				Args:     []string{},
				Stderr:   "error",
				ExitCode: 2,
			},
			wantSubs: []string{"bench-3", "exit=2", "error"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("Error() = %q, want substring %q", got, sub)
				}
			}
		})
	}
}

func TestDockerHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"empty host", "", ""},
		{"local constant", Local, ""},
		{"simple hostname", "lab-rig-1", "ssh://lab-rig-1"},
		{"user at host", "user@host", "ssh://user@host"},
		{"host with port", "host:2222", "ssh://host:2222"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			b := Bench{Host: tc.host}
			got := b.DockerHost()
			if got != tc.want {
				t.Errorf("DockerHost() = %q, want %q", got, tc.want)
			}
		})
	}
}
