# gsd-test

Run your Node test suite across Linux, Windows, and macOS in parallel — on hardware you already own — before pushing.

`gsd-test` is a local-dev harness, not a CI system. It runs on your Dev Workstation and ships work over SSH to remote Linux and Windows machines (Benches) you control. It catches platform-specific bugs — case-sensitive filesystems, missing system tools, different home directories, path-separator divergence — in your edit loop, while the diff is still hot.

## Features

- **Cross-platform parity** with zero CI lag. Push when you know it passes everywhere.
- **No shared infrastructure** — your laptop orchestrates; your own remote machines execute.
- **Fail-loud diagnostics** — every leg of the pipeline reports a distinct exit code with a diagnostics path.
- **Versioned Tester Images** published to GHCR. Sentinel labels catch silent stale-image drift.
- **Live progress** — see individual test events as they run, not just at the end.
- **Machine-readable output** via `--json-events` for CI integration or your own tooling.

## Quick Start

```bash
# 1. Install (macOS arm64 example — see docs/installation.md for all platforms)
curl -L -o gsd-test \
  https://github.com/open-gsd/gsd-test-runner/releases/latest/download/gsd-test-v1.0.0-darwin-arm64
chmod +x gsd-test && mv gsd-test ~/.local/bin/
gsd-test --version   # → v1.0.0

# 2. Configure a Bench (a remote machine you SSH to with Docker installed)
mkdir -p ~/.config/gsd-test
cat > ~/.config/gsd-test/config.toml <<'EOF'
[defaults]
targets = ["linux"]

[[benches]]
name = "lab-rig-1"
host = "lab-rig-1.local"
os = "linux"

[versions]
linux = "v1.0.0"
EOF

# 3. Run your tests
cd ~/my-node-project
gsd-test
```

## Documentation

- **[Release Notes](docs/release-notes.md)** — What changed in recent releases and how to adopt it
- **[Installation](docs/installation.md)** — Install the binary on macOS, Linux, or Windows
- **[Getting Started](docs/getting-started.md)** — Your first end-to-end test run
- **[Setting up Benches](docs/benches.md)** — Configure your Linux, Windows, and macOS hardware
- **[Configuration Reference](docs/configuration.md)** — Every `config.toml` field explained
- **[Troubleshooting](docs/troubleshooting.md)** — When things go wrong
- **[Architecture](docs/architecture.md)** — How `gsd-test` is built (for contributors)

## How it Works (30-second version)

1. You run `gsd-test` from inside your Node project's git repo.
2. It loads `~/.config/gsd-test/config.toml` — your list of remote machines (Benches), one per target OS.
3. It constructs a PR-merged worktree (base branch merged with your current changes) in a scratch directory.
4. For each target OS, it ensures the Tester Image is present on the corresponding Bench, then spawns a container, copies your worktree in, and runs `npm ci` + `npm run build` + `node --test`.
5. Per-OS results stream back as live events; a final per-OS pass/fail summary prints at the end.

**Exit codes:** `0` all platforms pass · `1` at least one platform failed · `2` infrastructure problem (see [Troubleshooting](docs/troubleshooting.md))

## License

MIT. See [LICENSE](LICENSE) if present, or check the repository root.

## Contributing

PRs welcome. See [docs/architecture.md](docs/architecture.md) for the design context and ADRs in [docs/adr/](docs/adr/).
