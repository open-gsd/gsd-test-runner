package installhooks

import (
	"encoding/json"
	"strings"
	"testing"
)

const hookCmd = "node /opt/gsd-test/route-tests.mjs"

func mustMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	return m
}

func TestMergeHook_AddsToEmptySettings(t *testing.T) {
	out, changed, err := MergeHook([]byte("{}"), "Bash", hookCmd)
	if err != nil || !changed {
		t.Fatalf("MergeHook empty: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(out), hookCmd) {
		t.Errorf("hook command not present:\n%s", out)
	}
	// Structure: hooks.PreToolUse[0].matcher == Bash, .hooks[0].command == hookCmd
	m := mustMap(t, out)
	pre := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	entry := pre[0].(map[string]any)
	if entry["matcher"] != "Bash" {
		t.Errorf("matcher = %v, want Bash", entry["matcher"])
	}
}

func TestMergeHook_Idempotent(t *testing.T) {
	once, _, _ := MergeHook([]byte("{}"), "Bash", hookCmd)
	twice, changed, err := MergeHook(once, "Bash", hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second merge reported a change — not idempotent")
	}
	if string(once) != string(twice) {
		t.Errorf("second merge altered settings:\n%s\nvs\n%s", once, twice)
	}
}

func TestMergeHook_PreservesExistingHooksAndKeys(t *testing.T) {
	existing := `{
      "model": "opus",
      "hooks": {
        "PostToolUse": [{"matcher":"Write","hooks":[{"type":"command","command":"echo hi"}]}],
        "PreToolUse": [{"matcher":"Edit","hooks":[{"type":"command","command":"lint"}]}]
      }
    }`
	out, changed, err := MergeHook([]byte(existing), "Bash", hookCmd)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	s := string(out)
	for _, must := range []string{`"model"`, "opus", "PostToolUse", "echo hi", `"Edit"`, "lint", hookCmd, `"Bash"`} {
		if !strings.Contains(s, must) {
			t.Errorf("merge dropped %q:\n%s", must, s)
		}
	}
	// PreToolUse must now have both the Edit entry and our Bash entry.
	pre := mustMap(t, out)["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Errorf("PreToolUse len = %d, want 2 (Edit + Bash): %v", len(pre), pre)
	}
}

func TestMergeHook_AppendsToExistingBashMatcher(t *testing.T) {
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"other"}]}]}}`
	out, changed, err := MergeHook([]byte(existing), "Bash", hookCmd)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	pre := mustMap(t, out)["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("should reuse the Bash matcher, got %d entries", len(pre))
	}
	hooks := pre[0].(map[string]any)["hooks"].([]any)
	if len(hooks) != 2 {
		t.Errorf("Bash matcher hooks = %d, want 2 (other + ours)", len(hooks))
	}
}

func TestRemoveHook_ReversesMergeExactly(t *testing.T) {
	existing := `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"lint"}]}]}}`
	merged, _, _ := MergeHook([]byte(existing), "Bash", hookCmd)
	removed, changed, err := RemoveHook(merged, "Bash", hookCmd)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Contains(string(removed), hookCmd) {
		t.Errorf("hook command still present after removal:\n%s", removed)
	}
	// The pre-existing Edit hook and model key must survive.
	for _, must := range []string{"opus", `"Edit"`, "lint"} {
		if !strings.Contains(string(removed), must) {
			t.Errorf("removal dropped pre-existing %q:\n%s", must, removed)
		}
	}
}

func TestMergeHook_ErrorsOnMalformedHooks(t *testing.T) {
	for _, bad := range []string{
		`{"hooks":"oops"}`,
		`{"hooks":{"PreToolUse":{"not":"an array"}}}`,
	} {
		if _, _, err := MergeHook([]byte(bad), "Bash", hookCmd); err == nil {
			t.Errorf("MergeHook(%s): expected an error rather than clobbering user data", bad)
		}
	}
}

func TestRemoveHook_NoopWhenAbsent(t *testing.T) {
	in := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"lint"}]}]}}`
	_, changed, err := RemoveHook([]byte(in), "Bash", hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("RemoveHook reported a change when our hook was absent")
	}
}
