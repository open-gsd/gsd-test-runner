# Setting up Benches

## What is a Bench?

A Bench is a remote machine your Dev Workstation hands containerized test runs off to. It is SSH-reachable, has Docker (or a compatible container runtime) installed, and holds one or more pulled Tester Images. One Bench per target OS family (Linux Bench, Windows Bench, future macOS Bench). Typically this is your own hardware — a desktop, a home-lab box, a spare workstation — not shared infrastructure or a CI fleet.

The name comes from the engineering "test bench": an isolated environment for running experiments on the unit under test.

## Requirements

All Benches require:

- **SSH key-based access** from your Dev Workstation (no password prompt). `gsd-test` uses `DOCKER_HOST=ssh://<host>` to connect; this requires a working SSH key in your agent or `~/.ssh/`.
- **Docker Engine** (Linux) or **Docker Desktop** (Windows/macOS) installed and running.
- The Tester Image pulled from GHCR (or built locally as a fallback). See the setup steps below.

## Setting up a Linux Bench

### 1. Set up SSH key access

Copy your public key to the Bench:

```bash
ssh-copy-id user@lab-rig-1.local
```

Add an alias to `~/.ssh/config` so you can use a short name:

```text
Host lab-rig-1
  HostName lab-rig-1.local
  User youruser
  IdentityFile ~/.ssh/id_ed25519
  ControlMaster auto
  ControlPath ~/.ssh/cm-%r@%h:%p
  ControlPersist 10m
```

Test the connection:

```bash
ssh lab-rig-1 echo ok
```

Expected output: `ok`

### 2. Install Docker

