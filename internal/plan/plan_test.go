package plan

import (
	"errors"
	"fmt"
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/bench"
	"github.com/open-gsd/gsd-test-runner/internal/config"
	"github.com/open-gsd/gsd-test-runner/internal/images"
)

// makeSelector is a test helper that builds a Selector from a registry slice.
// Panics on construction error (test setup failure).
func makeSelector(registry []bench.Bench) *bench.Selector {
	s, err := bench.NewSelector(registry, bench.Options{})
	if err != nil {
		panic(fmt.Sprintf("makeSelector: %v", err))
	}
	return s
}

// makeCfg is a test helper that builds a minimal *config.Config. node may be
// nil (then NodeVersionsFor falls back to DefaultNodeLTS()).
func makeCfg(versions map[string]string, registry []bench.Bench, node map[string][]string) *config.Config {
	return &config.Config{
		Registry: registry,
		Versions: versions,
		Node:     node,
	}
}

func TestBuild_NilConfig(t *testing.T) {
	sel := makeSelector([]bench.Bench{{Name: "b1", Host: "local", OS: "linux"}})
	_, err := Build(nil, sel, []string{"linux"}, nil)
	if err == nil {
		t.Fatal("expected error for nil cfg, got nil")
	}
}

func TestBuild_NilSelector(t *testing.T) {
	cfg := makeCfg(map[string]string{"linux": "v1.0"}, nil, nil)
	_, err := Build(cfg, nil, []string{"linux"}, nil)
	if err == nil {
		t.Fatal("expected error for nil selector, got nil")
	}
}

