# syntax=docker/dockerfile:1.7
FROM node:22-bookworm-slim

# Per ADR-0011: sh.gsd-test.image-version label is the sentinel. The version
# tag is injected at build time. Build with:
#   docker build -f dockerfiles/linux.Dockerfile \
#     --label sh.gsd-test.image-version=v1.4.0 \
#     -t ghcr.io/open-gsd/gsd-tester-linux:v1.4.0 .
# (The label can also be set via LABEL instruction with ARG injection — see below.)
ARG IMAGE_VERSION=dev
LABEL sh.gsd-test.image-version=$IMAGE_VERSION
LABEL org.opencontainers.image.source="https://github.com/open-gsd/gsd-test-runner"
LABEL org.opencontainers.image.description="gsd-test Tester Image (Linux)"

# Sandbox tooling per ADR-0001: base + Node runtime (from base image) +
# build toolchain (npm comes with node base) + minimal system tools that
# tests may invoke. Keep this list small — no project code, no test-suite
# specifics.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    tar \
    && rm -rf /var/lib/apt/lists/*

# Reporter at a known in-image path. ADR-0001 says the Reporter is baked
# into the image. The Local Engine's RunTests leg invokes:
#   node --test --test-reporter=/opt/gsd-test/reporter.mjs ...
# This path is contractual; do not change without updating the leg.
COPY reporter/reporter.mjs /opt/gsd-test/reporter.mjs

# Tier-1 watchdog baked alongside the reporter (issue #60, ADR-0021). The
# run-and-die path invokes it as the container entrypoint:
#   node /opt/gsd-test/watchdog.mjs --deadline-ms N -- node --test ...
# This path is contractual, like the reporter path above.
COPY reporter/watchdog.mjs /opt/gsd-test/watchdog.mjs

# Working directory matches Local Engine's CopyWorktree target (/work).
# Container is started idle (sleep infinity per Pipeline.StartContainer);
# legs docker exec into this WORKDIR.
WORKDIR /work

# HOME for npm cache + sandbox isolation. Distinct from /work so npm's
# global cache isn't co-mingled with the PR-merged worktree.
ENV HOME=/home/test
RUN mkdir -p /home/test && chmod 0777 /home/test
