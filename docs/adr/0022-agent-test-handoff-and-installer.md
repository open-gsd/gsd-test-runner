# ADR-0022 — Transparent agent test-handoff and dev-workstation installer

**Status**: Accepted (2026-06-17) — design of record for [issue #65](https://github.com/open-gsd/gsd-test-runner/issues/65). Decision 1 (the interception mechanism) was ratified in favour of the explicit `gsd-test run` wrapper.

## Context

ADR-0021 (issue #60) shipped the run-and-die engine: `gsd-test submit --execute` runs a suite in a disposable container under a watchdog + reaper and returns a structured envelope, so a hung or leaky test can never leave orphaned `node` processes on the Dev Workstation. The agent integration that ships with it (`agent-integration/`) is **deny-and-instruct**: the Claude Code `PreToolUse` hook (`route-tests.mjs`) returns a `deny` decision for `node --test` / `npm test`, and the Codex shim (`codex-shim.sh`) exits non-zero with a routing message. Neither runs the test or returns a result.

That model has three problems in practice:

1. **It is friction-heavy.** The agent must know about run specs and the front door, and hand-craft one. Failure modes multiply.
2. **The agent does not get a result it recognises.** A bare `deny` is not a test outcome, so the agent may retry the local command, spawn duplicates, or stall.
3. **There is no installer.** Adopting the integration means hand-editing `.claude/settings.json`, copying the skill, and placing the shim — per workstation, per project.

We want: an agent runs tests through one simple command, the run **executes in Docker** via the existing front door, the agent is **clearly notified** the handoff happened (so it does not re-run), and it gets back **exactly the result shape a test run returns** — blocking when it needs the result, able to proceed when it does not. Installed in one command for Claude Code and Codex. This ADR records how.

Nothing here changes the run-spec contract or the two-tier reaping guarantee (ADR-0021); this is the agent-facing layer above the front door.

## Decisions

### Decision 1 — The executor is an explicit `gsd-test run` wrapper; agents are *routed* to it (ratified)

A Claude Code `PreToolUse` hook can only return `allow` / `deny` / `ask`; it **cannot** capture a tool's stdout and substitute a result. So whatever executes the run and hands back a node:test result must be a *named command the agent invokes*, not the hook.

Options considered:

- **(A) PATH shim shadowing `node`/`npm`** that does the handoff invisibly. Maximally transparent, but shadowing `node` on `PATH` is invasive — it must perfectly pass through every non-test `node`/`npm` use, and the scoping needed to avoid hijacking a human's interactive shell is the riskiest part of the whole design.
- **(B) Hook-only deny (today's model).** Not an executor; no recognisable result. Rejected.
- **(C) An explicit `gsd-test run` wrapper** the agent is routed to: a single named command that builds a run spec from the cwd + args, calls `gsd-test submit --execute`, and renders a node:test-shaped result + exit code (Decision 2).

**Decision:** the executor is the **explicit `gsd-test run` wrapper** (option C). It is the one place that performs the handoff, and agents are *routed* to it rather than having `node` transparently shadowed:

- **Claude Code** — the `PreToolUse` hook keeps denying `node --test` / `npm test`, but its reason now names `gsd-test run` (a command that actually executes and returns a verdict, not a hand-built run spec), and the `run-and-die` skill teaches the agent to call `gsd-test run`. The agent invokes it by name.
- **Codex** — `codex-shim.sh` rewrites a matched `node --test …` / `npm test` into `gsd-test run …` and execs it (Codex's only interception point is the exec `PATH`, so the shim redirects to the wrapper rather than shadowing `node` for general use).

This keeps the magic low and visible: there is exactly one executor (`gsd-test run`), it is a named command, and nothing transparently shadows `node` for non-test use. The PATH-shim transparency route (A) is rejected — its `node`-shadowing blast radius is not worth the invisibility (see Alternatives).

### Decision 2 — `gsd-test run` returns a node:test-compatible result, and `reaped` is rendered as a loud, attributed failure

The wrapper must hand back something the agent already parses as a test outcome. The repo already owns the JSON-Lines reporter (`reporter/reporter.mjs`) and the run-and-die envelope carries `per_test`, `failures`, and the `kill` record.

**Decision:** on completion `gsd-test run` writes, to **stdout**, the standard `node --test` summary (and, when the caller passed a `--test-reporter`, that reporter's stream) reconstructed from the envelope, and exits with the matching code: **0** for `outcome:"passed"`, **1** for `"failed"` or `"reaped"`, **2** for an infra/spec error (mirroring `gsd-test submit`). A `reaped` outcome is printed as an unmistakable failing block that names the runaway from `kill.last_active_test` / `kill.in_flight_tests` and states it was killed for exceeding its deadline — never a silent pass, never an ambiguous flake. The agent's existing "did the tests pass?" logic then works unchanged.

### Decision 3 — Blocking by default; non-blocking dispatch is opt-in

"Proceed if it can, otherwise wait" maps onto two modes:

**Decision:** `gsd-test run` **blocks by default** (the common case — the agent ran tests because it needs the verdict) and prints liveness while the container runs (Decision 4), so a long run never looks hung. A **non-blocking** mode is opt-in (`gsd-test run --async`): it submits, prints a one-line dispatched-notice with a stable `run-id`, and returns immediately so the agent can keep working; the result is collected later with `gsd-test wait <run-id>` (blocks for the complete envelope) or `gsd-test status <run-id>`. Async never returns a partial result — `wait` only resolves on a complete, well-formed outcome. The default stays blocking so correctness does not depend on the agent remembering to wait.

### Decision 4 — One structured notification + liveness, keyed by a stable run-id, suppresses retries

The agent must know the handoff happened and must not spawn a duplicate while it is in flight.

**Decision:** on handoff `gsd-test run` emits one structured banner to **stderr**:
`↪ gsd-test: handed off to Docker (run-id=<id>, target=<os>) — do not re-run locally`, followed by periodic liveness lines for long runs. The result lands on stdout (Decision 2). Retry-suppression rests on three things: the banner names the action explicitly; blocking mode means the command simply does not return until the verdict exists (nothing to retry); and the `run-id` is stable so a re-invocation can be recognised/deduped. The Claude Code deny-hook carries the same `gsd-test run` instruction, so the routing signal is consistent whichever path triggers.

### Decision 5 — `install-agent-hooks` is the single, idempotent, reversible installer

**Decision:** add `gsd-test install-agent-hooks` (and `--uninstall`). It detects the target runtime(s) (`--claude` / `--codex`, default both), and:

- ensures the `gsd-test` binary is discoverable on the agent's `PATH`,
- merges the `PreToolUse` guard hook into `.claude/settings.json` **non-destructively** (preserving any existing hooks), with its reason pointing at `gsd-test run`,
- installs the `run-and-die` skill into `.claude/skills/` (teaching `gsd-test run`),
- installs the Codex shim and points Codex's exec `PATH` at it (the shim redirects `node --test`/`npm test` → `gsd-test run`),
- writes a manifest so `--uninstall` reverses exactly what was added.

It is idempotent (re-running converges, never duplicates), defaults to `--project` scope with `--global` available, and prints a post-install summary of what it touched. Because the executor is an explicit command (Decision 1), the installer does **not** inject a `node`-shadowing `PATH` entry — the human's interactive `node` is untouched by construction.

## Consequences

- Agents get the orphan-proof guarantee through **one simple command** — `gsd-test run` behaves like `node --test` but runs in Docker and returns a normal verdict. Claude Code is routed to it by the deny-hook + skill; Codex has `node --test` redirected to it by the shim. **Local `node --test` is never executed on the workstation.**
- **Behaviour change for #60 adopters:** the deny-hook's message changes (it now names `gsd-test run`, a working executor, instead of a hand-built run spec), and Codex's shim shifts from *block* to *redirect-and-execute*. Opt-in through the installer; documented.
- Choosing the explicit wrapper over a `node`-shadowing shim **removes the riskiest part of the design** — there is no global `node` interception to scope, so a human's interactive `node` use is never at risk.
- The flip side: routing relies on the integration being installed. A raw bypass (`/usr/bin/node --test` on Claude Code) is still caught by the `PreToolUse` hook; on Codex the protection depends on the shim being first on `PATH`. The installer and docs must make that contract explicit.
- The run-spec contract stays the single stable seam; all CLI-specific glue stays in `agent-integration/` + the installer, bounding the maintenance surface. New surface: the envelope→node:test renderer (Decision 2), the async `wait`/`status` path (Decision 3), and the `settings.json` merge/uninstall fidelity (Decision 5).

## Alternatives considered

- **Keep deny-and-instruct (status quo).** Rejected: friction-heavy, no recognisable result, the source of the retry/stall behaviour this ADR exists to fix.
- **Transparent PATH shim shadowing `node`/`npm` (Decision 1 option A).** Rejected: invisibility is attractive, but a shim that shadows `node` must flawlessly pass through every non-test use and be scoped so it never hijacks a human's interactive shell — the largest risk in the design — for a UX gain (the agent typing `node --test` instead of `gsd-test run`) that the deny-hook + skill already deliver at near-zero cost. The Codex shim still *redirects* `node --test` to the wrapper, but it does not become a general `node` replacement.
- **Hook-only transparency.** Impossible: `PreToolUse` cannot substitute tool output.
- **A persistent local daemon proxying test runs.** Heavier, a new always-on process to manage, and contrary to ADR-0021 Decision 2 (no resident processes). A stateless wrapper over the existing front door is the smaller system (Gall's Law).

## Implementation status

Not started — ratified and ready to slice. The work breaks into tracer-bullet slices off issue #65: the `gsd-test run` wrapper + envelope→node:test renderer (the spine); Claude Code routing (deny-hook reason + skill update); Codex shim redirect; the async `wait`/`status` path; the `install-agent-hooks` installer + manifest/uninstall; and the Diátaxis docs (how-to + reference).
