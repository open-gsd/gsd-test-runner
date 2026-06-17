package docscheck

// Regression guard for issue #55: documented install/download URLs must not
// combine GitHub's `/releases/latest/download/` redirect (which resolves to
// whatever tag is *latest*) with a version-pinned binary filename
// (`gsd-test-vX.Y.Z-...`). That pairing 404s the moment `latest` no longer
// equals the pinned version. The correct, self-consistent shape is
// `releases/download/<TAG>/gsd-test-<TAG>-<os>-<arch>`.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// antiPattern matches the self-contradictory "latest redirect + per-asset
// filename" download URL that caused the reported 404s. It deliberately matches
// any `gsd-test` asset under /releases/latest/download/ — the pinned literal
// (`gsd-test-v1.0.0-…`) AND the templated form (`gsd-test-${GSD_TEST_VERSION}-…`)
// are both wrong, because `latest` only ever resolves to one tag's assets. The
// correct shape is releases/download/<TAG>/gsd-test-<TAG>-<os>-<arch>.
var antiPattern = regexp.MustCompile(`releases/latest/download/gsd-test`)

// docFiles returns README.md plus every Markdown file under docs/, excluding
// docs/adr/ (ADRs are historical record, not runnable install instructions).
func docFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	readme := filepath.Join(root, "README.md")
	if _, err := os.Stat(readme); err == nil {
		out = append(out, readme)
	}
	docsDir := filepath.Join(root, "docs")
	err := filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "adr" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	return out
}

func TestDocs_NoLatestDownloadWithPinnedVersion(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, f := range docFiles(t, root) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if antiPattern.MatchString(line) {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s:%d uses the latest-redirect + pinned-version anti-pattern (issue #55): %s\n"+
					"  use releases/download/<TAG>/gsd-test-<TAG>-<os>-<arch> instead", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}