Follow the [official Docker Engine installation guide](https://docs.docker.com/engine/install/) for your Linux distribution. Make sure the Docker daemon is running:

```bash
ssh lab-rig-1 docker version
```

Expected output: a Docker version block with `Server:` and `Client:` sections.

### 3. Authenticate with GHCR on the Bench

The Bench must be logged in to GHCR to pull Tester Images. Run this once per Bench:

```bash
ssh lab-rig-1 docker login ghcr.io
```

Enter your GitHub username and a personal access token with `read:packages` scope.

If your Tester Images are public (no org-level package visibility restriction), you can skip this step — unauthenticated pulls work for public images.

### 4. Pull the Tester Image

```bash
ssh lab-rig-1 docker pull ghcr.io/open-gsd/gsd-tester-linux:v1.0.0
```

Replace `v1.0.0` with the version in your `config.toml`'s `[versions]` table.

`gsd-test` also pulls automatically during the EnsureImages phase if the image is absent. The manual pull here is optional but confirms your auth is working.

### 5. Verify

From your Dev Workstation:

```bash
gsd-test --probe-benches
```

A Bench that is reachable and has Docker running appears in the reachable list. One that fails the probe appears in the unreachable list with the cause.

## Setting up a Windows Bench

### 1. Set up SSH key access

Install OpenSSH Server on the Windows Bench. In PowerShell (as Administrator):

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service -Name sshd -StartupType Automatic
```

Copy your public key to `C:\Users\<user>\.ssh\authorized_keys` on the Bench.

Add an alias to `~/.ssh/config` on your Dev Workstation:

```text
Host win-rig-1
  HostName win-rig-1.local
  User yourwinuser
  IdentityFile ~/.ssh/id_ed25519
```

Test:

```bash
ssh win-rig-1 echo ok
```

### 2. Install Docker on Windows

Install [Docker Desktop for Windows](https://docs.docker.com/desktop/install/windows-install/) and switch it to **Windows containers** mode. Alternatively, install Docker Engine for Windows Server.

Verify from your Dev Workstation:

```bash
ssh win-rig-1 docker version
```

### 3. Authenticate with GHCR on the Windows Bench

In a PowerShell session on the Windows Bench:

```powershell
docker login ghcr.io
```

### 4. Pull the Tester Image

```bash
ssh win-rig-1 docker pull ghcr.io/open-gsd/gsd-tester-windows:v1.0.0
```

## Setting up a macOS Bench

A macOS Bench is a Mac with Docker installed — either Docker Desktop or colima. The Tester Image is a Linux container (provides isolation from your Mac's local filesystem during npm ci / build / test runs); the Mac provides the host environment.

> **Why isolation matters**: `npm ci` populates a node_modules directory; tests can write to disk; npm caches accumulate. Running these directly on your Mac clobbers your local state. The Linux container keeps the test environment ephemeral and reproducible — your Mac stays clean.
>
> **Why not native macOS containers?** Apple Containers (macOS-native sandboxing) requires macOS 26 and isn't yet available on GitHub Actions runners or most developer Macs. When it ships broadly, the `Runtime` field on `bench.Bench` is reserved to switch to it. See [ADR-0020](adr/0020-macos-bench-via-apple-containers.md).

### Install Docker on your Mac

Either:
- **Docker Desktop** — https://www.docker.com/products/docker-desktop (GUI; commercial license for organizations >250 employees or >$10M revenue)
- **colima** — `brew install colima docker docker-compose` then `colima start` (open source; CLI-only)

Verify with `docker version`.

### Configure your Mac as a Bench

In `~/.config/gsd-test/config.toml`:

```toml
[[benches]]
name = "my-mac"
host = "local"  # or "" -- empty/local means run Docker on the current machine, no SSH
os = "macos"
# runtime defaults to "docker" -- leave unset
```

Or for a remote Mac you SSH to:

```toml
[[benches]]
name = "lab-mac-1"
host = "lab-mac-1.local"  # SSH alias from ~/.ssh/config
os = "macos"
```

### What gets pulled

The Local Engine pulls `ghcr.io/open-gsd/gsd-tester-macos:vX.Y.Z` — which is an alias for the Linux Tester Image (same content, different tag). Pull happens automatically per ADR-0012.

### Caveats

- Tests run inside a Linux container; macOS-specific code paths (FSEvents, case-insensitive HFS+, macOS-only Node APIs) are not exercised by this configuration
- For testing actual macOS behavior natively, run `node --test` directly outside the harness, or wait for Apple Containers (see [ADR-0020](adr/0020-macos-bench-via-apple-containers.md))

## Listing your Benches in config.toml

Each `[[benches]]` block declares one Bench. The double-bracket syntax is TOML's table-array — each block adds an entry to the list.

```toml
[[benches]]
name = "lab-rig-1"
host = "lab-rig-1"   # matches your ~/.ssh/config Host alias
os   = "linux"

[[benches]]
name = "win-rig-1"
host = "win-rig-1"
os   = "windows"
```

See [Configuration Reference](configuration.md) for all available fields.

## Multiple Benches per OS

You can list more than one Bench for the same OS. When exactly one Bench is eligible for an OS, `gsd-test` uses it directly. When more than one Bench is eligible for the same OS, selection is **capacity-aware pull**, not plain round-robin: candidate Benches pull work from a shared per-OS queue, so a higher-capacity or faster Bench naturally drains more of the queue without any explicit load tracking. See [ADR-0025](adr/0025-capacity-aware-fanout-scheduler.md) for the full design; ADR-0016 still governs which Benches are *eligible* (Pin/Exclude filtering) for an OS in the first place.

```toml
[[benches]]
name = "linux-bench-a"
host = "bench-a.local"
os   = "linux"

[[benches]]
name = "linux-bench-b"
host = "bench-b.local"
os   = "linux"
```

To always use a specific Bench, pass **`--bench <name>`** on the command line or set `defaults.pin` in `config.toml`. To exclude a Bench from selection, pass **`--exclude <name>`**.

## Node.js version matrix and fan-out

Every run tests each target OS against one or more Node.js major versions — an **(OS × Node) cell**. `linux` with `["22", "24"]` configured produces two cells, `linux-node22` and `linux-node24`, each running its own Tester container.

Which majors run on which OS is controlled by the `[node]` table in `config.toml` (see [Configuration Reference](configuration.md#node)); if an OS is absent from `[node]`, `gsd-test` falls back to the currently-supported Node LTS majors. Pass **`--node <majors>`** on the command line to override the Node set for every target OS for that invocation, ignoring both `[node]` and the LTS default.

Cells are dispatched, not statically assigned: all (OS × Node) cells for a given OS are pulled from a shared queue by whichever eligible Benches for that OS have free capacity, so cells spread across all matching Benches rather than being pinned to one Bench per cell. A Bench's `capacity` (see the `[[benches]]` reference below) bounds how many containers it runs concurrently; leaving `capacity` unset lets `gsd-test` probe the Bench's own CPU count and use that as the default, so a capable multi-core Bench runs several containers side by side automatically. Set `capacity` explicitly on a Bench that also does other work, to avoid oversubscribing it.

Each Tester Image is published per (OS × Node major) — see the tag-suffix note in the mapping table below and [ADR-0024](adr/0024-node-matrix-tester-images.md) for the full design.

## OS-to-Image mapping

| `os` value | Image | Runtime | Notes |
|------------|-------|---------|-------|
| `linux` | `ghcr.io/open-gsd/gsd-tester-linux` | `docker` | non-default Node majors are published with a `-node<major>` tag suffix (e.g. `:v1.5.0-node22`); the Active LTS major also gets the plain `:<version>` tag |
| `windows` | `ghcr.io/open-gsd/gsd-tester-windows` | `docker` | requires Windows host; same `-node<major>` tag suffix convention |
| `macos` | `ghcr.io/open-gsd/gsd-tester-macos` (alias of linux) | `docker` | Mac host running Docker Desktop or colima; tests Linux behavior; same `-node<major>` tag suffix convention |

## Probing Bench reachability

By default, `gsd-test` trusts your config and starts pipelines without checking whether each Bench is reachable. Run `--probe-benches` to probe first:

```bash
gsd-test --probe-benches
```

Unreachable Benches are removed from the run and listed with the cause. Pipelines start only for reachable Benches. This is useful for diagnosing connectivity problems before a long run.
