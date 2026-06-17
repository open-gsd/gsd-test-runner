package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/dispatch"
)

func TestVerifyImageVersion_Match(t *testing.T) {
	var got []string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte("v1.4.0\n"), nil
	}
	if err := dispatch.VerifyImageVersion(context.Background(), runner, "img:v2", "v1.4.0"); err != nil {
		t.Fatalf("VerifyImageVersion: %v", err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "image inspect") || !strings.Contains(joined, "sh.gsd-test.image-version") {
		t.Errorf("inspect command missing sentinel label read: %v", got)
	}
}

func TestVerifyImageVersion_Mismatch(t *testing.T) {
	runner := func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("v1.3.0"), nil
	}
	err := dispatch.VerifyImageVersion(context.Background(), runner, "img:v2", "v1.4.0")
	var mm *dispatch.ImageVersionMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v, want *ImageVersionMismatch", err)
	}
	if mm.Want != "v1.4.0" || mm.Got != "v1.3.0" {
		t.Errorf("mismatch = %+v, want want=v1.4.0 got=v1.3.0", mm)
	}
}

func TestVerifyImageVersion_EmptyWantSkips(t *testing.T) {
	called := false
	runner := func(_ context.Context, _ ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	if err := dispatch.VerifyImageVersion(context.Background(), runner, "img:v2", ""); err != nil {
		t.Fatalf("VerifyImageVersion with empty want: %v", err)
	}
	if called {
		t.Error("empty want should skip the inspect call entirely")
	}
}
