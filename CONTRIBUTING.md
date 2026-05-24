# Contributing to gsd-test-runner

This repository hosts the remote Dockerized test runner for [GSD](https://github.com/open-gsd/get-shit-done-redux). It is small in surface (a Dockerfile plus a handful of shell scripts) but participates in the same org-wide contribution model as the main repo.

If you have not read it, please skim [the GSD CONTRIBUTING guide](https://github.com/open-gsd/get-shit-done-redux/blob/main/CONTRIBUTING.md) — the issue/PR workflow described there applies here too.

---

## Getting Started

```bash
git clone https://github.com/open-gsd/gsd-test-runner.git
cd gsd-test-runner

# Requirements: Docker (Linux + Windows daemons reachable), rsync, bash, ssh.
# See the README for environment variables (SSH host, image tag, etc).

# Run the suite the same way CI does:
./gsd-test-both
```

There is no `npm install` step — this repo is shell + Docker.

---

## Types of Contributions

We accept three types. Each maps to an issue template and a PR template. **Read this before opening anything.**

### 🐛 Fix (Bug Report)

A fix corrects something that is broken, crashes, produces wrong output, or behaves contrary to documented behavior.

**Process:**
1. Open a [Bug Report issue](https://github.com/open-gsd/gsd-test-runner/issues/new?template=bug_report.yml) — fill it out completely.
2. Wait for a maintainer to confirm (`confirmed-bug` label). Obvious reproducible bugs are confirmed quickly.
3. Fix it. Add a regression test or repro script where feasible.
4. Open a PR using the [Fix template](.github/PULL_REQUEST_TEMPLATE/fix.md), linking the confirmed issue.

**Rejection reasons:** Not reproducible, works-as-designed, duplicate.

### ⚡ Enhancement

Improves an existing feature — faster execution, better output, expanded edge-case handling. Does **not** add new commands or new concepts.

**Process:**
1. Open an [Enhancement issue](https://github.com/open-gsd/gsd-test-runner/issues/new?template=enhancement.yml). Describe the current behavior, the proposed improvement, and the motivation.
2. Wait for the `approved-enhancement` label. PRs opened before approval are closed.
3. Open a PR using the [Enhancement template](.github/PULL_REQUEST_TEMPLATE/enhancement.md), linking the approved issue.

### ✨ Feature

Adds something that doesn't exist today — a new script, a new workflow, a new integration.

**Process:**
1. Open a [Feature Request issue](https://github.com/open-gsd/gsd-test-runner/issues/new?template=feature_request.yml). Include the problem, the proposed shape, and alternatives considered.
2. Wait for the `approved-feature` label. Discussion may take longer for features. PRs opened before approval are closed.
3. Open a PR using the [Feature template](.github/PULL_REQUEST_TEMPLATE/feature.md), linking the approved issue.

---

## Pull Request Ground Rules

- **No draft PRs.** Draft PRs are closed. Open the PR when the code is complete and tests pass.
- **Use the correct typed template.** The default (untyped) template is a rejection reason — it exists only to route you to the right one.
- **Link the approved issue.** For enhancements and features, the linked issue must carry the approval label.
- **Keep PRs focused.** One concern per PR. Refactors get their own PR.
- **Tests must pass locally** before push. `./gsd-test-both` is the canonical local check.
- **Atomic commits with clear messages.** Follow conventional-commit-style prefixes (`fix:`, `feat:`, `chore:`, `docs:`) where possible.

CI / tooling, dependency, and doc-only PRs are exempt from the typed-template requirement (the PR template detects this from the changed paths).

---

## Security

Do **not** report security vulnerabilities through public issues. See [SECURITY.md](SECURITY.md) for the disclosure process.

---

## Code of Conduct

We follow the same conduct standard as the main GSD repo. Be kind, be specific, assume good faith, and keep discussion focused on the work.
