# syntax=docker/dockerfile:1.7
#
# macOS Tester Image for Apple Containers (macOS 26+).
#
# Apple Containers accepts Dockerfile syntax (it's an OCI builder). The
# .containerfile extension is convention to signal a non-Docker runtime
# at the file-layout level per ADR-0020 dec 3.
#
# Base image: an Apple-published macOS sandbox image. As of macOS 26 GA,
# Apple does not publish official macOS base images for Apple Containers
# to a public registry — projects either build their own or use the
# Apple-recommended approach (TBD on Apple's public roadmap). This file
# is groundwork; the FROM line will be replaced when an official base
# is available.
#
# Until then, this file documents the structure of what the macOS Tester
# Image will look like.

# FROM <apple-macos-base-image>  # blocked on Apple publishing this
FROM scratch  # placeholder so the file is syntactically valid

ARG IMAGE_VERSION=dev
LABEL sh.gsd-test.image-version=$IMAGE_VERSION
LABEL org.opencontainers.image.source="https://github.com/open-gsd/gsd-test-runner"
LABEL org.opencontainers.image.description="gsd-test Tester Image (macOS via Apple Containers)"

# Node 22 + git + tar — same baseline as the Linux Tester Image.
# Installation mechanism TBD: brew is the natural macOS choice once the
# base image supports it.

# Reporter at a known in-image path. Same contract as Linux/Windows
# Tester Images per ADR-0001.
COPY reporter/reporter.mjs /opt/gsd-test/reporter.mjs

WORKDIR /work
