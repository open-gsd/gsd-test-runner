package installhooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	agentintegration "github.com/open-gsd/gsd-test-runner/agent-integration"
)

// Matcher is the Claude Code PreToolUse matcher the guard hook attaches to.
const Matcher = "Bash"

// Options configures an install. Root is the base directory installed into
// (the project dir for --project, $HOME for --global). At least one runtime
// must be selected.
type Options struct {
	Root   string
	Claude bool
	Codex  bool
}

// Manifest records exactly what an install added so Uninstall can reverse it.
type Manifest struct {
	Files           []string `json:"files"`                      // files created, for deletion
	SettingsPath    string   `json:"settings_path,omitempty"`    // settings.json that was merged
	SettingsCreated bool     `json:"settings_created,omitempty"` // true if we created settings.json (delete on uninstall if emptied)
	HookCommand     string   `json:"hook_command,omitempty"`     // the PreToolUse command added
	Matcher         string   `json:"matcher,omitempty"`
}

// ManifestPath is where the install manifest is written under Root.
func ManifestPath(root string) string {
	return filepath.Join(root, ".gsd-test", "install-manifest.json")
}

// Install writes the selected runtime integration under opts.Root and returns
// the manifest (also persisted to ManifestPath). It is idempotent: re-running
// converges (the settings merge is non-destructive and dedup'd, files are
// rewritten with identical content) and never injects a node-shadowing PATH
// entry (the executor is the explicit `gsd-test run`, ADR-0022 D1/D5).
func Install(opts Options) (Manifest, error) {
	if !opts.Claude && !opts.Codex {
		return Manifest{}, fmt.Errorf("install: select at least one runtime (--claude / --codex)")
	}
	var man Manifest

	if opts.Claude {
		// Hook script the PreToolUse hook invokes.
		hookScript := filepath.Join(opts.Root, ".claude", "gsd-test", "route-tests.mjs")
		if err := writeFile(hookScript, agentintegration.RouteTests(), 0o644); err != nil {
			return Manifest{}, err
		}
		man.Files = append(man.Files, hookScript)

		// run-and-die skill.
		skill := filepath.Join(opts.Root, ".claude", "skills", "run-and-die", "SKILL.md")
		if err := writeFile(skill, agentintegration.SkillMD(), 0o644); err != nil {
			return Manifest{}, err
		}
		man.Files = append(man.Files, skill)

		// Non-destructive settings.json merge.
		settingsPath := filepath.Join(opts.Root, ".claude", "settings.json")
		// Shell-quote the script path so a Root with spaces still yields a valid
		// hook command. RemoveHook matches the identical (manifest-recorded) string.
		hookCommand := fmt.Sprintf("node %q", hookScript)
		_, statErr := os.Stat(settingsPath)
		man.SettingsCreated = os.IsNotExist(statErr)
		existing, _ := os.ReadFile(settingsPath) // missing → empty, handled by MergeHook
		merged, _, err := MergeHook(existing, Matcher, hookCommand)
		if err != nil {
			return Manifest{}, err
		}
		if err := writeFile(settingsPath, append(merged, '\n'), 0o644); err != nil {
			return Manifest{}, err
		}
		man.SettingsPath = settingsPath
		man.HookCommand = hookCommand
		man.Matcher = Matcher
	}

	if opts.Codex {
		shim := filepath.Join(opts.Root, ".gsd-test", "codex-shim.sh")
		if err := writeFile(shim, agentintegration.CodexShim(), 0o755); err != nil {
			return Manifest{}, err
		}
		if err := os.Chmod(shim, 0o755); err != nil { // repair mode on reinstall (WriteFile only sets it on create)
			return Manifest{}, fmt.Errorf("chmod %s: %w", shim, err)
		}
		man.Files = append(man.Files, shim)
	}

	// Accumulate with any prior install so a single --uninstall reverses
	// everything ever installed under this Root, and the first install's
	// SettingsCreated stays authoritative across reinstalls.
	man = mergeManifests(loadManifest(ManifestPath(opts.Root)), man)

	if err := writeFile(ManifestPath(opts.Root), mustJSON(man), 0o644); err != nil {
		return Manifest{}, err
	}
	return man, nil
}

