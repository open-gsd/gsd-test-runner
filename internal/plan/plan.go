// Package plan builds a per-run execution plan: which Benches will run
// which OSes, which targeted OSes are being skipped via
// --allow-skip-os, and any unreachable Benches that should abort the
// run before any Pipeline starts.
//
// Fail-loud-at-planning-time discipline per
// docs/adr/0009-local-engine-top-level-orchestration.md (default
// fail-loud, opt-in skip behavior).
package plan

import (
	"errors"
	"fmt"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/images"
)

// Plan is the resolved per-run schedule that the orchestrator dispatches.
// Produced by Build from a *config.Config + selector + target OS list.
// Each Run is fully resolved (Bench picked, ImageID and version selected);
// Skipped carries OSes the planner could not resolve, with reasons for
// the renderer to display.
type Plan struct {
	Runs    []Run
	Skipped []SkipReason
}

// Run is one per-OS pipeline invocation. The orchestrator constructs a
// pipeline.New(Run.Bench, Run.ImageID, ..., Run.OS, ...) per Run.
type Run struct {
	Bench   bench.Bench
	ImageID images.ImageID
	Version string // expected Tester Image version from config.Versions
	OS      string
}

// SkipReason explains why a target OS doesn't appear in Plan.Runs. The
// aggregator and renderer consume these for the per-OS summary block.
type SkipReason struct {
	OS     string
	Reason string // "no_bench_for_os" | "bench_unreachable" | "no_image_version" | "pin_not_in_registry"
	Detail string // free-text for renderer (e.g., "all 2 Linux Benches were excluded")
}

// SkipReason constants for the Reason field. Defined as exported strings
// for stable matching by the renderer and aggregator.
const (
	SkipReasonNoBenchForOS     = "no_bench_for_os"
	SkipReasonBenchUnreachable = "bench_unreachable"
	SkipReasonNoImageVersion   = "no_image_version"
	SkipReasonPinNotInRegistry = "pin_not_in_registry"
)

// Build constructs a Plan from a loaded Config, a pre-constructed Selector,
// and the list of target OSes. Pure: no I/O, deterministic given inputs.
//
// For each target OS:
//   - if Config.Versions has no entry → SkipReason{Reason: SkipReasonNoImageVersion}
//   - else call selector.Pick(os):
//   - if *bench.NoBenchForOSError → SkipReason{Reason: SkipReasonNoBenchForOS}
//   - if any other error → Build returns error (doesn't classify)
//   - on success → Run{Bench, ImageID derived from OS, Version from Config.Versions, OS}
//
// images.ImageID for each OS is derived per ADR-0005 conventions:
//
//	ghcr.io/open-gsd/gsd-tester-<os>
//
// The version comes from Config.Versions[os]; Pipeline.New then takes
// ImageID + version separately (per existing pipeline signature).
func Build(cfg *config.Config, selector *bench.Selector, targets []string) (*Plan, error) {
	if cfg == nil {
		return nil, errors.New("plan.Build: cfg is nil")
	}
	if selector == nil {
		return nil, errors.New("plan.Build: selector is nil")
	}

	p := &Plan{}
	for _, os := range targets {
		version, ok := cfg.Versions[os]
		if !ok || version == "" {
			p.Skipped = append(p.Skipped, SkipReason{
				OS:     os,
				Reason: SkipReasonNoImageVersion,
				Detail: fmt.Sprintf("Config.Versions has no entry for OS %q (add to [versions] in config.toml)", os),
			})
			continue
		}

		b, err := selector.Pick(os)
		if err != nil {
			var nbf *bench.NoBenchForOSError
			if errors.As(err, &nbf) {
				p.Skipped = append(p.Skipped, SkipReason{
					OS:     os,
					Reason: SkipReasonNoBenchForOS,
					Detail: fmt.Sprintf("no Bench available for OS=%s in registry", os),
				})
				continue
			}
			// Unexpected error type — bubble it up rather than masking.
			return nil, fmt.Errorf("plan.Build: selector.Pick(%q): %w", os, err)
		}

		p.Runs = append(p.Runs, Run{
			Bench:   b,
			ImageID: images.ImageID(fmt.Sprintf("ghcr.io/open-gsd/gsd-tester-%s", os)),
			Version: version,
			OS:      os,
		})
	}
	return p, nil
}

// buildWithPickFn is an internal test helper that allows the test package
// (same package — package plan) to inject an arbitrary pick function,
// exercising the error-propagation path for non-NoBenchForOSError errors.
// Not exported; only reachable within this package.
func buildWithPickFn(cfg *config.Config, pick func(os string) (bench.Bench, error), targets []string) (*Plan, error) {
	if cfg == nil {
		return nil, errors.New("plan.Build: cfg is nil")
	}
	p := &Plan{}
	for _, os := range targets {
		version, ok := cfg.Versions[os]
		if !ok || version == "" {
			p.Skipped = append(p.Skipped, SkipReason{
				OS:     os,
				Reason: SkipReasonNoImageVersion,
				Detail: fmt.Sprintf("Config.Versions has no entry for OS %q (add to [versions] in config.toml)", os),
			})
			continue
		}
		b, err := pick(os)
		if err != nil {
			var nbf *bench.NoBenchForOSError
			if errors.As(err, &nbf) {
				p.Skipped = append(p.Skipped, SkipReason{
					OS:     os,
					Reason: SkipReasonNoBenchForOS,
					Detail: fmt.Sprintf("no Bench available for OS=%s in registry", os),
				})
				continue
			}
			return nil, fmt.Errorf("plan.Build: selector.Pick(%q): %w", os, err)
		}
		p.Runs = append(p.Runs, Run{
			Bench:   b,
			ImageID: images.ImageID(fmt.Sprintf("ghcr.io/open-gsd/gsd-tester-%s", os)),
			Version: version,
			OS:      os,
		})
	}
	return p, nil
}

// AddUnreachable appends SkipReason entries derived from config.Unreachable
// for any OS the orchestrator couldn't otherwise plan. Called by the
// orchestrator BEFORE Build for any target OS whose only Benches are in
// Config.Unreachable. Optional helper — keeps the orchestrator simple.
//
// (Implementation detail: prefer to wire Unreachable awareness via
// selector — but Selector receives a pre-filtered Registry and doesn't
// know about Unreachable. So this helper lives in plan.)
func (p *Plan) AddUnreachable(unreachable []config.UnreachableBench, targets []string) {
	// For each target OS, if there's no Run AND there's an unreachable bench
	// for that OS, add a SkipReason.
	runOSes := make(map[string]bool, len(p.Runs))
	for _, r := range p.Runs {
		runOSes[r.OS] = true
	}
	skippedOSes := make(map[string]bool, len(p.Skipped))
	for _, s := range p.Skipped {
		skippedOSes[s.OS] = true
	}
	for _, os := range targets {
		if runOSes[os] || skippedOSes[os] {
			continue
		}
		for _, u := range unreachable {
			if u.Bench.OS == os {
				p.Skipped = append(p.Skipped, SkipReason{
					OS:     os,
					Reason: SkipReasonBenchUnreachable,
					Detail: fmt.Sprintf("Bench %q (host %s) unreachable: %v", u.Bench.Name, u.Bench.Host, u.Cause),
				})
				break
			}
		}
	}
}
