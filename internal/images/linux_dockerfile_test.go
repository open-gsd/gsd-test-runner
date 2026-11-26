package images

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// linuxAptPackages returns the package names on the Linux Tester Image's
// `apt-get install` line(s), parsed from dockerfiles/linux.Dockerfile.
func linuxAptPackages(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "dockerfiles", "linux.Dockerfile"))
	if err != nil {
		t.Fatalf("read linux.Dockerfile: %v", err)
	}
	// Join backslash continuations, then find the install command.
	joined := strings.ReplaceAll(string(raw), "\\\n", " ")
	m := regexp.MustCompile(`apt-get install[^\n&]*`).FindString(joined)
	if m == "" {
		t.Fatal("no `apt-get install` command found in linux.Dockerfile")
	}
	var pkgs []string
	for _, f := range strings.Fields(m)[2:] { // skip "apt-get install"
		if !strings.HasPrefix(f, "-") {
			pkgs = append(pkgs, f)
		}
	}
	return pkgs
}

// Issue #154: consumer bash-block tests need curl and jq; without them the
// suites skip silently and a green verdict did not exercise them.
func TestLinuxDockerfile_InstallsConsumerTools(t *testing.T) {
	have := map[string]bool{}
	for _, p := range linuxAptPackages(t) {
		have[p] = true
	}
	for _, want := range []string{"ca-certificates", "git", "tar", "curl", "jq"} {
		if !have[want] {
			t.Errorf("linux.Dockerfile apt install list is missing %q", want)
		}
	}
}

// --no-install-recommends keeps the image small (issue #154: "keep it").
func TestLinuxDockerfile_KeepsNoInstallRecommends(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "dockerfiles", "linux.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--no-install-recommends") {
		t.Error("linux.Dockerfile must keep --no-install-recommends")
	}
}
