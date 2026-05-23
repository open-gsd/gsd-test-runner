package bench

import "testing"

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
