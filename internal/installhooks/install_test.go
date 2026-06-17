package installhooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstall_BothRuntimes_WritesAssetsHookAndManifest(t *testing.T) {
	root := t.TempDir()
	man, err := Install(Options{Root: root, Claude: true, Codex: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		".claude/gsd-test/route-tests.mjs",
		".claude/skills/run-and-die/SKILL.md",
		".claude/settings.json",
		".gsd-test/codex-shim.sh",
		".gsd-test/codex-bin/node",
		".gsd-test/codex-bin/npm",
		".gsd-test/install-manifest.json",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("expected %s: %v", rel, err)
		}
	}

	settings, _ := os.ReadFile(man.SettingsPath)
	if !strings.Contains(string(settings), "route-tests.mjs") || !strings.Contains(string(settings), `"Bash"`) {
		t.Errorf("settings.json missing the guard hook:\n%s", settings)
	}
	// The Codex shim + node/npm wrappers must be executable.
	for _, rel := range []string{".gsd-test/codex-shim.sh", ".gsd-test/codex-bin/node", ".gsd-test/codex-bin/npm"} {
		info, _ := os.Stat(filepath.Join(root, rel))
		if info == nil || info.Mode()&0o100 == 0 {
			t.Errorf("%s not executable: %v", rel, info)
		}
	}
	// The node/npm shims must export GSD_SHIM_DIR so the shim skips its own dir
	// and never recurses (issue #78).
	nodeShim, _ := os.ReadFile(filepath.Join(root, ".gsd-test/codex-bin/node"))
	if !strings.Contains(string(nodeShim), "GSD_SHIM_DIR=") {
		t.Errorf("codex-bin/node missing GSD_SHIM_DIR recursion guard:\n%s", nodeShim)
	}
	// The node shims live ONLY under the Codex-scoped codex-bin dir — never at a
	// path that would shadow the human's interactive node (ADR-0022 D1/D5).
	if _, err := os.Stat(filepath.Join(root, ".gsd-test", "node")); err == nil {
		t.Error("node shim must be confined to codex-bin, not .gsd-test root")
	}
}

func TestInstall_PreservesExistingSettingsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"lint"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(Options{Root: root, Claude: true}); err != nil {
		t.Fatal(err)
	}
	after1, _ := os.ReadFile(settingsPath)
	for _, must := range []string{"opus", `"Edit"`, "lint", "route-tests.mjs", `"Bash"`} {
		if !strings.Contains(string(after1), must) {
			t.Errorf("after install, settings dropped %q:\n%s", must, after1)
		}
	}

	// Idempotent: a second install must not duplicate the hook.
	if _, err := Install(Options{Root: root, Claude: true}); err != nil {
		t.Fatal(err)
	}
	after2, _ := os.ReadFile(settingsPath)
	if n := strings.Count(string(after2), "route-tests.mjs"); n != 1 {
		t.Errorf("hook duplicated on re-install: %d occurrences\n%s", n, after2)
	}
}

func TestUninstall_ReversesExactly(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"lint"}]}]}}`
	os.WriteFile(settingsPath, []byte(existing), 0o644)

	if _, err := Install(Options{Root: root, Claude: true, Codex: true}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(ManifestPath(root)); err != nil {
		t.Fatal(err)
	}

	// Installed files are gone.
	for _, rel := range []string{
		".claude/gsd-test/route-tests.mjs",
		".claude/skills/run-and-die/SKILL.md",
		".gsd-test/codex-shim.sh",
		".gsd-test/install-manifest.json",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed, stat err=%v", rel, err)
		}
	}
	// Our hook is gone but the pre-existing Edit hook + model key survive.
	settings, _ := os.ReadFile(settingsPath)
	if strings.Contains(string(settings), "route-tests.mjs") {
		t.Errorf("guard hook not removed:\n%s", settings)
	}
	for _, must := range []string{"opus", `"Edit"`, "lint"} {
		if !strings.Contains(string(settings), must) {
			t.Errorf("uninstall dropped pre-existing %q:\n%s", must, settings)
		}
	}
}

func TestUninstall_RemovesSettingsItCreated(t *testing.T) {
	root := t.TempDir() // no pre-existing .claude/settings.json
	if _, err := Install(Options{Root: root, Claude: true}); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("install should create settings.json: %v", err)
	}
	if err := Uninstall(ManifestPath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("settings.json we created should be removed on uninstall (exact reversal), stat err=%v", err)
	}
}

func TestInstall_AccumulatesAcrossRuntimeInstalls(t *testing.T) {
	root := t.TempDir()
	// Codex first, then Claude separately — uninstall must reverse BOTH.
	if _, err := Install(Options{Root: root, Codex: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(Options{Root: root, Claude: true}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(ManifestPath(root)); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		".gsd-test/codex-shim.sh",          // from the first (Codex) install
		".claude/gsd-test/route-tests.mjs", // from the second (Claude) install
		".claude/skills/run-and-die/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed (manifest must accumulate), stat err=%v", rel, err)
		}
	}
}

func TestUninstall_RemovesSettingsAfterReinstall(t *testing.T) {
	root := t.TempDir() // no pre-existing settings
	if _, err := Install(Options{Root: root, Claude: true}); err != nil {
		t.Fatal(err)
	}
	// Reinstall: the manifest must not downgrade SettingsCreated true→false.
	if _, err := Install(Options{Root: root, Claude: true}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(ManifestPath(root)); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("settings.json we created should be removed even after a reinstall, stat err=%v", err)
	}
}

func TestUninstall_MissingManifestIsNoop(t *testing.T) {
	if err := Uninstall(ManifestPath(t.TempDir())); err != nil {
		t.Errorf("uninstall with no manifest should be a no-op, got %v", err)
	}
}
