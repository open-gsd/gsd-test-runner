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

## Setting up a macOS Bench (preview)

macOS Bench support targets macOS 26 and later using Apple Containers — Apple's native container runtime (distinct from Docker Desktop). Unlike Docker on Mac, which runs Linux containers inside a Linux VM, Apple Containers runs macOS-native containers natively. This is the only way to test actual macOS code paths in a sandboxed environment on macOS hardware.

This feature is in preview: the Tester Image base image is not yet published (Apple has not released official macOS base images), and GitHub Actions does not yet offer macOS 26 runners. The runtime abstraction is already wired — see [ADR-0020](adr/0020-macos-bench-via-apple-containers.md) for the full design.

When macOS Bench support ships, the setup flow will be:

1. SSH key access to the macOS Bench (same as Linux).
2. macOS 26 with Apple Containers installed (`container` binary).
3. Pull the Tester Image: `ssh mac-rig-1 container pull ghcr.io/open-gsd/gsd-tester-macos:v1.0.0`.
4. Set `runtime = "container"` in the `[[benches]]` entry.

```toml
[[benches]]
name    = "mac-rig-1"
host    = "mac-rig-1"
os      = "macos"
runtime = "container"
```

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

You can list more than one Bench for the same OS. `gsd-test` round-robins across them per invocation:

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

## OS-to-Image mapping

| `os` value | Tester Image | Runtime |
|------------|--------------|---------|
| `linux` | `ghcr.io/open-gsd/gsd-tester-linux` | `docker` |
| `windows` | `ghcr.io/open-gsd/gsd-tester-windows` | `docker` |
| `macos` | `ghcr.io/open-gsd/gsd-tester-macos` | `container` (Apple Containers, preview) |

## Probing Bench reachability

By default, `gsd-test` trusts your config and starts pipelines without checking whether each Bench is reachable. Run `--probe-benches` to probe first:

```bash
gsd-test --probe-benches
```

Unreachable Benches are removed from the run and listed with the cause. Pipelines start only for reachable Benches. This is useful for diagnosing connectivity problems before a long run.
