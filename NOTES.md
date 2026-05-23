# Implementation worktree: LegCheckImageVersion + ADR-0011

Slice scope:
- ADR-0011 capturing the four decisions: sentinel mechanism (OCI label
  `sh.gsd-test.image-version`), Pipeline→Bench transport (DOCKER_HOST=
  ssh:// per command), expected-version source (caller-supplied to
  Pipeline.New), docker abstraction (function-variable swap for tests).
- internal/pipeline: replace runLegStub with a general runLeg helper
  that wraps every leg's LegStart/ctx-check/work/LegSuccess/LegFailure
  protocol. Migrate the 7 still-stubbed methods to runLeg.
- Implement (p *Pipeline) CheckImageVersion for real:
  - Compute DOCKER_HOST from bench.Host (empty if "local")
  - Call dockerInspect (function variable) with the version-label format
  - Classify result into nil / ImageVersionMismatch / ImageNotPresentError /
    BenchDockerError
- Pipeline.New gains an expectedVersion parameter.

Not in scope: other legs (CopyWorktree, NpmCI, etc.) stay stubbed.
