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

// Run is one (OS × Node major) pipeline invocation — the unit the
// capacity-aware scheduler fans out across Benches (enhancement #108). Bench
// is intentionally absent: assignment is dynamic (least-loaded, pull-based) at
// dispatch time, not fixed at plan time, so a Run is Bench-agnostic.
type Run struct {
	OS        string
	NodeMajor string         // Node.js major, e.g. "22" (the second matrix axis)
	ImageID   images.ImageID // fully-qualified, node-suffixed tag: gsd-tester-<os>:<version>-node<major>
	Version   string         // image-version sentinel from config.Versions (NOT node-suffixed)
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
// the target OSes, and an optional Node-major override. Pure: no I/O,
// deterministic given inputs.
//
// For each target OS:
//   - if Config.Versions has no entry → SkipReason{Reason: SkipReasonNoImageVersion}
//   - else if the Selector has no candidate Bench for the OS →
//     SkipReason{Reason: SkipReasonNoBenchForOS}
//   - else emit one Run per Node major (the OS × Node cross-product): the majors
//     come from nodeOverride when non-empty (the --node flag, applied uniformly
//     to every OS), otherwise Config.NodeVersionsFor(os).
//
// Bench selection is NOT done here — Runs are Bench-agnostic and the scheduler
// assigns Benches dynamically (enhancement #108). The image reference is the
// node-suffixed tag per ADR-0005 + the #108 tag convention:
//
//	ghcr.io/open-gsd/gsd-tester-<os>:<version>-node<major>
//
// Version (the un-suffixed image-version sentinel) is carried separately so the
// Pipeline's CheckImageVersion leg still verifies the sh.gsd-test.image-version
// label (which is the un-suffixed version, per ADR-0011).
func Build(cfg *config.Config, selector *bench.Selector, targets []string, nodeOverride []string) (*Plan, error) {
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

		if len(selector.BenchesForOS(os)) == 0 {
			p.Skipped = append(p.Skipped, SkipReason{
				OS:     os,
				Reason: SkipReasonNoBenchForOS,
				Detail: fmt.Sprintf("no Bench available for OS=%s in registry", os),
			})
			continue
		}

		majors := nodeOverride
		if len(majors) == 0 {
			majors = cfg.NodeVersionsFor(os)
		}
		for _, major := range majors {
			p.Runs = append(p.Runs, Run{
				OS:        os,
				NodeMajor: major,
				ImageID:   images.ImageID(fmt.Sprintf("ghcr.io/open-gsd/gsd-tester-%s:%s-node%s", os, version, major)),
				Version:   version,
			})
		}
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
