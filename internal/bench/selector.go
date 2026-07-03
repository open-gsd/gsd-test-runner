package bench

import (
	"fmt"
	"sync"
)

// Selector picks a Bench for each per-OS Pipeline within a single Local
// Engine invocation. Pure (no I/O); holds in-memory registry + per-OS
// round-robin cursors. Reachability validation is a registry-loading
// concern (internal/config) per ADR-0016 dec 3 — Selector trusts its
// registry.
type Selector struct {
	registry []Bench        // post-filter (Pin + Exclude already applied)
	cursor   map[string]int // per-OS round-robin cursor
	mu       sync.Mutex
}

// Options configures NewSelector.
type Options struct {
	// Pin, if non-empty, filters the registry to the single named bench.
	Pin string
	// Exclude removes named benches from the registry before round-robin.
	Exclude []string
}

// NewSelector constructs a Selector from a Bench registry and filter options.
// Returns *PinExcludeConflictError if Pin is also in Exclude.
// Returns *PinnedBenchNotInRegistryError if Pin is set but no Bench with
// that name exists in the (pre-exclude) registry.
func NewSelector(registry []Bench, opts Options) (*Selector, error) {
	// Validate Pin not in Exclude
	if opts.Pin != "" {
		for _, ex := range opts.Exclude {
			if ex == opts.Pin {
				return nil, &PinExcludeConflictError{Pin: opts.Pin}
			}
		}
	}

	// Validate Pin exists in registry
	if opts.Pin != "" {
		found := false
		available := make([]string, 0, len(registry))
		for _, b := range registry {
			available = append(available, b.Name)
			if b.Name == opts.Pin {
				found = true
			}
		}
		if !found {
			return nil, &PinnedBenchNotInRegistryError{
				Pin:       opts.Pin,
				Available: available,
			}
		}
	}

	// Apply Pin filter then Exclude filter
	filtered := make([]Bench, 0, len(registry))
	for _, b := range registry {
		if opts.Pin != "" && b.Name != opts.Pin {
			continue
		}
		excluded := false
		for _, ex := range opts.Exclude {
			if b.Name == ex {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, b)
		}
	}

	return &Selector{
		registry: filtered,
		cursor:   make(map[string]int),
	}, nil
}

// Pick returns the next Bench for the given OS, advancing the per-OS
// round-robin cursor. Thread-safe.
// Returns *NoBenchForOSError if no Bench in the post-filter registry
// has matching OS.
func (s *Selector) Pick(os string) (Bench, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Filter registry to benches matching os
	candidates := make([]Bench, 0)
	for _, b := range s.registry {
		if b.OS == os {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return Bench{}, &NoBenchForOSError{OS: os}
	}

	idx := s.cursor[os] % len(candidates)
	s.cursor[os]++
	return candidates[idx], nil
}

// BenchesForOS returns the post-filter (Pin + Exclude already applied)
// candidate Benches matching os, without advancing any round-robin cursor.
// Used by the capacity-aware scheduler (enhancement #108), which assigns
// (OS×Node) jobs to Benches dynamically rather than one-Bench-per-OS via Pick.
// Returns a fresh slice (safe for the caller to retain); nil when none match.
func (s *Selector) BenchesForOS(os string) []Bench {
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidates []Bench
	for _, b := range s.registry {
		if b.OS == os {
			candidates = append(candidates, b)
		}
	}
	return candidates
}

// --- Errors ---

// NoBenchForOSError is returned by Pick when no Bench in the post-filter
// registry matches the requested OS.
type NoBenchForOSError struct {
	OS string
}

func (e *NoBenchForOSError) Error() string {
	return fmt.Sprintf("no Bench available for OS=%s", e.OS)
}

// PinnedBenchNotInRegistryError is returned by NewSelector when Options.Pin
// is set but no Bench with that name exists in the registry.
type PinnedBenchNotInRegistryError struct {
	Pin       string
	Available []string
}

func (e *PinnedBenchNotInRegistryError) Error() string {
	return fmt.Sprintf("pinned Bench %q not found in registry; available: %v",
		e.Pin, e.Available)
}

// PinExcludeConflictError is returned by NewSelector when Options.Pin
// is also present in Options.Exclude.
type PinExcludeConflictError struct {
	Pin string
}

func (e *PinExcludeConflictError) Error() string {
	return fmt.Sprintf("Pin %q is in Exclude list", e.Pin)
}
