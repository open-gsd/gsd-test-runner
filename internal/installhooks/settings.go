// Package installhooks installs (and reverses) the gsd-test agent integration
// on a Dev Workstation: the Claude Code PreToolUse guard hook, the run-and-die
// skill, and the Codex shim (issue #71, ADR-0022 Decision 5). The settings.json
// merge here is non-destructive — it preserves every pre-existing hook and key —
// and idempotent, so installing is safe to repeat and `--uninstall` reverses
// exactly what was added.
package installhooks

import (
	"encoding/json"
	"fmt"
)

// MergeHook returns settingsJSON with a Claude Code PreToolUse hook added for
// the given matcher + command, preserving all other content. changed is false
// when the hook was already present (idempotent). An empty/blank input is
// treated as {}.
func MergeHook(settingsJSON []byte, matcher, command string) ([]byte, bool, error) {
	root, err := decodeSettings(settingsJSON)
	if err != nil {
		return nil, false, err
	}

	// Refuse to merge into a settings file whose hooks structure is the wrong
	// shape — overwriting it would corrupt user data. (Absent is fine.)
	if v, ok := root["hooks"]; ok {
		if _, isMap := v.(map[string]any); !isMap {
			return nil, false, fmt.Errorf(`settings.json: "hooks" is not an object`)
		}
	}
	hooks := childMap(root, "hooks")
	if v, ok := hooks["PreToolUse"]; ok {
		if _, isSlice := v.([]any); !isSlice {
			return nil, false, fmt.Errorf(`settings.json: "hooks.PreToolUse" is not an array`)
		}
	}
	pre := childSlice(hooks, "PreToolUse")

	// Find an existing entry for this matcher.
	for _, e := range pre {
		entry, ok := e.(map[string]any)
		if !ok || entry["matcher"] != matcher {
			continue
		}
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			if hm, ok := h.(map[string]any); ok && hm["command"] == command {
				return marshal(root) // already present → idempotent no-op
			}
		}
		entry["hooks"] = append(inner, commandHook(command))
		hooks["PreToolUse"] = pre
		root["hooks"] = hooks
		out, mErr := json.MarshalIndent(root, "", "  ")
		return out, true, mErr
	}

	// No entry for this matcher yet — add one.
	pre = append(pre, map[string]any{
		"matcher": matcher,
		"hooks":   []any{commandHook(command)},
	})
	hooks["PreToolUse"] = pre
	root["hooks"] = hooks
	out, mErr := json.MarshalIndent(root, "", "  ")
	return out, true, mErr
}

// RemoveHook reverses MergeHook: it drops the matcher/command hook, cleaning up
// an emptied matcher entry, an emptied PreToolUse list, and an emptied hooks
// map, while preserving everything else. changed is false when our hook was
// absent (idempotent).
func RemoveHook(settingsJSON []byte, matcher, command string) ([]byte, bool, error) {
	root, err := decodeSettings(settingsJSON)
	if err != nil {
		return nil, false, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return marshalUnchanged(root)
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok {
		return marshalUnchanged(root)
	}

	changed := false
	newPre := pre[:0:0]
	for _, e := range pre {
		entry, ok := e.(map[string]any)
		if !ok || entry["matcher"] != matcher {
			newPre = append(newPre, e)
			continue
		}
		inner, _ := entry["hooks"].([]any)
		kept := inner[:0:0]
		for _, h := range inner {
			if hm, ok := h.(map[string]any); ok && hm["command"] == command {
				changed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) > 0 {
			entry["hooks"] = kept
			newPre = append(newPre, entry)
		}
		// else: drop the now-empty matcher entry
	}

	if !changed {
		return marshalUnchanged(root)
	}
	if len(newPre) > 0 {
		hooks["PreToolUse"] = newPre
	} else {
		delete(hooks, "PreToolUse")
	}
	if len(hooks) > 0 {
		root["hooks"] = hooks
	} else {
		delete(root, "hooks")
	}
	out, mErr := json.MarshalIndent(root, "", "  ")
	return out, true, mErr
}

func decodeSettings(b []byte) (map[string]any, error) {
	root := map[string]any{}
	trimmed := len(b) == 0
	for _, c := range b {
		if c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			trimmed = false
			break
		}
		trimmed = true
	}
	if trimmed {
		return root, nil
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}
	return root, nil
}

func commandHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command}
}

func childMap(parent map[string]any, key string) map[string]any {
	if m, ok := parent[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func childSlice(parent map[string]any, key string) []any {
	if s, ok := parent[key].([]any); ok {
		return s
	}
	return nil
}

func marshal(root map[string]any) ([]byte, bool, error) {
	out, err := json.MarshalIndent(root, "", "  ")
	return out, false, err
}

func marshalUnchanged(root map[string]any) ([]byte, bool, error) {
	out, err := json.MarshalIndent(root, "", "  ")
	return out, false, err
}
