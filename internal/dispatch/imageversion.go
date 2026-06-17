package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/reaper"
)

// ImageVersionLabel is the OCI sentinel label carrying the Tester Image's
// version (ADR-0011). Reverse-DNS, matching the run-container labels.
const ImageVersionLabel = "sh.gsd-test.image-version"

// ImageVersionMismatch is returned when the image's sentinel label does not
// match the version expected for this repo checkout — the "stale image silently
// produces wrong results" failure class, surfaced loud (ADR-0011/ADR-0004).
type ImageVersionMismatch struct {
	Image string
	Want  string
	Got   string
}

func (e *ImageVersionMismatch) Error() string {
	return fmt.Sprintf("image %s version mismatch: want %q, got %q", e.Image, e.Want, e.Got)
}

// VerifyImageVersion reads the image-version sentinel label via
// `docker image inspect` and checks it equals want. An empty want skips the
// check (no expected version configured). This mirrors the pipeline's
// CheckImageVersion leg for the run-and-die path.
func VerifyImageVersion(ctx context.Context, runner reaper.Runner, imageID, want string) error {
	if want == "" {
		return nil
	}
	out, err := runner(ctx, "image", "inspect", imageID,
		"--format", fmt.Sprintf(`{{ index .Config.Labels %q }}`, ImageVersionLabel))
	if err != nil {
		return fmt.Errorf("dispatch: inspect image version: %w", err)
	}
	got := strings.TrimSpace(string(out))
	if got != want {
		return &ImageVersionMismatch{Image: imageID, Want: want, Got: got}
	}
	return nil
}
