package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
)

// writeTOML writes content to a temp file and returns its path.
func writeTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// --- Happy-path tests ---

func TestLoad_MinimalValid(t *testing.T) {
	path := writeTOML(t, `
[defaults]
targets = ["linux", "windows"]
pin = "bench-a"
exclude = ["macos-container"]

[[benches]]
name = "bench-a"
host = "bench-a.local"
os = "linux"

[[benches]]
name = "bench-b"
host = "bench-b.local"
os = "windows"

[[benches]]
name = "bench-c"
host = "bench-c.local"
os = "macos-container"

[versions]
linux = "v1.4.0"
windows = "v1.4.0"

[testing]
command = "npm test -- --test-reporter={{REPORTER_PATH}} --test-reporter-destination={{REPORTER_DEST}}"
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Registry) != 3 {
		t.Errorf("Registry len = %d, want 3", len(cfg.Registry))
	}

	wantBenches := []bench.Bench{
		{Name: "bench-a", Host: "bench-a.local", OS: "linux"},
		{Name: "bench-b", Host: "bench-b.local", OS: "windows"},
		{Name: "bench-c", Host: "bench-c.local", OS: "macos-container"},
	}
	for i, want := range wantBenches {
		if i >= len(cfg.Registry) {
			break
		}
		got := cfg.Registry[i]
		if got != want {
			t.Errorf("Registry[%d] = %+v, want %+v", i, got, want)
		}
	}

	if cfg.Versions["linux"] != "v1.4.0" {
		t.Errorf("Versions[linux] = %q, want %q", cfg.Versions["linux"], "v1.4.0")
	}
	if cfg.Versions["windows"] != "v1.4.0" {
		t.Errorf("Versions[windows] = %q, want %q", cfg.Versions["windows"], "v1.4.0")
	}

	if len(cfg.Defaults.Targets) != 2 {
		t.Errorf("Defaults.Targets len = %d, want 2", len(cfg.Defaults.Targets))
	}
	if cfg.Defaults.Pin != "bench-a" {
		t.Errorf("Defaults.Pin = %q, want %q", cfg.Defaults.Pin, "bench-a")
	}
	if len(cfg.Defaults.Exclude) != 1 || cfg.Defaults.Exclude[0] != "macos-container" {
		t.Errorf("Defaults.Exclude = %v, want [macos-container]", cfg.Defaults.Exclude)
	}
	wantCommand := []string{
		"npm",
		"test",
		"--",
		"--test-reporter={{REPORTER_PATH}}",
		"--test-reporter-destination={{REPORTER_DEST}}",
	}
	if !reflect.DeepEqual(cfg.Testing.Command, wantCommand) {
		t.Errorf("Testing.Command = %v, want %v", cfg.Testing.Command, wantCommand)
	}
}

func TestLoad_TestingCommand_StringQuoted_ParsesShellWords(t *testing.T) {
	path := writeTOML(t, `
[testing]
command = "bash -c 'npm run pretest && node --test tests/*.test.cjs'"
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := []string{
		"bash",
		"-c",
		"npm run pretest && node --test tests/*.test.cjs",
	}
	if !reflect.DeepEqual(cfg.Testing.Command, want) {
		t.Fatalf("Testing.Command = %v, want %v", cfg.Testing.Command, want)
	}
}

func TestLoad_TestingCommand_Array_UsesExplicitArgv(t *testing.T) {
	path := writeTOML(t, `
[testing]
command = ["bash", "-c", "npm run pretest && node --test tests/*.test.cjs"]
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := []string{
		"bash",
		"-c",
		"npm run pretest && node --test tests/*.test.cjs",
	}
	if !reflect.DeepEqual(cfg.Testing.Command, want) {
		t.Fatalf("Testing.Command = %v, want %v", cfg.Testing.Command, want)
	}
}

func TestLoad_TestingCommand_Array_NonStringElementError(t *testing.T) {
	path := writeTOML(t, `
[testing]
command = ["bash", 123]
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "testing.command") {
		t.Fatalf("expected testing.command error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("expected element-type error, got: %v", err)
	}
}

func TestLoad_EmptyBenches(t *testing.T) {
	path := writeTOML(t, `
[defaults]
targets = ["linux"]

[versions]
linux = "v1.0.0"
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Registry) != 0 {
		t.Errorf("Registry len = %d, want 0", len(cfg.Registry))
	}
}

// --- Validation error tests ---

func TestLoad_DuplicateBenchName(t *testing.T) {
	path := writeTOML(t, `
[[benches]]
name = "dup"
host = "host-a"
os = "linux"

[[benches]]
name = "dup"
host = "host-b"
os = "windows"
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ice *InvalidConfigError
	if !errors.As(err, &ice) {
		t.Errorf("error type = %T, want *InvalidConfigError; err: %v", err, err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error = %q, want 'duplicate' mention", err.Error())
	}
}

func TestLoad_MissingName(t *testing.T) {
	path := writeTOML(t, `
[[benches]]
host = "host-a"
os = "linux"
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ice *InvalidConfigError
	if !errors.As(err, &ice) {
		t.Errorf("error type = %T, want *InvalidConfigError; err: %v", err, err)
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error = %q, want 'name is required' mention", err.Error())
	}
}

func TestLoad_MissingOS(t *testing.T) {
	path := writeTOML(t, `
[[benches]]
name = "bench-a"
host = "host-a"
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ice *InvalidConfigError
	if !errors.As(err, &ice) {
		t.Errorf("error type = %T, want *InvalidConfigError; err: %v", err, err)
	}
	if !strings.Contains(err.Error(), "os is required") {
		t.Errorf("error = %q, want 'os is required' mention", err.Error())
	}
}

// --- File-level error tests ---

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml", LoadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

func TestLoad_MalformedTOML(t *testing.T) {
	path := writeTOML(t, `
this is not valid toml ][[[
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Errorf("error = %q, want 'parse config' prefix", err.Error())
	}
}

// --- Default path tests ---

func TestLoad_DefaultPath_XDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Load with empty path — should resolve to $XDG_CONFIG_HOME/gsd-test/config.toml
	// File doesn't exist: we expect a file-not-found wrapped error, not a path computation error.
	_, err := Load("", LoadOptions{})
	if err == nil {
		t.Fatal("expected file-not-found error, got nil")
	}
	wantPath := filepath.Join(dir, "gsd-test", "config.toml")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error = %q, want path %q in message", err.Error(), wantPath)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

func TestLoad_DefaultPath_Home(t *testing.T) {
	// Unset XDG_CONFIG_HOME to force home fallback.
	t.Setenv("XDG_CONFIG_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir:", err)
	}

	_, loadErr := Load("", LoadOptions{})
	if loadErr == nil {
		// Config file happened to exist — that's fine, test the happy path.
		return
	}
	wantPath := filepath.Join(home, ".config", "gsd-test", "config.toml")
	if !strings.Contains(loadErr.Error(), wantPath) {
		t.Errorf("error = %q, want path %q in message", loadErr.Error(), wantPath)
	}
}

// --- Probe tests ---

func TestLoad_NoProbe(t *testing.T) {
	path := writeTOML(t, `
[[benches]]
name = "bench-a"
host = "host-a"
os = "linux"

[[benches]]
name = "bench-b"
host = "host-b"
os = "windows"
`)

	cfg, err := Load(path, LoadOptions{Probe: false})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Registry) != 2 {
		t.Errorf("Registry len = %d, want 2", len(cfg.Registry))
	}
	if len(cfg.Unreachable) != 0 {
		t.Errorf("Unreachable len = %d, want 0", len(cfg.Unreachable))
	}
}

func TestLoad_WithProbe_AllReachable(t *testing.T) {
	// Stub probeRun to succeed for all benches.
	orig := probeRun
	t.Cleanup(func() { probeRun = orig })
	probeRun = func(_ context.Context, _ bench.Bench, _ []string) (string, error) {
		return "20.10.0", nil
	}

	path := writeTOML(t, `
[[benches]]
name = "bench-a"
host = "host-a"
os = "linux"

[[benches]]
name = "bench-b"
host = "host-b"
os = "windows"
`)

	cfg, err := Load(path, LoadOptions{Probe: true})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Registry) != 2 {
		t.Errorf("Registry len = %d, want 2", len(cfg.Registry))
	}
	if len(cfg.Unreachable) != 0 {
		t.Errorf("Unreachable len = %d, want 0; got %+v", len(cfg.Unreachable), cfg.Unreachable)
	}
}

func TestLoad_WithProbe_SomeUnreachable(t *testing.T) {
	// Stub probeRun: fail for "bench-b", succeed for everything else.
	orig := probeRun
	t.Cleanup(func() { probeRun = orig })
	probeRun = func(_ context.Context, b bench.Bench, _ []string) (string, error) {
		if b.Name == "bench-b" {
			return "", fmt.Errorf("ssh: connect timeout")
		}
		return "20.10.0", nil
	}

	path := writeTOML(t, `
[[benches]]
name = "bench-a"
host = "host-a"
os = "linux"

[[benches]]
name = "bench-b"
host = "host-b"
os = "windows"

[[benches]]
name = "bench-c"
host = "host-c"
os = "macos-container"
`)

	cfg, err := Load(path, LoadOptions{Probe: true})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Registry) != 2 {
		t.Errorf("Registry len = %d, want 2", len(cfg.Registry))
	}
	if len(cfg.Unreachable) != 1 {
		t.Errorf("Unreachable len = %d, want 1; got %+v", len(cfg.Unreachable), cfg.Unreachable)
	}
	if len(cfg.Unreachable) == 1 {
		if cfg.Unreachable[0].Bench.Name != "bench-b" {
			t.Errorf("Unreachable[0].Bench.Name = %q, want %q", cfg.Unreachable[0].Bench.Name, "bench-b")
		}
		if cfg.Unreachable[0].Cause == nil {
			t.Error("Unreachable[0].Cause is nil, want non-nil error")
		}
	}

	// Verify reachable benches are the expected two.
	reachableNames := make(map[string]bool)
	for _, b := range cfg.Registry {
		reachableNames[b.Name] = true
	}
	if !reachableNames["bench-a"] {
		t.Error("bench-a missing from Registry")
	}
	if !reachableNames["bench-c"] {
		t.Error("bench-c missing from Registry")
	}
}

func TestLoad_BenchWithRuntimeField(t *testing.T) {
	path := writeTOML(t, `
[[benches]]
name = "bench-macos"
host = "mac-rig.local"
os = "macos"
runtime = "container"
platform = "linux/amd64"

[[benches]]
name = "bench-linux"
host = "linux-rig.local"
os = "linux"
# no runtime field — should default to "" (docker via RuntimeBin)
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Registry) != 2 {
		t.Fatalf("Registry len = %d, want 2", len(cfg.Registry))
	}

	macosBench := cfg.Registry[0]
	if macosBench.Runtime != "container" {
		t.Errorf("Registry[0].Runtime = %q, want %q", macosBench.Runtime, "container")
	}
	if macosBench.OS != "macos" {
		t.Errorf("Registry[0].OS = %q, want %q", macosBench.OS, "macos")
	}
	if macosBench.Platform != "linux/amd64" {
		t.Errorf("Registry[0].Platform = %q, want %q", macosBench.Platform, "linux/amd64")
	}

	linuxBench := cfg.Registry[1]
	if linuxBench.Runtime != "" {
		t.Errorf("Registry[1].Runtime = %q, want empty string (omitted defaults to docker)", linuxBench.Runtime)
	}
}

// --- Table-driven validation tests ---

func TestValidateAndTransform_Errors(t *testing.T) {
	tests := []struct {
		name    string
		raw     rawConfig
		wantErr string
	}{
		{
			name: "empty bench name",
			raw: rawConfig{
				Benches: []rawBench{{Name: "", Host: "h", OS: "linux"}},
			},
			wantErr: "name is required",
		},
		{
			name: "duplicate bench name",
			raw: rawConfig{
				Benches: []rawBench{
					{Name: "x", Host: "h1", OS: "linux"},
					{Name: "x", Host: "h2", OS: "windows"},
				},
			},
			wantErr: "duplicate bench name",
		},
		{
			name: "empty bench os",
			raw: rawConfig{
				Benches: []rawBench{{Name: "b", Host: "h", OS: ""}},
			},
			wantErr: "os is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateAndTransform(tt.raw)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want %q substring", err.Error(), tt.wantErr)
			}
			var ice *InvalidConfigError
			if !errors.As(err, &ice) {
				t.Errorf("error type = %T, want *InvalidConfigError", err)
			}
		})
	}
}

// --- Storage section tests ---

// TestLoad_Storage_ExplicitValues verifies that [storage] fields parse correctly.
func TestLoad_Storage_ExplicitValues(t *testing.T) {
	path := writeTOML(t, `
[storage]
keep_artifacts = true
artifact_ttl = "48h"
keep_last_runs = 5
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if !cfg.Storage.KeepArtifacts {
		t.Errorf("Storage.KeepArtifacts = false, want true")
	}
	wantTTL := 48 * time.Hour
	if cfg.Storage.ArtifactTTL != wantTTL {
		t.Errorf("Storage.ArtifactTTL = %v, want %v", cfg.Storage.ArtifactTTL, wantTTL)
	}
	if cfg.Storage.KeepLastRuns != 5 {
		t.Errorf("Storage.KeepLastRuns = %d, want 5", cfg.Storage.KeepLastRuns)
	}
}

// TestLoad_Storage_Defaults verifies that defaults apply when [storage] is absent.
func TestLoad_Storage_Defaults(t *testing.T) {
	// Config with no [storage] section.
	path := writeTOML(t, `
[[benches]]
name = "bench-a"
host = "host-a"
os = "linux"
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Storage.KeepArtifacts {
		t.Errorf("Storage.KeepArtifacts = true, want false (default)")
	}
	wantTTL := 24 * time.Hour
	if cfg.Storage.ArtifactTTL != wantTTL {
		t.Errorf("Storage.ArtifactTTL = %v, want %v (default)", cfg.Storage.ArtifactTTL, wantTTL)
	}
	if cfg.Storage.KeepLastRuns != 10 {
		t.Errorf("Storage.KeepLastRuns = %d, want 10 (default)", cfg.Storage.KeepLastRuns)
	}
}

// TestLoad_Storage_ArtifactTTL_Disabled verifies that "0" disables the TTL.
func TestLoad_Storage_ArtifactTTL_Disabled(t *testing.T) {
	path := writeTOML(t, `
[storage]
artifact_ttl = "0"
`)

	cfg, err := Load(path, LoadOptions{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Storage.ArtifactTTL != 0 {
		t.Errorf("Storage.ArtifactTTL = %v, want 0 (disabled)", cfg.Storage.ArtifactTTL)
	}
}

// TestLoad_Storage_ArtifactTTL_InvalidDuration verifies a parse error.
func TestLoad_Storage_ArtifactTTL_InvalidDuration(t *testing.T) {
	path := writeTOML(t, `
[storage]
artifact_ttl = "not-a-duration"
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error for invalid artifact_ttl, got nil")
	}
	if !strings.Contains(err.Error(), "storage.artifact_ttl") {
		t.Errorf("error = %q, want 'storage.artifact_ttl' in message", err.Error())
	}
}

// TestLoad_Storage_NegativeKeepLastRuns verifies that a negative value is rejected.
func TestLoad_Storage_NegativeKeepLastRuns(t *testing.T) {
	path := writeTOML(t, `
[storage]
keep_last_runs = -1
`)

	_, err := Load(path, LoadOptions{})
	if err == nil {
		t.Fatal("expected error for negative keep_last_runs, got nil")
	}
	if !strings.Contains(err.Error(), "storage.keep_last_runs") {
		t.Errorf("error = %q, want 'storage.keep_last_runs' in message", err.Error())
	}
}
