#!/bin/bash
# macos-tart-provision.sh — Packer shell provisioner for the macOS Tart
# Tester Image (ADR-0030 Phase 4, issue #134). Runs INSIDE the guest VM,
# invoked by dockerfiles/macos-tart.pkr.hcl's shell provisioner.
#
# Per ADR-0004 (fail-loud at every pipeline leg), this script does not
# silently continue past a broken toolchain step -- `set -euo pipefail`
# plus explicit verification of the Node/npm install (step 4 below) means
# any failure here aborts the bake non-zero rather than shipping a broken
# image.
#
# Expects two environment variables, passed in via Packer's
# `environment_vars` on the shell provisioner:
#   NODE_MAJOR     e.g. "22" or "24" (from -var node_version)
#   IMAGE_VERSION  e.g. "v1.9.0"     (from -var image_version)

set -euo pipefail

: "${NODE_MAJOR:?NODE_MAJOR must be set (e.g. 22 or 24)}"
: "${IMAGE_VERSION:?IMAGE_VERSION must be set (e.g. v1.9.0)}"

echo "==> Provisioning macOS Tart Tester Image (node${NODE_MAJOR}, ${IMAGE_VERSION})"

# ---------------------------------------------------------------------------
# 1. Headless Xcode Command Line Tools install.
#
# Homebrew's installer does NOT install the CLT for you (confirmed against
# docs.brew.sh/Installation) -- it must be installed separately. A plain
# `xcode-select --install` pops an interactive GUI dialog that would hang a
# headless/CI script, so use the same trick GitHub's own macOS runner images
# and other CI providers use: a placeholder file tricks `softwareupdate`
# into listing the CLT package non-interactively.
# ---------------------------------------------------------------------------
echo "==> Installing Xcode Command Line Tools (headless)"
if ! xcode-select -p >/dev/null 2>&1; then
  CLT_PLACEHOLDER="/tmp/.com.apple.dt.CommandLineTools.installondemand.in-progress"
  sudo touch "$CLT_PLACEHOLDER"
  # softwareupdate -ia ("install all") picks up the CLT package once the
  # placeholder file is present -- the standard headless-CI workaround for
  # what would otherwise be an interactive `xcode-select --install` dialog.
  if ! sudo softwareupdate -ia --verbose; then
    sudo rm -f "$CLT_PLACEHOLDER"
    echo "FATAL: 'softwareupdate -ia' failed to install Command Line Tools" >&2
    exit 1
  fi
  sudo rm -f "$CLT_PLACEHOLDER"
  if ! xcode-select -p >/dev/null 2>&1; then
    echo "FATAL: Command Line Tools still not present after 'softwareupdate -ia'" >&2
    exit 1
  fi
else
  echo "    already installed, skipping"
fi

# ---------------------------------------------------------------------------
# 2. Non-interactive Homebrew bootstrap.
# ---------------------------------------------------------------------------
if ! command -v brew >/dev/null 2>&1; then
  echo "==> Installing Homebrew (non-interactive)"
  NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
fi
# Apple Silicon Homebrew installs to /opt/homebrew; make brew itself
# reachable for the rest of this script (non-interactive SSH sessions get a
# minimal PATH -- /usr/bin:/bin:/usr/sbin:/sbin -- with no Homebrew dir).
eval "$(/opt/homebrew/bin/brew shellenv)"

# ---------------------------------------------------------------------------
# 3. Install Node via Homebrew.
#
# NOTE: brew install node@${NODE_MAJOR} always resolves to whatever patch
# release is currently in homebrew-core -- unlike a Docker image tag, there
# is no way to pin an exact patch (e.g. 22.19.0) here. This is a known,
# accepted limitation of the Homebrew-based provenance choice (ADR-0030
# discussion), not something this script works around.
#
# node@22/node@24 are keg-only (Homebrew's own docs advise against
# `brew link --force` for keg-only formulae), so they are NOT symlinked
# into /opt/homebrew/bin. Reference the keg's own opt path directly instead.
# ---------------------------------------------------------------------------
echo "==> Installing node@${NODE_MAJOR} via Homebrew"
brew install "node@${NODE_MAJOR}"

NODE_KEG_BIN="/opt/homebrew/opt/node@${NODE_MAJOR}/bin"
export PATH="${NODE_KEG_BIN}:${PATH}"

# ---------------------------------------------------------------------------
# 4. Verify install -- fail loud (ADR-0004) if either command is broken.
# ---------------------------------------------------------------------------
echo "==> Verifying node/npm"
if ! NODE_VERSION_OUT=$("${NODE_KEG_BIN}/node" --version 2>&1); then
  echo "FATAL: node --version failed: ${NODE_VERSION_OUT}" >&2
  exit 1
fi
if ! NPM_VERSION_OUT=$("${NODE_KEG_BIN}/npm" --version 2>&1); then
  echo "FATAL: npm --version failed: ${NPM_VERSION_OUT}" >&2
  exit 1
fi
echo "    node ${NODE_VERSION_OUT}, npm ${NPM_VERSION_OUT}"

# ---------------------------------------------------------------------------
# 5. Bake the Reporter into place (contractual path, matches linux.Dockerfile
#    -- see CONTEXT.md). Packer's `file` provisioner already uploaded it to
#    /tmp/reporter.mjs (a world-writable path); move it into /opt/gsd-test
#    and fix ownership here, since /opt is root-owned by default.
# ---------------------------------------------------------------------------
echo "==> Installing Reporter to /opt/gsd-test/reporter.mjs"
sudo mkdir -p /opt/gsd-test
sudo mv /tmp/reporter.mjs /opt/gsd-test/reporter.mjs
sudo chown admin:staff /opt/gsd-test/reporter.mjs
sudo chmod 0644 /opt/gsd-test/reporter.mjs

# ---------------------------------------------------------------------------
# 6. Version sentinel (ADR-0030 Decision 3): Tart has no confirmed
#    label-read-back mechanism, so the sentinel is realized as a plain-text
#    in-guest file at a known path instead of an OCI label. (The registry
#    push side, dockerfiles/macos-tart.pkr.hcl's caller in
#    publish-tester-images.yml, ALSO sets --label sh.gsd-test.image-version
#    for parity with the Docker images and to future-proof for if a read
#    mechanism is found later -- this file is the one actually consumed by
#    the Local Engine's future Tart-aware CheckImageVersion leg.)
# ---------------------------------------------------------------------------
echo "==> Writing version sentinel: ${IMAGE_VERSION}"
echo -n "${IMAGE_VERSION}" | sudo tee /opt/gsd-test/image-version >/dev/null
sudo chown admin:staff /opt/gsd-test/image-version
sudo chmod 0644 /opt/gsd-test/image-version

# ---------------------------------------------------------------------------
# 7. NOTE: this script intentionally leaves the Cirrus Labs base image's
#    default admin/admin guest SSH credentials unchanged. Real production
#    credential rotation for baked Tart images is a known, explicitly
#    flagged follow-up (see ADR-0030) -- not solved here.
# ---------------------------------------------------------------------------

echo "==> Provisioning complete: node${NODE_MAJOR} / ${IMAGE_VERSION}"
