# 0011 — Image-version sentinel, Bench transport, and docker-call abstraction

Status: Accepted (2026-05-23)

## Context

ADR-0001 establishes that each Tester Image carries an Image-version sentinel that the Local Engine verifies before running tests. ADR-0008 places that verification in the Per-OS Pipeline's first leg, `CheckImageVersion`. Implementing that leg forces four concrete decisions the architecture ADRs did not pre-commit:

1. **What form does the sentinel take?** A file inside the image, an OCI label, an environment variable, or just the image tag.
2. **How does the Pipeline invoke docker on a remote Bench?** Wrap docker in an SSH command, use named Docker contexts, or use the `DOCKER_HOST=ssh://` env var per invocation.
3. **Where does the expected version come from?** Hardcoded at build time, loaded from a repo config file, an env var, or supplied by the caller.
4. **How is docker abstracted for testability?** A Go interface with mocks, function-variable swap, or no abstraction at all (real docker only).

Each leg subsequent to `CheckImageVersion` will reuse the answers to #2 and #4 — so getting these right once compounds across the remaining 7 legs.

## Decision

**1. Sentinel mechanism: OCI label `sh.gsd-test.image-version`.** Each Tester Image's Dockerfile declares `LABEL sh.gsd-test.image-version=<version>`. The Local Engine reads it via `docker image inspect <image> --format '{{ index .Config.Labels "sh.gsd-test.image-version" }}'`. Reverse-DNS naming (`sh.gsd-test.*`) avoids collision with OCI standard labels and signals project ownership.

**2. Pipeline → Bench transport: `DOCKER_HOST=ssh://<bench.Host>` per command.** The Pipeline sets the env var on each `exec.Command` invocation. Docker's native SSH transport handles the connection. For a Bench with `Host: "local"`, the env var is omitted and docker runs against the Dev Workstation's local daemon.

**3. Expected version source: caller-supplied to `Pipeline.New`.** `Pipeline.New(bench, image, expectedVersion, worktreePath, events)` takes the version as an explicit parameter. The Pipeline does not know where the value came from. The orchestrator (`cmd/gsd-test/main.go`) will eventually load it from a repo config file (probably `.gsd-test/versions.json` mapping OS → version), but that policy decision is deferred until `internal/config/` is implemented.

**4. Docker abstraction: function-variable swap.** Each docker invocation goes through a package-level variable in `internal/pipeline/`:

```go
var dockerInspect = realDockerInspect

func realDockerInspect(ctx context.Context, dockerHost, image, format string) (string, error)
```

Tests swap the variable for a stub returning canned output, restoring it via `t.Cleanup`. No interface, no mock framework. Future docker wrappers (`dockerRun`, `dockerExec`, etc.) follow the same pattern.

## Consequences

+ Verification is fast: `docker image inspect` is metadata-only, no container start. A `CheckImageVersion` leg completes in <100ms on a warm SSH connection.
+ The sentinel is forgery-resistant: anyone can `docker tag` an image to a different name, but only a fresh build can write the label. Belt-and-suspenders against accidental tag-juggling on a Bench.
+ Zero per-Dev-Workstation setup for the transport: `DOCKER_HOST=ssh://` works as soon as SSH does. No Docker context registration to maintain across machines.
+ The Pipeline does not couple to config-file format. When `internal/config/` lands, it picks the version-source policy without touching pipeline code.
+ Every subsequent leg becomes unit-testable end-to-end without a docker daemon by stubbing the relevant `docker*` function variable. Integration tests against a real Bench become a separate "smoke" tier, not the only way to test.
- The OCI label name is now a load-bearing contract: every Tester Image build (in this repo's CI per ADR-0005) must set `sh.gsd-test.image-version`. A future Dockerfile typo (`sh.gsd-test.tester-version` etc.) is a silent regression — CheckImageVersion will report "version mismatch (Actual='')" on every Bench.
- The `DOCKER_HOST=ssh://` transport requires SSH config to be reachable as the executing user on the Dev Workstation. Headless CI runs (if any are ever built atop the Local Engine) need their SSH config plumbed accordingly.
- Function-variable swapping is not goroutine-safe for tests running in parallel within the same package — `t.Cleanup` restores the variable, but two `t.Parallel()` tests swapping the same variable would race. Per ADR-0008's threading model, Pipeline-internal tests don't run in parallel within the same package, so this is a non-issue in practice. Documented here so it stays a non-issue.

## Alternatives considered

- **Sentinel as in-image file (`/etc/gsd-tester-version`)** — Rejected: requires `docker run` to read (full container start), slow per-check. Adds a redundant cold-path for a metadata lookup.
- **Sentinel as ENV var** — Rejected: less standard than OCI labels for "metadata about the image"; would require `docker inspect` parsing of the env array, more code for the same outcome.
- **Sentinel as tag-only** — Rejected: tags are mutable and can be retargeted with `docker tag`; the label is the forgery-resistant integrity check.
- **Transport via SSH-wrapped docker (`ssh bench "docker ..."`)** — Rejected: requires shell-escaping docker args inside the SSH command; errors return as ssh's stderr, not docker's structured output. Hides docker's exit codes behind ssh's.
- **Transport via named Docker contexts (`docker --context bench-linux-1`)** — Rejected: requires per-Dev-Workstation `docker context create` setup. Friction for the cross-platform, mixed-contributor audience ADR-0006 targets.
- **Expected version hardcoded at build time (`-ldflags "-X ..."`)** — Rejected: forces a Local Engine binary rebuild every time the expected Tester Image version changes. Defeats the "static binary you download once" ergonomic.
- **Expected version from env var** — Rejected: makes the version invisible in `git diff` and easy to drift across contributors. Repo-config-file (the deferred future policy) is the right answer for that question; meanwhile, caller-supplied is policy-neutral.
- **Docker abstraction via Go interface** — Rejected: interface ceremony with one real implementation is mock-pattern programming, not design. Function-variable swap gives the same testability with less code.
- **No docker abstraction (integration tests only)** — Rejected: requires every test environment (laptops, CI, contributor machines) to have docker installed and a docker daemon available. Hostile to contribution. Function-variable swap eliminates the requirement for unit tests.
