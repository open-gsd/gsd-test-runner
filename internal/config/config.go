package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
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
	Defaults    Defaults
}

// UnreachableBench records a Bench that failed reachability probing.
// Populated by Load when LoadOptions.Probe is true.
type UnreachableBench struct {
	Bench bench.Bench
	Cause error
}

// Defaults are CLI flag defaults parsed from the config file.
type Defaults struct {
	Targets []string
	Pin     string
	Exclude []string
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
	Defaults rawDefaults       `toml:"defaults"`
	Benches  []rawBench        `toml:"benches"`
	Versions map[string]string `toml:"versions"`
}

type rawDefaults struct {
	Targets []string `toml:"targets"`
	Pin     string   `toml:"pin"`
	Exclude []string `toml:"exclude"`
}

type rawBench struct {
	Name    string `toml:"name"`
	Host    string `toml:"host"`
	OS      string `toml:"os"`
	Runtime string `toml:"runtime,omitempty"` // "docker" (default; all benches today) | "container" (reserved for future Apple Containers)
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

		registry = append(registry, bench.Bench{
			Name:    rb.Name,
			Host:    rb.Host,    // empty is fine — means local
			OS:      rb.OS,
			Runtime: rb.Runtime, // empty defaults to "docker" via bench.RuntimeBin()
		})
	}

	return &Config{
		Registry: registry,
		Targets:  raw.Defaults.Targets,
		Versions: raw.Versions,
		Defaults: Defaults{
			Targets: raw.Defaults.Targets,
			Pin:     raw.Defaults.Pin,
			Exclude: raw.Defaults.Exclude,
		},
	}, nil
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
