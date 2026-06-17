// Package agentintegration embeds the agent-integration assets (the Claude Code
// PreToolUse hook, the Codex shim, and the run-and-die skill) into the gsd-test
// binary so the installer can write them onto a Dev Workstation regardless of
// where the binary runs (issue #71, ADR-0022).
package agentintegration

import _ "embed"

//go:embed skills/run-and-die/SKILL.md
var skillMD []byte

//go:embed codex-shim.sh
var codexShim []byte

//go:embed route-tests.mjs
var routeTests []byte

// SkillMD returns the run-and-die skill document.
func SkillMD() []byte { return skillMD }

// CodexShim returns the Codex exec-path shim script.
func CodexShim() []byte { return codexShim }

// RouteTests returns the Claude Code PreToolUse hook script.
func RouteTests() []byte { return routeTests }
