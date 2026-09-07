# macOS Tart Tester Image — Packer template (ADR-0030 Phase 4, issue #134).
#
# This is the Tart-equivalent of dockerfiles/linux.Dockerfile: a versioned,
# automated recipe that bakes Node + the Reporter into a macOS guest, on top
# of a Cirrus Labs base image (required for the Tart Guest Agent — see
# ADR-0030 Decision 4). Built via HashiCorp Packer + the cirruslabs/tart
# Packer plugin (github.com/cirruslabs/packer-plugin-tart).
#
# Invoke from the REPO ROOT (not this directory) so the `file` provisioner's
# relative source path (reporter/reporter.mjs) resolves correctly:
#   packer init dockerfiles/macos-tart.pkr.hcl
#   packer build \
#     -var node_version=22 \
#     -var image_version=v1.9.0 \
#     dockerfiles/macos-tart.pkr.hcl
#
# Per ADR-0030 Decision 3, the version sentinel is baked as an in-guest file
# (/opt/gsd-test/image-version) rather than an OCI label, since Tart has no
# confirmed `tart image inspect`-equivalent read-back. See
# dockerfiles/macos-tart-provision.sh for the sentinel-write step.

packer {
  required_plugins {
    tart = {
      version = ">= 1.11.1"
      source  = "github.com/cirruslabs/tart"
    }
  }
}

variable "node_version" {
  type        = string
  description = "Node major to bake in (e.g. \"22\" or \"24\"). Mirrors linux.Dockerfile's ARG NODE_VERSION. No default -- required per build, matching this repo's DefaultNodeLTS() majors (internal/config/config.go)."
}

variable "image_version" {
  type        = string
  description = "Tester Image version sentinel (e.g. \"v1.9.0\"). Mirrors linux.Dockerfile's ARG IMAGE_VERSION. No default -- required per build."
}

variable "vm_base_name" {
  type        = string
  description = "Cirrus Labs base image to build on top of (must ship the Tart Guest Agent -- ADR-0030 Decision 4). Overridable, but defaults to the current macOS base so a plain `packer build` with just node_version/image_version set works."
  default     = "ghcr.io/cirruslabs/macos-tahoe-base:latest"
}

# vm_name is templated from node_version so concurrent builds for different
# Node majors (e.g. the publish-tester-images.yml matrix) don't collide on
# the same local Tart VM name.
source "tart-cli" "tart" {
  vm_base_name = var.vm_base_name
  vm_name      = "gsd-tester-macos-tart-node${var.node_version}"
  cpu_count    = 4
  memory_gb    = 8
  disk_size_gb = 50
  ssh_username = "admin"
  ssh_password = "admin"
  ssh_timeout  = "120s"
  headless     = true
}

build {
  sources = ["source.tart-cli.tart"]

  # Two-step copy: Packer/Tart's SSH communicator commonly can't write
  # directly to arbitrary root-owned paths as a non-root user (mirrors the
  # same permission-driven two-step pattern used for the reporter elsewhere
  # in this repo). Upload to a world-writable temp path first...
  provisioner "file" {
    source      = "reporter/reporter.mjs"
    destination = "/tmp/reporter.mjs"
  }

  # ...then the shell provisioner (macos-tart-provision.sh) moves it into
  # /opt/gsd-test/reporter.mjs -- the same contractual path linux.Dockerfile
  # uses (see CONTEXT.md) -- with correct ownership, plus does the actual
  # Xcode CLT / Homebrew / Node bake sequence and writes the version
  # sentinel file.
  provisioner "shell" {
    script = "dockerfiles/macos-tart-provision.sh"
    environment_vars = [
      "NODE_MAJOR=${var.node_version}",
      "IMAGE_VERSION=${var.image_version}",
    ]
  }
}