func TestBuild_EmptyTargets(t *testing.T) {
	registry := []bench.Bench{{Name: "b1", OS: "linux"}}
	cfg := makeCfg(map[string]string{"linux": "v1.0"}, registry, nil)
	sel := makeSelector(registry)
	p, err := Build(cfg, sel, []string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 0 {
		t.Errorf("expected 0 Runs, got %d", len(p.Runs))
	}
	if len(p.Skipped) != 0 {
		t.Errorf("expected 0 Skipped, got %d", len(p.Skipped))
	}
}

// TestBuild_SingleOSSingleNode exercises the happy path with the Node axis
// pinned to one major (via override) so exactly one Run is produced.
func TestBuild_SingleOSSingleNode(t *testing.T) {
	registry := []bench.Bench{{Name: "bench-linux-1", Host: "local", OS: "linux"}}
	cfg := makeCfg(map[string]string{"linux": "v1.0"}, registry, nil)
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"linux"}, []string{"22"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 1 {
		t.Fatalf("expected 1 Run, got %d", len(p.Runs))
	}
	if len(p.Skipped) != 0 {
		t.Errorf("expected 0 Skipped, got %d", len(p.Skipped))
	}

	r := p.Runs[0]
	if r.OS != "linux" {
		t.Errorf("Run.OS: got %q, want %q", r.OS, "linux")
	}
	if r.NodeMajor != "22" {
		t.Errorf("Run.NodeMajor: got %q, want %q", r.NodeMajor, "22")
	}
	// ImageID is the node-suffixed, fully-qualified tag.
	wantImageID := images.ImageID("ghcr.io/open-gsd/gsd-tester-linux:v1.0-node22")
	if r.ImageID != wantImageID {
		t.Errorf("Run.ImageID: got %q, want %q", r.ImageID, wantImageID)
	}
	// Version stays the un-suffixed image-version sentinel.
	if r.Version != "v1.0" {
		t.Errorf("Run.Version: got %q, want %q", r.Version, "v1.0")
	}
}

// TestBuild_NodeCrossProduct: with two configured Node majors and no override,
// Build emits one Run per (OS × Node) cell.
func TestBuild_NodeCrossProduct(t *testing.T) {
	registry := []bench.Bench{{Name: "bench-linux-1", OS: "linux"}}
	cfg := makeCfg(map[string]string{"linux": "v1.0"}, registry, map[string][]string{"linux": {"22", "24"}})
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"linux"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 2 {
		t.Fatalf("expected 2 Runs (linux×{22,24}), got %d", len(p.Runs))
	}
	got := map[string]images.ImageID{}
	for _, r := range p.Runs {
		if r.OS != "linux" {
			t.Errorf("Run.OS = %q, want linux", r.OS)
		}
		got[r.NodeMajor] = r.ImageID
	}
	want := map[string]images.ImageID{
		"22": "ghcr.io/open-gsd/gsd-tester-linux:v1.0-node22",
		"24": "ghcr.io/open-gsd/gsd-tester-linux:v1.0-node24",
	}
	for major, wantID := range want {
		if got[major] != wantID {
			t.Errorf("major %s: ImageID = %q, want %q", major, got[major], wantID)
		}
	}
}

// TestBuild_NodeOverrideAppliesToAllOSes: the --node override replaces the
// per-OS configured majors for every target OS.
func TestBuild_NodeOverrideAppliesToAllOSes(t *testing.T) {
	registry := []bench.Bench{
		{Name: "bench-linux-1", OS: "linux"},
		{Name: "bench-windows-1", OS: "windows"},
	}
	cfg := makeCfg(map[string]string{"linux": "v1.0", "windows": "v2.0"}, registry,
		map[string][]string{"linux": {"18", "20", "22"}}) // override must win over this
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"linux", "windows"}, []string{"22"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 2 {
		t.Fatalf("expected 2 Runs (2 OSes × 1 override major), got %d", len(p.Runs))
	}
	for _, r := range p.Runs {
		if r.NodeMajor != "22" {
			t.Errorf("Run[%s].NodeMajor = %q, want 22 (override)", r.OS, r.NodeMajor)
		}
	}
}

// TestBuild_DefaultLTSWhenNodeAbsent: no [node] config and no override →
// Build uses config.DefaultNodeLTS().
func TestBuild_DefaultLTSWhenNodeAbsent(t *testing.T) {
	registry := []bench.Bench{{Name: "bench-linux-1", OS: "linux"}}
	cfg := makeCfg(map[string]string{"linux": "v1.0"}, registry, nil)
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"linux"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != len(config.DefaultNodeLTS()) {
		t.Fatalf("expected %d Runs (default LTS), got %d", len(config.DefaultNodeLTS()), len(p.Runs))
	}
	wantMajors := map[string]bool{}
	for _, m := range config.DefaultNodeLTS() {
		wantMajors[m] = true
	}
	for _, r := range p.Runs {
		if !wantMajors[r.NodeMajor] {
			t.Errorf("unexpected NodeMajor %q not in DefaultNodeLTS()", r.NodeMajor)
		}
	}
}

func TestBuild_MultipleOSes(t *testing.T) {
	registry := []bench.Bench{
		{Name: "bench-linux-1", OS: "linux"},
		{Name: "bench-windows-1", OS: "windows"},
		{Name: "bench-macos-1", OS: "macos"},
	}
	versions := map[string]string{
		"linux":   "v1.0",
		"windows": "v2.0",
		"macos":   "v3.0",
	}
	cfg := makeCfg(versions, registry, nil)
	sel := makeSelector(registry)
	targets := []string{"linux", "windows", "macos"}

	// Pin one major so we get exactly one Run per OS.
	p, err := Build(cfg, sel, targets, []string{"22"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 3 {
		t.Fatalf("expected 3 Runs, got %d", len(p.Runs))
	}
	if len(p.Skipped) != 0 {
		t.Errorf("expected 0 Skipped, got %d", len(p.Skipped))
	}

	runOSes := make(map[string]string, len(p.Runs))
	for _, r := range p.Runs {
		runOSes[r.OS] = r.Version
	}
	for _, os := range targets {
		if v, ok := runOSes[os]; !ok {
			t.Errorf("missing Run for OS %q", os)
		} else if v != versions[os] {
			t.Errorf("Run[%s].Version = %q, want %q", os, v, versions[os])
		}
	}
}

func TestBuild_OSMissingFromVersions(t *testing.T) {
	registry := []bench.Bench{{Name: "b1", OS: "linux"}}
	// No "linux" in versions
	cfg := makeCfg(map[string]string{}, registry, nil)
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"linux"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 0 {
		t.Errorf("expected 0 Runs, got %d", len(p.Runs))
	}
	if len(p.Skipped) != 1 {
		t.Fatalf("expected 1 Skipped, got %d", len(p.Skipped))
	}
	s := p.Skipped[0]
	if s.OS != "linux" {
		t.Errorf("Skipped[0].OS = %q, want %q", s.OS, "linux")
	}
	if s.Reason != SkipReasonNoImageVersion {
		t.Errorf("Skipped[0].Reason = %q, want %q", s.Reason, SkipReasonNoImageVersion)
	}
}

func TestBuild_NoBenchForOS(t *testing.T) {
	// Selector has no bench for "windows"
	registry := []bench.Bench{{Name: "b1", OS: "linux"}}
	cfg := makeCfg(map[string]string{"windows": "v1.0"}, registry, nil)
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"windows"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 0 {
		t.Errorf("expected 0 Runs, got %d", len(p.Runs))
	}
	if len(p.Skipped) != 1 {
		t.Fatalf("expected 1 Skipped, got %d", len(p.Skipped))
	}
	s := p.Skipped[0]
	if s.OS != "windows" {
		t.Errorf("Skipped[0].OS = %q, want %q", s.OS, "windows")
	}
	if s.Reason != SkipReasonNoBenchForOS {
		t.Errorf("Skipped[0].Reason = %q, want %q", s.Reason, SkipReasonNoBenchForOS)
	}
}

// TestBuild_NoBenchSkipsBeforeCrossProduct: an OS with no bench produces exactly
// one skip regardless of how many Node majors are configured (the bench check
// precedes the Node cross-product).
func TestBuild_NoBenchSkipsBeforeCrossProduct(t *testing.T) {
	registry := []bench.Bench{{Name: "b1", OS: "linux"}}
	cfg := makeCfg(map[string]string{"windows": "v1.0"}, registry, map[string][]string{"windows": {"22", "24"}})
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"windows"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 0 {
		t.Errorf("expected 0 Runs, got %d", len(p.Runs))
	}
	if len(p.Skipped) != 1 {
		t.Fatalf("expected exactly 1 Skipped (not one per node major), got %d", len(p.Skipped))
	}
}

func TestBuild_MixedSuccessAndSkip(t *testing.T) {
	registry := []bench.Bench{{Name: "bench-linux-1", OS: "linux"}}
	// "macos" has no version entry
	cfg := makeCfg(map[string]string{"linux": "v1.0"}, registry, nil)
	sel := makeSelector(registry)

	p, err := Build(cfg, sel, []string{"linux", "macos"}, []string{"22"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Runs) != 1 {
		t.Fatalf("expected 1 Run, got %d", len(p.Runs))
	}
	if p.Runs[0].OS != "linux" {
		t.Errorf("Run[0].OS = %q, want linux", p.Runs[0].OS)
	}
	if len(p.Skipped) != 1 {
		t.Fatalf("expected 1 Skipped, got %d", len(p.Skipped))
	}
	if p.Skipped[0].OS != "macos" {
		t.Errorf("Skipped[0].OS = %q, want macos", p.Skipped[0].OS)
	}
	if p.Skipped[0].Reason != SkipReasonNoImageVersion {
		t.Errorf("Skipped[0].Reason = %q, want %q", p.Skipped[0].Reason, SkipReasonNoImageVersion)
	}
}

func TestAddUnreachable_AddsSkipReasonsForUnreachableOSes(t *testing.T) {
	// Plan: linux is ok (1 Run), macos has a NoBenchForOS skip
	p := &Plan{
		Runs: []Run{
			{OS: "linux", NodeMajor: "22"},
		},
		Skipped: []SkipReason{
			{OS: "macos", Reason: SkipReasonNoBenchForOS, Detail: "no bench"},
		},
	}

	unreachable := []config.UnreachableBench{
		{
			Bench: bench.Bench{Name: "bench-windows-1", Host: "win-host", OS: "windows"},
			Cause: errors.New("connection refused"),
		},
	}
	targets := []string{"linux", "macos", "windows"}

	p.AddUnreachable(unreachable, targets)

	// Should add 1 SkipReason for windows; linux and macos already accounted for
	if len(p.Skipped) != 2 {
		t.Fatalf("expected 2 Skipped after AddUnreachable, got %d", len(p.Skipped))
	}
	found := false
	for _, s := range p.Skipped {
		if s.OS == "windows" && s.Reason == SkipReasonBenchUnreachable {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SkipReason{OS:windows, Reason:%s} in Skipped", SkipReasonBenchUnreachable)
	}
}

func TestAddUnreachable_DoesNotDuplicateExistingSkips(t *testing.T) {
	// macos already has a SkipReason
	p := &Plan{
		Skipped: []SkipReason{
			{OS: "macos", Reason: SkipReasonNoBenchForOS},
		},
	}
	unreachable := []config.UnreachableBench{
		{
			Bench: bench.Bench{Name: "bench-macos-1", OS: "macos"},
			Cause: errors.New("unreachable"),
		},
	}
	targets := []string{"macos"}

	p.AddUnreachable(unreachable, targets)

	// Should not add a duplicate for macos
	if len(p.Skipped) != 1 {
		t.Errorf("expected 1 Skipped (no duplicate), got %d", len(p.Skipped))
	}
}
