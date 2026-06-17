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
blocks the tool call. The deny reason routes the agent to **`gsd-test run`**
(issue #67, ADR-0022) — the executor that runs the suite in Docker and returns a
`node --test`-style verdict, so the agent simply swaps `node --test` →
`gsd-test run`. If the command is unrelated, the hook exits 0 silently and the
tool call proceeds normally. The [`run-and-die` skill](skills/run-and-die/SKILL.md)
teaches the agent how to use `gsd-test run` and read a reaped result.

## Using the Codex shim

`gsd-test install-agent-hooks --codex` writes `codex-shim.sh` plus a `codex-bin/`
directory holding `node` and `npm` PATH shims. Point Codex's exec PATH at
`codex-bin` **first** so its `node`/`npm` route through the shim — in
`~/.codex/config.toml`:

```toml
[shell_environment_policy.set]
PATH = "/abs/path/.gsd-test/codex-bin:${PATH}"
```

When Codex runs `node --test` / `npm test` the shim **redirects it to
`gsd-test run`** (issue #69/#78, ADR-0022) — printing a handoff banner on stderr
and exec-ing the Docker-backed run, which returns a `node --test`-style verdict.
Test-file path patterns are forwarded; `node --test` flags are dropped (the
watchdog supplies its own). Every other command (e.g. `node app.js`,
`npm run lint`) is passed through to the **real** binary — the shims resolve it
by skipping their own directory (canonicalised, so symlinks can't cause
recursion), so this shadows `node`/`npm` only inside Codex, never your
interactive shell.

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
