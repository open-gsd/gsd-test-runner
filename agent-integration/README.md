# agent-integration

Hooks that prevent coding agents (Claude Code, Codex) from spawning local
`node --test` / `npm test` processes and route them to `gsd-test run` instead —
the explicit executor that runs the suite in Docker and returns a
`node --test`-style verdict. Designed for issue #65 (ADR-0022).

## Why

Running `node --test` directly on the Dev Workstation creates orphaned `node`
children that outlive the agent turn. ADR-0021 Decision 2 makes the Bench
(not the Workstation) the execution site; the hook enforces that contract.
ADR-0022 adds the `gsd-test run` wrapper so agents get a recognisable
test result — not just a denial — and `install-agent-hooks` so wiring is a
single command rather than hand-editing config files.

## Files

| File | Purpose |
|---|---|
| `route-tests.mjs` | Node ESM module — pure logic + Claude Code PreToolUse hook entrypoint |
| `route-tests.test.mjs` | `node --test` unit tests for the pure functions |
| `codex-shim.sh` | POSIX sh exec-path shim for Codex's exec path |
| `skills/run-and-die/` | Claude Code skill — teaches the agent to call `gsd-test run` |

## Installing the agent integration

Run the installer from the project root:

```bash
gsd-test install-agent-hooks
```

This installs both Claude Code and Codex in one idempotent, reversible step —
no manual editing required. See
[docs/run-and-die-how-to.md](../docs/run-and-die-how-to.md) for the
step-by-step recipe and verification guide.

To install only one runtime: `--claude` or `--codex`. To install globally
(into `$HOME`): `--global`. To reverse: `--uninstall`.

See the [reference](../docs/run-and-die-reference.md#gsd-test-install-agent-hooks)
for all flags and exit codes.

## What the installer does (manual fallback)

The details below describe what `install-agent-hooks` configures. They are
provided for understanding or for environments where you need to apply the
wiring by hand. The installer is the recommended path; these steps are the
fallback.

### Claude Code hook

The installer merges a `PreToolUse` guard into `.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "node agent-integration/route-tests.mjs"
          }
        ]
      }
    ]
  }
}
```

The hook reads the Claude Code payload from stdin. If the Bash command is a
`node --test` / `npm test` invocation, it writes a `deny` decision to stdout
with a reason naming `gsd-test run`. The agent then calls `gsd-test run`
directly — which executes the suite in Docker and returns a
`node --test`-style verdict (ADR-0022 Decision 1). Unrelated commands pass
through untouched.

### Claude Code skill

The installer copies `skills/run-and-die/` into `.claude/skills/`. This
teaches the agent to call `gsd-test run`, interpret the handoff banner, and
read a reaped result. It pairs with the hook: the hook routes, the skill
instructs.

To install the skill manually, copy the directory:

```bash
cp -r agent-integration/skills/run-and-die .claude/skills/
```

### Codex shim

The installer writes a `codex-bin/` directory containing `node` and `npm`
PATH shims. Add it **first** on Codex's exec PATH in `~/.codex/config.toml`:

```toml
[shell_environment_policy.set]
PATH = "/abs/path/.gsd-test/codex-bin:${PATH}"
```

(The exact path is printed by the installer.)

When Codex runs `node --test` / `npm test`, the shim **redirects it to
`gsd-test run`** — printing the handoff banner on stderr and exec-ing the
Docker-backed run, which returns a `node --test`-style verdict (ADR-0022
Decisions 1 and 2). Test-file path patterns are forwarded; `node --test`
flags are dropped (the watchdog supplies its own). Every other command (e.g.
`node app.js`, `npm run lint`) is passed through to the real binary — the
shims resolve it by skipping their own directory (canonicalised, so symlinks
cannot cause recursion), so this shadows `node`/`npm` only inside Codex,
never your interactive shell.

## Behaviour change from issue #60

Earlier adopters (ADR-0021, issue #60) wired a deny-and-instruct hook: `node
--test` was blocked and the agent had to hand-craft a run spec for
`gsd-test submit`. ADR-0022 (issue #65) replaces that model: the hook's deny
reason now names `gsd-test run` (a working executor), and Codex's shim shifts
from block to redirect-and-execute. `gsd-test run` returns a normal
`node --test`-style verdict — the agent no longer needs to know about run
specs. The run-spec contract and the two-tier reaping guarantee are unchanged.
