# Installation

`gsd-test` ships as a single static binary with no runtime dependencies. Download it, make it executable, and put it on your `PATH`.

## Prerequisites

- A Unix-like shell (bash, zsh, fish) on macOS or Linux, or PowerShell on Windows.
- SSH key-based access to at least one Bench (a remote machine with Docker installed). See [Setting up Benches](benches.md).

## Download from GitHub Releases

Releases publish on every `v*.*.*` tag (per [ADR-0019](adr/0019-local-engine-binary-distribution.md)). Download the binary for your platform, verify it, and move it to a directory on your `PATH`.

Each binary ships with a `.sha256` sidecar file for integrity verification. To verify:

```bash
# Download sidecar
curl -L -o gsd-test.sha256 \
  https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.3.2-darwin-arm64.sha256
shasum -a 256 -c gsd-test.sha256
```

Binaries are named `gsd-test-v<version>-<os>-<arch>` (plus `.exe` for Windows). Replace `v1.3.2` in the commands below with the current release version.

### macOS (Apple Silicon / arm64)

```bash
curl -L -o gsd-test \
  https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.3.2-darwin-arm64
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

Expected output:

```text
v1.3.2
```

### macOS (Intel / amd64)

```bash
curl -L -o gsd-test \
  https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.3.2-darwin-amd64
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

### Linux (amd64)

```bash
curl -L -o gsd-test \
  https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.3.2-linux-amd64
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

### Linux (arm64)

```bash
curl -L -o gsd-test \
  https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.3.2-linux-arm64
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

### Windows (amd64)

In PowerShell:

```powershell
Invoke-WebRequest `
  -Uri https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.3.2-windows-amd64.exe `
  -OutFile gsd-test.exe
Move-Item gsd-test.exe "$env:LOCALAPPDATA\Microsoft\WindowsApps\gsd-test.exe"
gsd-test --version
```

If `$env:LOCALAPPDATA\Microsoft\WindowsApps` is not on your `PATH`, move the binary to any directory that is, or add the target directory to `$env:PATH`.

## Build from Source

You need Go 1.23 or later (the version declared in `go.mod`).

```bash
go install github.com/open-gsd/gsd-test-runner/cmd/gsd-test@latest
gsd-test --version
```

The binary lands in `$(go env GOPATH)/bin`. Make sure that directory is on your `PATH`.

## Verify the Installation

```bash
gsd-test --version
```

You should see a version string. Any other output (command not found, permission denied) means the binary is not on your `PATH` or not executable.

## Update

Re-run the download command for your platform with the new release tag, or run:

```bash
go install github.com/open-gsd/gsd-test-runner/cmd/gsd-test@latest
```

## Uninstall

```bash
rm ~/.local/bin/gsd-test       # macOS / Linux
# Windows: delete the .exe from wherever you placed it
```
