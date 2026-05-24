package main

import (
	"testing"
)

// Integration tests for run() itself require a real Docker daemon, real
// Bench machines, and a real git repo with reachable refs. Those are
// out of scope for unit tests. This file covers the two purely-functional
// surfaces: parseFlags and commaSplit.

// ── parseFlags ────────────────────────────────────────────────────────────────

func TestParseFlags_Defaults(t *testing.T) {
	f, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags([]): unexpected error: %v", err)
	}
	if f.base != "main" {
		t.Errorf("base: got %q, want %q", f.base, "main")
	}
	if f.head != "HEAD" {
		t.Errorf("head: got %q, want %q", f.head, "HEAD")
	}
	if f.source != "." {
		t.Errorf("source: got %q, want %q", f.source, ".")
	}
	if f.configPath != "" {
		t.Errorf("configPath: got %q, want empty", f.configPath)
	}
	if f.probeBenches {
		t.Error("probeBenches: got true, want false")
	}
	if f.targets != "" {
		t.Errorf("targets: got %q, want empty", f.targets)
	}
	if f.pin != "" {
		t.Errorf("pin: got %q, want empty", f.pin)
	}
	if f.exclude != "" {
		t.Errorf("exclude: got %q, want empty", f.exclude)
	}
	if f.jsonEvents {
		t.Error("jsonEvents: got true, want false")
	}
	if f.scratch != "" {
		t.Errorf("scratch: got %q, want empty", f.scratch)
	}
}

func TestParseFlags_AllSet(t *testing.T) {
	args := []string{
		"--config", "x.toml",
		"--probe-benches",
		"--targets", "linux,windows",
		"--bench", "lab-rig-1",
		"--exclude", "lab-rig-2,lab-rig-3",
		"--json-events",
		"--base", "release/v2",
		"--head", "refs/pull/42/head",
		"--source", "/repo",
		"--scratch", "/tmp/scratch",
	}
	f, err := parseFlags(args)
	if err != nil {
		t.Fatalf("parseFlags(allSet): unexpected error: %v", err)
	}
	if f.configPath != "x.toml" {
		t.Errorf("configPath: got %q, want %q", f.configPath, "x.toml")
	}
	if !f.probeBenches {
		t.Error("probeBenches: got false, want true")
	}
	if f.targets != "linux,windows" {
		t.Errorf("targets: got %q, want %q", f.targets, "linux,windows")
	}
	if f.pin != "lab-rig-1" {
		t.Errorf("pin: got %q, want %q", f.pin, "lab-rig-1")
	}
	if f.exclude != "lab-rig-2,lab-rig-3" {
		t.Errorf("exclude: got %q, want %q", f.exclude, "lab-rig-2,lab-rig-3")
	}
	if !f.jsonEvents {
		t.Error("jsonEvents: got false, want true")
	}
	if f.base != "release/v2" {
		t.Errorf("base: got %q, want %q", f.base, "release/v2")
	}
	if f.head != "refs/pull/42/head" {
		t.Errorf("head: got %q, want %q", f.head, "refs/pull/42/head")
	}
	if f.source != "/repo" {
		t.Errorf("source: got %q, want %q", f.source, "/repo")
	}
	if f.scratch != "/tmp/scratch" {
		t.Errorf("scratch: got %q, want %q", f.scratch, "/tmp/scratch")
	}
}

func TestParseFlags_BadFlag(t *testing.T) {
	_, err := parseFlags([]string{"--unknown-flag"})
	if err == nil {
		t.Error("parseFlags(--unknown-flag): expected error, got nil")
	}
}

// ── commaSplit ────────────────────────────────────────────────────────────────

func TestCommaSplit_Empty(t *testing.T) {
	got := commaSplit("")
	if got != nil {
		t.Errorf("commaSplit(%q): got %v, want nil", "", got)
	}
}

func TestCommaSplit_Single(t *testing.T) {
	got := commaSplit("linux")
	if len(got) != 1 || got[0] != "linux" {
		t.Errorf("commaSplit(%q): got %v, want [linux]", "linux", got)
	}
}

func TestCommaSplit_Multiple(t *testing.T) {
	got := commaSplit("linux,windows,macos")
	if len(got) != 3 {
		t.Errorf("commaSplit(%q): got len=%d, want 3: %v", "linux,windows,macos", len(got), got)
		return
	}
	want := []string{"linux", "windows", "macos"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("commaSplit index %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestCommaSplit_TrimsWhitespace(t *testing.T) {
	got := commaSplit(" linux , windows ")
	if len(got) != 2 {
		t.Errorf("commaSplit(whitespace): got len=%d, want 2: %v", len(got), got)
		return
	}
	if got[0] != "linux" {
		t.Errorf("commaSplit[0]: got %q, want %q", got[0], "linux")
	}
	if got[1] != "windows" {
		t.Errorf("commaSplit[1]: got %q, want %q", got[1], "windows")
	}
}
