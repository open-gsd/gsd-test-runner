# agent-integration

Hooks that prevent coding agents (Claude Code, Codex) from spawning local
`node --test` / `npm test` processes. Instead, agents are routed to
`gsd-test submit` — the run-spec front door described in ADR-0021 §G and
[issue #60](https://github.com/open-gsd/gsd-test-runner/issues/60).

## Why

Running `node --test` directly on the Dev Workstation creates orphaned `node`
children that outlive the agent turn. ADR-0021 Decision 2 makes the Bench
(not the Workstation) the execution site; the hook enforces that contract.

## Files

| File | Purpose |
|---|---|
| `route-tests.mjs` | Node ESM module — pure logic + Claude Code PreToolUse hook entrypoint |
| `route-tests.test.mjs` | `node --test` unit tests for the pure functions |
| `codex-shim.sh` | POSIX sh exec-path shim for Codex's exec path |

## Wiring the Claude Code hook

Add a `PreToolUse` hook to `.claude/settings.json`:

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
node test invocation, it writes a `deny` decision to stdout and Claude Code
blocks the tool call. If the command is unrelated, the hook exits 0 silently
and the tool call proceeds normally.

## Using the Codex shim

Place `codex-shim.sh` on `PATH` before the real `node`/`npm` binaries (e.g.
symlink it from `~/bin/node-test-guard`). When Codex executes a node test
command the shim prints a routing message on stderr and exits non-zero;
other commands are exec'd unchanged.

## Run-spec contract

Submit a run spec to the front door:

```sh
echo '{"repo":"my-repo","suite":"unit","estimateMs":30000}' \
  | gsd-test submit --spec-file -
```

See ADR-0021 for the full run-spec schema and the two-tier reaping contract.

## Claude Code skill

A ready-to-install skill lives at [`skills/run-and-die/SKILL.md`](skills/run-and-die/SKILL.md).
Copy that directory into your project's `.claude/skills/` so the agent knows the
run-spec contract and how to read a reaped result. It pairs with the PreToolUse
hook above: the hook blocks local `node --test`, the skill teaches the agent
what to do instead.
