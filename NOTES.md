# Implementation worktree: images.EnsurePresent + ADR-0012

Slice scope:
- ADR-0012 covering: Bench-side GHCR auth policy (pre-logged-in,
  documented), fallback build location (on Bench via DOCKER_HOST=ssh://),
  EnsurePresent's scope (presence-only — version verification stays in
  Pipeline.CheckImageVersion per ADR-0011), Dockerfile layout convention
  (top-level `dockerfiles/<os>.Dockerfile`).
- internal/images: EnsurePresent function, dockerPull + dockerBuild +
  dockerInspect function variables (deliberately duplicated from
  internal/pipeline — see "Known duplication" below), typed errors
  (PullAuthError, PullNotFoundError, PullDockerError, BuildError,
  BenchDockerError).
- internal/images/images_test.go: stub-based unit tests covering all
  paths and error classifications.

Not in scope:
- Real Dockerfile content. The `dockerfiles/` directory layout is
  documented in ADR-0012 but creating actual Tester Image Dockerfiles
  is its own concern (a build artifact, not engine logic). Will be a
  separate slice.
- internal/dockerexec extraction. The duplication between pipeline and
  images is intentional for one more cycle — extract after a third leg
  implementation cements the patterns.

Known duplication: `dockerInspect`, `realDockerInspect`, and
`dockerExecError` exist in both internal/pipeline and (new) internal/images.
This is a deliberate Rule-of-Three violation — extract after the next
leg lands so we have three concrete uses to design against.
