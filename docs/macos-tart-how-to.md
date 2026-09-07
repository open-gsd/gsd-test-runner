# macOS via Tart How-to Guides

Task-focused recipes for setting up and operating a `RuntimeTart` Bench. For the concepts see [macOS via Tart](macos-tart.md); for exact fields, error types, and limitations see the [reference](macos-tart-reference.md).

## How to install Tart on a Bench

Tart requires **Apple Silicon** and **macOS 26 or later** on the Bench (Cirrus Labs' base images ship the Tart Guest Agent needed for a booted guest to be reachable, and this is the macOS line ADR-0030 confirmed working against). Install Tart itself via Homebrew:

```bash
ssh my-mac-bench brew install cirruslabs/cli/tart
tart --version
```

Then pull (or build — see [How to build and publish your own Tester Image](#how-to-build-and-publish-your-own-tester-image)) the macOS Tart Tester Image on that Bench:

```bash
ssh my-mac-bench tart pull ghcr.io/open-gsd/gsd-tester-macos-tart:v1.9.0-node22
```

`gsd-test` also pulls automatically when the image is absent (see [`EnsurePresentTart`](macos-tart-reference.md#known-limitations) — note there is no local-build fallback if the pull fails).

## How to configure a Bench to use `runtime = "tart"`

Add a `[[benches]]` entry with `os = "macos"` and `runtime = "tart"`:

```toml
[[benches]]
name = "mac-tart-1"
host = "my-mac-bench"   # SSH alias from ~/.ssh/config, or "local"
os   = "macos"
runtime = "tart"
```

Everything else — `[versions]`, `[node]`, `capacity`, `platform` — works the same as any other Bench entry. See the [Configuration Reference](configuration.md#benches) for the full `[[benches]]` field table.

## How to build and publish your own Tester Image

The bake recipe is a Packer template, `dockerfiles/macos-tart.pkr.hcl`, plus its provisioning script, `dockerfiles/macos-tart-provision.sh`. Both are documented at a reference level in the [reference doc](macos-tart-reference.md#the-tester-image-bake-recipe).

**Via CI** — push a version tag (or run the workflow manually) and let `.github/workflows/publish-tester-images.yml`'s `publish-macos-tart` job build and publish for you. That job requires a **self-hosted runner** labeled `[self-hosted, macos, tart]` — it does not run on GitHub-hosted `macos-*` runners, because whether those expose the nested-virtualization CPU features Tart's Virtualization.framework usage needs is unconfirmed. If you want CI-built images, register your own Mac as a self-hosted runner with that label set.

**No runner-side credential setup needed.** The CI job authenticates to GHCR via `TART_REGISTRY_USERNAME`/`TART_REGISTRY_PASSWORD` environment variables (`github.actor`/`GITHUB_TOKEN`, already available to every workflow run) rather than `tart login` — `tart pull`'s own `--help` documents this as a supported auth method alongside Keychain and Docker credential helpers, and `push` shares the same auth resolution. This deliberately avoids Keychain entirely: an earlier version of this setup used `tart login` (which writes into Keychain) plus a dedicated CI-only keychain kept unlocked as the session default, but that caused an unwanted macOS system prompt (Spotlight and other services asking for that keychain's password) on every reboot or login — not worth it when the env-var path works headlessly with no session dependency at all.

**Locally** — run `packer build` yourself from the repo root (not from `dockerfiles/`, so the `file` provisioner's relative path to `reporter/reporter.mjs` resolves):

```bash
packer init dockerfiles/macos-tart.pkr.hcl
packer build \
  -var node_version=22 \
  -var image_version=v1.9.0 \
  dockerfiles/macos-tart.pkr.hcl
```

This clones the Cirrus Labs base image, installs Xcode Command Line Tools and Homebrew headlessly, installs `node@22` via Homebrew, bakes the Reporter into `/opt/gsd-test/reporter.mjs`, and writes the version sentinel to `/opt/gsd-test/image-version`. Push the resulting local VM to your registry with `tart push`:

```bash
tart push gsd-tester-macos-tart-node22 \
  ghcr.io/open-gsd/gsd-tester-macos-tart:v1.9.0-node22 \
  --label sh.gsd-test.image-version=v1.9.0 \
  --label sh.gsd-test.node-major=22
```

## How to diagnose a stuck or failed run

A `RuntimeTart` run can fail with one of a few typed errors. Each names the leg it came from:

- **`VMStartError`** — the `StartContainer` leg failed at one of four sub-steps (`clone`, `set_memory`, `boot`, or `wait_ip`). A `wait_ip` failure with empty output means the guest booted but never became SSH-reachable within the timeout — check the Bench has network access and the base image's Guest Agent is intact.
- **`SentinelReadError`** — the guest booted, but reading the in-guest version-sentinel file (`/opt/gsd-test/image-version`) over SSH failed. This is a connectivity/guest problem, distinct from a version *mismatch* (which surfaces as the same `images.ImageVersionMismatch` Docker Benches use).
- **`GuestExecError`** — a command run inside the guest (`npm ci`, `npm run build`, or the test runner itself) failed. Its `Stdout`/`Stderr` carry the guest's captured output — read those first.
- **`DrainError`** — pulling the test-results JSONL file back from the Bench to your workstation failed, either creating the local temp file (`Stage: "create_temp"`) or the `scp` transfer itself (`Stage: "scp"`).

Because there is no live event streaming during `RunTests` (see the [reference](macos-tart-reference.md#known-limitations)), a run that looks stuck mid-test-suite is not necessarily wedged — it may simply not report anything until the whole suite finishes. Give it time before assuming a hang; if you need to confirm the guest is actually alive, SSH to the Bench and check `tart list` for the VM's `State`.

## How to clean up a leaked VM by hand

`RuntimeTart` has no Tier-2-reaper equivalent yet (see the [reference](macos-tart-reference.md#known-limitations)) — if a run crashes before its cleanup step runs (process killed, Bench rebooted), the VM it started is left running on the Bench. Find and remove it manually, from the Bench itself or over SSH:

```bash
ssh my-mac-bench tart list
```

Look for a VM named `gsd-tart-<cell>-<runID>` (or `gsd-tart-<runID>` if no cell was set) that's still `running` from a run you know finished or failed. Stop and delete it:

```bash
ssh my-mac-bench tart stop gsd-tart-macos-node22-abc12345
ssh my-mac-bench tart delete gsd-tart-macos-node22-abc12345
```

A leaked VM holds memory, CPU, and a disk clone on the Bench until removed — it does not go away on its own the way a Docker container's `--rm` would.
