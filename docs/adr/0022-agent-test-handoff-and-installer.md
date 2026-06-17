# ADR-0022 — Transparent agent test-handoff and dev-workstation installer

**Status**: Proposed (2026-06-17) — design of record for [issue #65](https://github.com/open-gsd/gsd-test-runner/issues/65). Decision 1 (the interception mechanism) is the load-bearing choice and needs maintainer sign-off before implementation.

## Context

ADR-0021 (issue #60) shipped the run-and-die engine: `gsd-test submit --execute` runs a suite in a disposable container under a watchdog + reaper and returns a structured envelope, so a hung or leaky test can never leave orphaned `node` processes on the Dev Workstation. The agent integration that ships with it (`agent-integration/`) is **deny-and-instruct**: the Claude Code `PreToolUse` hook (`route-tests.mjs`) returns a `deny` decision for `node --test` / `npm test`, and the Codex shim (`codex-shim.sh`) exits non-zero with a routing message. Neither runs the test or returns a result.

That model has three problems in practice:

1. **It is not transparent.** The agent must know about run specs and the front door, and manually translate. Friction and failure modes multiply.
2. **The agent does not get a result it recognises.** A bare `deny` is not a test outcome, so the agent may retry the local command, spawn duplicates, or stall.
3. **There is no installer.** Adopting the integration means hand-editing `.claude/settings.json`, copying the skill, and symlinking the shim — per workstation, per project.

We want: an agent runs its normal `node --test` / `npm test`, the run **transparently executes in Docker** via the existing front door, the agent is **clearly notified** the handoff happened (so it does not re-run), and it gets back **exactly the result shape a test run returns** — blocking when it needs the result, able to proceed when it does not. Installed in one command for Claude Code and Codex. This ADR records how.

Nothing here changes the run-spec contract or the two-tier reaping guarantee (ADR-0021); this is the agent-facing layer above the front door.

## Decisions

### Decision 1 — A PATH-scoped *executing* shim is the interception primitive; the Claude Code hook is a guard, not the executor (the load-bearing choice)

A Claude Code `PreToolUse` hook can only return `allow` / `deny` / `ask`; it **cannot** capture a tool's stdout and substitute a result. So a hook alone can never be transparent — it can block, but it cannot "run it elsewhere and hand back the output." Transparency requires *being the thing that executes*.

Options considered:

- **(A) Global PATH shim shadowing `node`/`npm`.** Transparent for every runtime (agents run commands through a shell that honours `PATH`). Risk: it also shadows a human's interactive `node script.js`. Mitigated the same way `codex-shim.sh` already does it — intercept **only** test invocations (`node --test…`, `npm test`, `npm run test`), and `exec` the real binary unchanged for everything else.
- **(B) Hook-only (today's model), kept as deny.** Not transparent; rejected as the primary path.
- **(C) Route the agent to a distinct wrapper command (`gsd-test run`).** Transparent-ish but not *invisible* — the agent must learn to call it (that is the skill's job). Lower magic, lower risk, but relies on the agent complying.

**Decision:** the interception primitive is an **executing shim** (option A) for `node`/`npm`, **scoped to the agent's environment** rather than the user's interactive login shell (see Decision 5 for scoping). When the shim sees a test invocation it performs the handoff (Decision 3) and emits a node-test-shaped result (Decision 2); for any non-test command it `exec`s the real binary untouched. The Claude Code `PreToolUse` hook is **retained as a belt-and-suspenders guard**: it denies any test invocation that bypasses the shim (e.g. an absolute-path `/usr/bin/node --test`) with a reason pointing at `gsd-test run`. The skill remains the agent's comprehension layer (how to read a reaped result). Codex is covered by the same shim on its exec `PATH`.

> **Maintainer decision needed:** confirm option A (executing shim + hook-as-guard) over option C (explicit `gsd-test run` wrapper the agent is routed to). A and C can share all downstream machinery; only the "is it invisible or an explicit command" question differs. The rest of this ADR assumes A with C's `gsd-test run` existing anyway as the shim's own implementation and as a manual escape hatch.

### Decision 2 — The handoff returns a node:test-compatible result, and `reaped` is rendered as a loud, attributed failure

The shim must hand back something the agent already parses as a test outcome. The repo already owns the JSON-Lines reporter (`reporter/reporter.mjs`) and the run-and-die envelope carries `per_test`, `failures`, and the `kill` record.

**Decision:** on completion the shim writes, to **stdout**, the standard `node --test` summary (and, when the caller passed a `--test-reporter`, that reporter's stream) reconstructed from the envelope, and exits with the matching code: **0** for `outcome:"passed"`, **1** for `"failed"` or `"reaped"`, **2** for an infra/spec error (mirroring `gsd-test submit`). A `reaped` outcome is printed as an unmistakable failing block that names the runaway from `kill.last_active_test` / `kill.in_flight_tests` and states it was killed for exceeding its deadline — never a silent pass, never an ambiguous flake. The agent's existing "did the tests pass?" logic then works unchanged.

### Decision 3 — Blocking by default; non-blocking dispatch is opt-in

"Proceed if it can, otherwise wait" maps onto two modes:

**Decision:** the shim **blocks by default** (the common case — the agent ran tests because it needs the verdict) and prints liveness while the container runs (Decision 4), so a long run never looks hung. A **non-blocking** mode is opt-in (`GSD_TEST_ASYNC=1` / `gsd-test run --async`): it submits, prints a one-line dispatched-notice with a stable `run-id`, and returns immediately so the agent can keep working; the result is collected later with `gsd-test wait <run-id>` (blocks for the complete envelope) or `gsd-test status <run-id>`. Async never returns a partial result — `wait` only resolves on a complete, well-formed outcome. The default stays blocking so correctness does not depend on the agent remembering to wait.

### Decision 4 — One structured notification + liveness, keyed by a stable run-id, suppresses retries

The agent must know the handoff happened and must not spawn a duplicate while it is in flight.

**Decision:** on handoff the shim emits one structured banner to **stderr**:
`↪ gsd-test: handed off to Docker (run-id=<id>, target=<os>) — do not re-run locally`, followed by periodic liveness lines for long runs. The result lands on stdout (Decision 2). Retry-suppression rests on three things: the banner names the action explicitly; blocking mode means the command simply does not return until the verdict exists (nothing to retry); and the `run-id` is stable so a re-invocation can be recognised/deduped. The Claude Code hook-guard carries the identical message when it fires, so the signal is the same whichever path triggers.

### Decision 5 — `install-agent-hooks` is the single, idempotent, reversible installer; PATH injection is agent-scoped

**Decision:** add `gsd-test install-agent-hooks` (and `--uninstall`). It detects the target runtime(s) (`--claude` / `--codex`, default both), and:

- merges the `PreToolUse` guard hook into `.claude/settings.json` **non-destructively** (preserving any existing hooks),
- installs the `run-and-die` skill into `.claude/skills/`,
- installs the `node`/`npm` shim into an **agent-scoped** `PATH` entry (a project `bin/` prepended only in the agent's execution environment, not the user's interactive login `PATH`) and wires Codex's exec `PATH` to it,
- writes a manifest so `--uninstall` reverses exactly what was added.

It is idempotent (re-running converges, never duplicates), defaults to `--project` scope with `--global` available, and prints a post-install summary of what it touched. Agent-scoped `PATH` injection is the deliberate answer to Decision 1's shadowing risk: the human's interactive `node` is untouched; only the agent's test invocations are intercepted.

## Consequences

- Agents get the orphan-proof guarantee with **zero protocol knowledge** — they run `node --test` and it Just Works in Docker, returning a normal verdict. This is the point.
- **Behaviour change for #60 adopters:** local `node --test` shifts from *blocked* to *succeeding via Docker*. It is opt-in through the installer and must be documented as a change.
- The run-spec contract stays the single stable seam; all CLI-specific glue stays in `agent-integration/` + the installer, bounding the maintenance surface.
- New surface to maintain: the envelope→node:test output renderer (Decision 2), the async `wait`/`status` path (Decision 3), the `settings.json` merge/uninstall fidelity (Decision 5), and per-runtime `PATH`/config touchpoints.
- A shim that shadows `node` is inherently invasive; agent-scoped injection (Decision 5) contains the blast radius but the scoping mechanism per runtime is the riskiest implementation detail and needs its own tests.

## Alternatives considered

- **Keep deny-and-instruct (status quo).** Rejected: not transparent, no recognisable result, the source of the retry/stall behaviour this ADR exists to fix.
- **Hook-only transparency.** Impossible: `PreToolUse` cannot substitute tool output.
- **A persistent local daemon proxying test runs.** Heavier, a new always-on process to manage, and contrary to ADR-0021 Decision 2 (no resident processes). A stateless shim over the existing front door is the smaller system (Gall's Law).
- **Explicit `gsd-test run` wrapper only, no shim (Decision 1 option C).** Lower magic and lower risk, but relies on the agent always choosing it; kept as the shim's implementation and a manual escape hatch rather than the primary UX. This is the main fork for the maintainer to confirm.

## Implementation status

Not started — this ADR is Proposed. Once Decision 1 is confirmed, the work breaks into tracer-bullet slices (installer + manifest/uninstall; envelope→node:test renderer; shim execute-and-return for both runtimes; hook-guard update; async `wait`/`status`; docs), tracked off issue #65.