// loadManifest reads a manifest, returning the zero value if absent/unreadable.
func loadManifest(path string) Manifest {
	var m Manifest
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// mergeManifests unions a prior install record with the current one so an
// uninstall after several installs reverses all of them. Settings fields from
// whichever record actually touched settings win; SettingsCreated is taken from
// the first install that touched settings (so a reinstall can't downgrade it).
func mergeManifests(prior, cur Manifest) Manifest {
	out := Manifest{}
	seen := map[string]bool{}
	for _, f := range append(append([]string{}, prior.Files...), cur.Files...) {
		if !seen[f] {
			seen[f] = true
			out.Files = append(out.Files, f)
		}
	}
	switch {
	case prior.SettingsPath != "":
		// Settings already recorded by an earlier install — that record (and its
		// SettingsCreated) is authoritative.
		out.SettingsPath, out.HookCommand, out.Matcher = prior.SettingsPath, prior.HookCommand, prior.Matcher
		out.SettingsCreated = prior.SettingsCreated
	case cur.SettingsPath != "":
		out.SettingsPath, out.HookCommand, out.Matcher = cur.SettingsPath, cur.HookCommand, cur.Matcher
		out.SettingsCreated = cur.SettingsCreated
	}
	return out
}

// Uninstall reverses the install recorded at manifestPath: it removes our
// PreToolUse hook from settings.json (preserving any other hooks/keys), deletes
// every installed file, prunes now-empty install dirs, and removes the
// manifest. Missing pieces are tolerated (idempotent).
func Uninstall(manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing installed
		}
		return fmt.Errorf("uninstall: read manifest: %w", err)
	}
	var man Manifest
	if err := json.Unmarshal(data, &man); err != nil {
		return fmt.Errorf("uninstall: parse manifest: %w", err)
	}

	if man.SettingsPath != "" && man.HookCommand != "" {
		if existing, rErr := os.ReadFile(man.SettingsPath); rErr == nil {
			cleaned, _, mErr := RemoveHook(existing, man.Matcher, man.HookCommand)
			if mErr != nil {
				return fmt.Errorf("uninstall: edit settings: %w", mErr)
			}
			// If we created settings.json and removing our hook empties it, delete
			// it so the uninstall is exact; otherwise write the cleaned content.
			if man.SettingsCreated && strings.TrimSpace(string(cleaned)) == "{}" {
				if rmErr := os.Remove(man.SettingsPath); rmErr != nil && !os.IsNotExist(rmErr) {
					return fmt.Errorf("uninstall: remove created settings: %w", rmErr)
				}
			} else if wErr := os.WriteFile(man.SettingsPath, append(cleaned, '\n'), 0o644); wErr != nil {
				return fmt.Errorf("uninstall: write settings: %w", wErr)
			}
		}
	}

	for _, f := range man.Files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("uninstall: remove %s: %w", f, err)
		}
	}
	// root is the parent of the .gsd-test dir that holds the manifest.
	root := filepath.Dir(filepath.Dir(manifestPath))
	pruneEmptyParents(root, append(man.Files, manifestPath))

	if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("uninstall: remove manifest: %w", err)
	}
	return nil
}

// writeFile creates parent dirs and writes data.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// pruneEmptyParents removes now-empty directories that held installed files,
// deepest first, best-effort. It only touches directories STRICTLY under root,
// never root itself or any ancestor — so uninstall can never delete the user's
// project/home directory even if it happens to be otherwise empty.
func pruneEmptyParents(root string, paths []string) {
	prefix := root + string(filepath.Separator)
	dirs := map[string]bool{}
	for _, p := range paths {
		for d := filepath.Dir(p); strings.HasPrefix(d, prefix); d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, d := range ordered {
		_ = os.Remove(d) // fails (non-empty) → left in place, which is correct
	}
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}
