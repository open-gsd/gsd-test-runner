package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/shlex"
	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/dockerexec"
)

// Config is the loaded Local Engine configuration. Produced by Load.
// Inputs the orchestrator and plan.Build need: the Bench registry,
// per-OS expected image versions, CLI flag defaults, optional probe
// results.
type Config struct {
	Registry    []bench.Bench      // reachable Benches (post-probe if --probe-benches)
	Unreachable []UnreachableBench // populated only when --probe-benches
	Targets     []string           // default target OSes from config
	Versions    map[string]string  // OS -> expected image version

	// Node maps OS -> the Node major versions to test against, e.g.
	// {"linux": ["22","24"]}. Empty/absent for an OS -> DefaultNodeLTS().
	Node map[string][]string

	Defaults Defaults
	Testing  Testing
	Storage  Storage
}

// Storage controls run-artifact retention (#102 Option C).
type Storage struct {
	KeepArtifacts bool
	ArtifactTTL   time.Duration // 0 = no TTL bound
	KeepLastRuns  int           // 0 = no count bound
}

// UnreachableBench records a Bench that failed reachability probing.
// Populated by Load when LoadOptions.Probe is true.
type UnreachableBench struct {
	Bench bench.Bench
	Cause error
}

// DefaultNodeLTS returns the Node major versions tested when an OS has no
// explicit [node] entry. These are the currently-supported Node LTS lines
// (see https://github.com/nodejs/Release); bump as the schedule moves.
func DefaultNodeLTS() []string { return []string{"22", "24"} }

// NodeVersionsFor returns the configured Node majors for os, or
// DefaultNodeLTS() when none are configured.
func (c *Config) NodeVersionsFor(os string) []string {
	majors := c.Node[os]
	if len(majors) == 0 {
		return DefaultNodeLTS()
	}
	out := make([]string, len(majors))
	copy(out, majors)
	return out
}

// Defaults are CLI flag defaults parsed from the config file.
type Defaults struct {
	Targets []string
	Pin     string
	Exclude []string
}

// Testing contains optional test command configuration.
type Testing struct {
	Command []string
}

// LoadOptions controls config.Load behavior.
type LoadOptions struct {
	// Probe enables reachability probing of every Bench during Load.
	// Unreachable Benches are removed from Registry and listed in
	// Unreachable. When false (default), Load trusts the registry.
	Probe bool

	// ProbeTimeout is the per-Bench probe timeout. Default 5 seconds.
	ProbeTimeout time.Duration
}

// rawConfig is the on-disk TOML shape, distinct from the validated Config.
// Lets us validate + transform after parsing.
type rawConfig struct {
	Defaults rawDefaults         `toml:"defaults"`
	Benches  []rawBench          `toml:"benches"`
	Versions map[string]string   `toml:"versions"`
	Node     map[string][]string `toml:"node"`
	Testing  rawTesting          `toml:"testing"`
	Storage  rawStorage          `toml:"storage"`
}

type rawStorage struct {
	KeepArtifacts bool   `toml:"keep_artifacts"`
	ArtifactTTL   string `toml:"artifact_ttl"`   // Go duration string, e.g. "24h"; "" = default, "0" = disabled
	KeepLastRuns  int    `toml:"keep_last_runs"` // 0 = disabled (use default 10)
}

type rawDefaults struct {
	Targets []string `toml:"targets"`
	Pin     string   `toml:"pin"`
	Exclude []string `toml:"exclude"`
}

type rawBench struct {
	Name     string `toml:"name"`
	Host     string `toml:"host"`
	OS       string `toml:"os"`
	Runtime  string `toml:"runtime,omitempty"`  // "docker" (default; all benches today) | "container" (reserved for future Apple Containers)
	Platform string `toml:"platform,omitempty"` // optional OCI platform override, e.g. "linux/amd64"
	Capacity int    `toml:"capacity,omitempty"` // max concurrent Tester containers; 0 = unset (runner defaults to the Bench's NCPU, floored to 1)
}

