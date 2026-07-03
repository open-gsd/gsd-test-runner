# Node matrix reference

Neutral reference for the Node.js version matrix and Bench fan-out. For the
concepts see [The Node version matrix](node-matrix.md); for task recipes see the
[how-to guides](node-matrix-how-to.md). Design is fixed in
[ADR-0024](adr/0024-node-matrix-tester-images.md) and
[ADR-0025](adr/0025-capacity-aware-fanout-scheduler.md).

## Terms

| Term | Definition |
|------|------------|
| **Cell** | One `(OS, Node major)` pair. The unit of scheduling, execution, and reporting. |
| **Node major** | A Node.js major version as a digits-only string, e.g. `"22"`. Never prefixed (`v22`) or a full version (`22.11.0`). |
| **Capacity** | The maximum number of cells a Bench runs concurrently. |
| **Eligible Bench** | A Bench whose `os` matches the cell's OS and that survives `--bench` / `--exclude` filtering. |

## `[node]` config table

A table mapping an OS to the list of Node majors tested for it.

```toml
[node]
linux   = ["22", "24"]
windows = ["22"]
```

| Property | Value |
|----------|-------|
| Type | Table of `string` → array of `string` |
| Keys | One of `"linux"`, `"windows"`, `"macos"` |
| Values | Arrays of digits-only major strings; no duplicates within one list |
| Default | When the table is absent, or an OS is absent from it, that OS uses `config.DefaultNodeLTS()` |
| Validation | An unknown OS key, a non-digit major, or a duplicate major within one OS list is a load-time error |

`config.DefaultNodeLTS()` returns the currently-supported Node LTS majors —
`["22", "24"]` as of v1.6.0. It is the fallback for any OS without an explicit
`[node]` entry.

## `capacity` Bench field

Set on a `[[benches]]` entry.

```toml
[[benches]]
name = "lab-rig-1"
host = "lab-rig-1"
os   = "linux"
capacity = 6
```

| Property | Value |
|----------|-------|
| Type | `int` |
| Default | `0` (unset) |
| Meaning of unset | Resolved to the Bench's own CPU count via `docker info -f '{{.NCPU}}'` against that Bench's daemon, probed once per run and cached, floored to `1` if the probe fails |
| Meaning when set | The exact maximum number of concurrent cells for this Bench |
| Validation | A negative value is a load-time error |

Capacity is enforced structurally: a Bench with capacity *N* contributes *N*
worker goroutines pulling from its OS's cell queue, so it runs at most *N*
containers at once.

## `--node` flag

```
--node <majors>
```

| Property | Value |
|----------|-------|
| Value | Comma-separated Node majors, e.g. `--node 22,24` |
| Scope | The current run only; not persisted |
| Precedence | Overrides the `[node]` config for every target OS when non-empty; empty (flag omitted) falls back to `[node]`, then to `DefaultNodeLTS()` |
| Applies to | Every target OS uniformly |

## Cell identity

A cell's stable key is derived from its OS and Node major:

| Node major | Key | Example |
|------------|-----|---------|
| non-empty | `<os>-node<major>` | `linux-node22` |
| empty (legacy single-Node report) | `<os>` | `linux` |

This key appears as:

- the live stream prefix — `[linux-node22]`
- the key in the verdict's `per_os` map
- the `name` of the `<testsuite>` in `junit.xml`
- the suffix of the per-cell event file — `test-events-linux-node22.jsonl`

## Image references

Tester Images are published per Node major with a node-suffixed tag.

| Element | Form | Example |
|---------|------|---------|
| Node-suffixed tag | `ghcr.io/open-gsd/gsd-tester-<os>:<version>-node<major>` | `…/gsd-tester-linux:v1.6.0-node22` |
| Plain tag (Active-LTS major only) | `ghcr.io/open-gsd/gsd-tester-<os>:<version>` and `:latest` | `…/gsd-tester-linux:v1.6.0` |
| Image-version sentinel label | `sh.gsd-test.image-version=<version>` (un-suffixed) | `v1.6.0` |
| Node-major sentinel label | `sh.gsd-test.node-major=<major>` | `22` |

The pipeline's `CheckImageVersion` leg verifies the un-suffixed
`sh.gsd-test.image-version` label; the node major rides in the tag and the
companion label and is not part of the verified version string.

## Scheduling

| Property | Behaviour |
|----------|-----------|
| Assignment | Dispatch-time, pull-based. All cells for an OS share one queue; every eligible Bench pulls from it. |
| Balancing | Least-loaded by construction — a faster/higher-capacity Bench returns for work sooner and takes more. No load metric is computed. |
| Eligibility | Governed by `os` match plus `--bench` / `--exclude` (ADR-0016 filters); unchanged from prior versions. |
| Image presence | Ensured inside each worker on its assigned Bench (pull, then local fallback build passing `--build-arg NODE_VERSION=<major>`), because Bench binding is dynamic. |

## Exit codes

Unchanged from ADR-0009; the roll-up is now across all cells.

| Code | Condition |
|------|-----------|
| `0` | Every cell passed |
| `1` | At least one cell had a failing test |
| `2` | At least one cell hit an infrastructure error, or any planning step produced a skip |

## CI publishing

`.github/workflows/publish-tester-images.yml` builds a `strategy.matrix.node`
across the published majors for Linux, Windows, and the macOS alias, tagging each
build `-node<major>`. `DEFAULT_NODE_MAJOR` is the Active-LTS major that
additionally receives the plain `:<version>` and `:latest` tags. Sentinel
verification checks both `sh.gsd-test.image-version` and `sh.gsd-test.node-major`
on every architecture.
