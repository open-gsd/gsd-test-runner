# Installation

`gsd-test` ships as a single static binary with no runtime dependencies. Download it, make it executable, and put it on your `PATH`.

## Prerequisites

- A Unix-like shell (bash, zsh, fish) on macOS or Linux, or PowerShell on Windows.
- SSH key-based access to at least one Bench (a remote machine with Docker installed). See [Setting up Benches](benches.md).

## Download from GitHub Releases

Releases publish on every `v*.*.*` tag (per [ADR-0019](adr/0019-local-engine-binary-distribution.md)). Download the binary for your platform, verify it, and move it to a directory on your `PATH`.

Binaries are named `gsd-test-v<version>-<os>-<arch>` (plus `.exe` for Windows). The commands below pin the version once in a single variable so they run as-is on copy-paste; bump it to the release you want:

```bash
GSD_TEST_VERSION=v1.6.0
```

> Use the **versioned** download path — `releases/download/<TAG>/…` — not `releases/latest/download/…`. The `latest` redirect resolves to whatever tag is newest, which only matches a version-pinned filename until the next release ships (the cause of [issue #55](https://github.com/open-gsd/gsd-test-runner/issues/55)).

Each binary ships with a `.sha256` sidecar for integrity verification. The sidecar lists the **published asset name** (e.g. `gsd-test-v1.6.0-darwin-arm64`), so after downloading the binary as `./gsd-test` (see your platform below) pair that hash with the local file:

```bash
# darwin-arm64 shown — swap the -<os>-<arch> suffix for your platform.
curl -L -o gsd-test.sha256 \
  "https://github.com/open-gsd/gsd-test-runner/releases/download/${GSD_TEST_VERSION}/gsd-test-${GSD_TEST_VERSION}-darwin-arm64.sha256"
echo "$(awk '{print $1}' gsd-test.sha256)  gsd-test" | shasum -a 256 -c -   # → gsd-test: OK
```

### macOS (Apple Silicon / arm64)

```bash
curl -L -o gsd-test \
  "https://github.com/open-gsd/gsd-test-runner/releases/download/${GSD_TEST_VERSION}/gsd-test-${GSD_TEST_VERSION}-darwin-arm64"
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

Expected output (matching `$GSD_TEST_VERSION`):

```text
v1.6.0
```

### macOS (Intel / amd64)

```bash
curl -L -o gsd-test \
  "https://github.com/open-gsd/gsd-test-runner/releases/download/${GSD_TEST_VERSION}/gsd-test-${GSD_TEST_VERSION}-darwin-amd64"
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

### Linux (amd64)

```bash
curl -L -o gsd-test \
  "https://github.com/open-gsd/gsd-test-runner/releases/download/${GSD_TEST_VERSION}/gsd-test-${GSD_TEST_VERSION}-linux-amd64"
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

### Linux (arm64)

```bash
curl -L -o gsd-test \
  "https://github.com/open-gsd/gsd-test-runner/releases/download/${GSD_TEST_VERSION}/gsd-test-${GSD_TEST_VERSION}-linux-arm64"
chmod +x gsd-test
mv gsd-test ~/.local/bin/
gsd-test --version
```

### Windows (amd64)

In PowerShell:

```powershell
$Version = "v1.6.0"
Invoke-WebRequest `
  -Uri "https://github.com/open-gsd/gsd-test-runner/releases/download/$Version/gsd-test-$Version-windows-amd64.exe" `
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

## For release maintainers

When cutting a new release, bump the version token in every place the docs pin it, so the install story never drifts ahead of (or behind) the published assets — the regression in [issue #55](https://github.com/open-gsd/gsd-test-runner/issues/55):

- `README.md` — Quick Start `GSD_TEST_VERSION`, the expected `--version` comment, and the `[versions]` example.
- `docs/installation.md` — the `GSD_TEST_VERSION` / `$Version` declarations and the expected-output block.
- `docs/getting-started.md` — the `[versions]` config examples and any expected `--version` output.

The download URLs themselves are tag-templated (`releases/download/${GSD_TEST_VERSION}/…`), so only the version token changes. `internal/docscheck` guards against the `releases/latest/download/…` + pinned-filename anti-pattern returning.