type rawTesting struct {
	Command any `toml:"command"`
}

// probeRun is the function used by probeBenches to test connectivity.
// Package-level var so tests can stub it without spawning docker.
var probeRun = func(ctx context.Context, b bench.Bench, args []string) (string, error) {
	return dockerexec.Run(ctx, b, args)
}

// Load reads, parses, and validates the config file at path.
// If opts.Probe is true, reachable Benches go into Registry and
// unreachable ones go into Unreachable.
//
// Default path resolution: if path is empty, uses
// $XDG_CONFIG_HOME/gsd-test/config.toml or ~/.config/gsd-test/config.toml.
func Load(path string, opts LoadOptions) (*Config, error) {
	if path == "" {
		var err error
		path, err = defaultConfigPath()
		if err != nil {
			return nil, fmt.Errorf("resolve default config path: %w", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var raw rawConfig
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg, err := validateAndTransform(raw)
	if err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	if opts.Probe {
		timeout := opts.ProbeTimeout
		if timeout == 0 {
			timeout = 5 * time.Second
		}
		cfg.Registry, cfg.Unreachable = probeBenches(context.Background(), cfg.Registry, timeout)
	}

	return cfg, nil
}

// defaultConfigPath returns $XDG_CONFIG_HOME/gsd-test/config.toml or
// ~/.config/gsd-test/config.toml.
func defaultConfigPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gsd-test", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gsd-test", "config.toml"), nil
}

// validateAndTransform converts the raw TOML structure into a validated Config.
// Validates: bench name uniqueness, bench OS values, version OS coverage.
func validateAndTransform(raw rawConfig) (*Config, error) {
	seen := make(map[string]bool, len(raw.Benches))
	registry := make([]bench.Bench, 0, len(raw.Benches))
	for i, rb := range raw.Benches {
		if rb.Name == "" {
			return nil, &InvalidConfigError{
				Section: fmt.Sprintf("benches[%d]", i),
				Reason:  "name is required",
			}
		}
		if seen[rb.Name] {
			return nil, &InvalidConfigError{
				Section: fmt.Sprintf("benches[%d]", i),
				Reason:  fmt.Sprintf("duplicate bench name %q", rb.Name),
			}
		}
		seen[rb.Name] = true

		if rb.OS == "" {
			return nil, &InvalidConfigError{
				Section: fmt.Sprintf("benches[%d]", i),
				Reason:  "os is required",
			}
		}
		// We don't constrain OS values here; future config-schema validation
		// can add an allowlist when the supported set is finalized.

		if rb.Capacity < 0 {
			return nil, &InvalidConfigError{
				Section: fmt.Sprintf("benches[%d]", i),
				Reason:  fmt.Sprintf("bench %q: capacity must be >= 0", rb.Name),
			}
		}

		registry = append(registry, bench.Bench{
			Name:     rb.Name,
			Host:     rb.Host, // empty is fine — means local
			OS:       rb.OS,
			Runtime:  rb.Runtime, // empty defaults to "docker" via bench.RuntimeBin()
			Platform: rb.Platform,
			Capacity: rb.Capacity, // 0 = unset; the scheduler defaults it to the Bench's NCPU (floored to 1)
		})
	}
	command, err := parseTestingCommand(raw.Testing.Command)
	if err != nil {
		return nil, &InvalidConfigError{
			Section: "testing.command",
			Reason:  err.Error(),
		}
	}

	if err := validateNode(raw.Node); err != nil {
		return nil, err
	}

	storage, err := transformStorage(raw.Storage)
	if err != nil {
		return nil, err
	}

	return &Config{
		Registry: registry,
		Targets:  raw.Defaults.Targets,
		Versions: raw.Versions,
		Node:     raw.Node,
		Defaults: Defaults{
			Targets: raw.Defaults.Targets,
			Pin:     raw.Defaults.Pin,
			Exclude: raw.Defaults.Exclude,
		},
		Testing: Testing{
			Command: command,
		},
		Storage: storage,
	}, nil
}

// transformStorage maps rawStorage to Storage, applying defaults for unset fields.
// Defaults: ArtifactTTL = 24h (when empty), KeepLastRuns = 10 (when 0).
// "0" or "0s" for ArtifactTTL disables the TTL bound (sets 0).
// Negative KeepLastRuns is a validation error.
func transformStorage(raw rawStorage) (Storage, error) {
	var ttl time.Duration
	switch raw.ArtifactTTL {
	case "":
		ttl = 24 * time.Hour
	case "0", "0s":
		ttl = 0
	default:
		var err error
		ttl, err = time.ParseDuration(raw.ArtifactTTL)
		if err != nil {
			return Storage{}, fmt.Errorf("storage.artifact_ttl: %w", err)
		}
	}

	keepLastRuns := raw.KeepLastRuns
	if keepLastRuns < 0 {
		return Storage{}, &InvalidConfigError{
			Section: "storage.keep_last_runs",
			Reason:  "must not be negative",
		}
	}
	if keepLastRuns == 0 {
		keepLastRuns = 10
	}

	return Storage{
		KeepArtifacts: raw.KeepArtifacts,
		ArtifactTTL:   ttl,
		KeepLastRuns:  keepLastRuns,
	}, nil
}

// validateNode validates the [node] table: OS keys must be one of the
// supported OS families, majors must be all-digit strings, and majors must
// not repeat within a single OS's list.
func validateNode(node map[string][]string) error {
	for os, majors := range node {
		switch os {
		case "linux", "windows", "macos":
			// ok
		default:
			return &InvalidConfigError{
				Section: "node",
				Reason:  fmt.Sprintf("[node]: unknown OS key %q", os),
			}
		}

		seen := make(map[string]bool, len(majors))
		for _, major := range majors {
			if major == "" || strings.IndexFunc(major, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
				return &InvalidConfigError{
					Section: "node",
					Reason:  fmt.Sprintf("[node] %s: invalid Node major %q (must be digits)", os, major),
				}
			}
			if seen[major] {
				return &InvalidConfigError{
					Section: "node",
					Reason:  fmt.Sprintf("[node] %s: duplicate Node major %q", os, major),
				}
			}
			seen[major] = true
		}
	}
	return nil
}

func parseTestingCommand(raw any) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, nil
		}
		args, err := shlex.Split(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid command string: %w", err)
		}
		return args, nil
	case []any:
		if len(v) == 0 {
			return nil, nil
		}
		args := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("command array element %d must be a string", i)
			}
			args = append(args, s)
		}
		return args, nil
	default:
		return nil, fmt.Errorf("command must be a string or array of strings")
	}
}

// probeBenches probes each Bench concurrently. Unreachable ones return in
// the second slice with the probe error attached.
func probeBenches(parent context.Context, benches []bench.Bench, timeout time.Duration) (reachable []bench.Bench, unreachable []UnreachableBench) {
	type probeResult struct {
		bench bench.Bench
		err   error
	}
	results := make(chan probeResult, len(benches))

	for _, b := range benches {
		b := b
		go func() {
			ctx, cancel := context.WithTimeout(parent, timeout)
			defer cancel()
			_, err := probeRun(ctx, b, []string{"version", "--format", "{{.Server.Version}}"})
			results <- probeResult{bench: b, err: err}
		}()
	}

	for range benches {
		r := <-results
		if r.err == nil {
			reachable = append(reachable, r.bench)
		} else {
			unreachable = append(unreachable, UnreachableBench{
				Bench: r.bench, Cause: r.err,
			})
		}
	}
	return reachable, unreachable
}

// --- Errors ---

// InvalidConfigError is returned by Load when config validation fails.
type InvalidConfigError struct {
	Section string
	Reason  string
}

func (e *InvalidConfigError) Error() string {
	return fmt.Sprintf("invalid config at %s: %s", e.Section, e.Reason)
}
