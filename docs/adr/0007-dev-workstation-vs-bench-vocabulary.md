# 0007 — Dev Workstation and Bench are distinct named roles

Status: Accepted (2026-05-23)

## Context

The transitional vocabulary used `Host` for the SSH-reachable machine that runs containers, with no name for the developer's own machine — it was implicit. As the target architecture emerged, half of the failure messages and runbook procedures will reference one role; half will reference the other. Conflating them, or using ambiguous terms ("host," "machine," "runner"), produces messages like "Host unreachable" that don't tell the reader whether their laptop is broken or their lab box is offline.

The replacement term for the SSH-reachable role had to avoid overload with existing CI/Docker vocabulary: "Runner" collides with GitHub Actions runners and the transitional Runner module, "Node" collides with Node.js, "Worker" collides with CI workers, "Engine" collides with Local Engine.

## Decision

Two terms, used exclusively from this point forward:

- **Dev Workstation** — the developer's own machine where the Local Engine runs. Platform-agnostic (macOS, Linux, Windows). Does not run containers itself.
- **Bench** — a remote SSH-reachable machine that runs containerized test suites on behalf of a Dev Workstation. One Bench per target OS family. Typically the developer's own hardware (desktop, home lab, spare workstation), not shared infrastructure. Named after the engineering "test bench."

Error messages, runbooks, configuration files, and ADRs use these terms. The transitional `Host` term is preserved only in the Transitional vocabulary section of CONTEXT.md as a mapping aid for readers of legacy code.

## Consequences

+ Failure messages can name the responsible role precisely: "Linux Bench `bench-linux-1` unreachable" vs "Dev Workstation cannot resolve GHCR auth."
+ Configuration vocabulary becomes self-documenting: `~/.config/gsd-test/benches/linux` is obviously the Linux Bench config; `~/.config/gsd-test/benches/windows` the Windows Bench.
+ Contributors reading source code or docs see one term per role, no inference required.
- Pre-existing user installs with `~/.config/gsd-test/hosts` need a migration path when the target architecture lands. Plan: read both paths during transition, prefer benches/ if present, log a deprecation notice when hosts/ is used.
- Any external documentation, blog posts, or contributor scripts referring to "hosts" will need updating.

## Alternatives considered

- Container Host — Rejected: accurate but "host" remains overloaded with web/networking usage; two words where one suffices.
- Sandbox Host — Rejected: "sandbox" overloaded with security usage; the Tester Image itself is described as a sandbox in ADR-0001, creating a Sandbox-Host-runs-Sandbox-Image stutter.
- Executor — Rejected: generic; doesn't convey "remote machine you SSH to."
- Tester Node — Rejected: "Node" collides with Node.js, the runtime the tests use.
- Runner Host — Rejected: "Runner" collides with GitHub Actions and the transitional Runner module.
- Keep `Host` — Rejected: ambiguous (could refer to either role); colocated with existing transitional usage; missed opportunity to separate the two roles cleanly in error messages.
