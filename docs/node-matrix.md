# The Node version matrix

This document explains *why* `gsd-test` tests your project against more than one
Node.js version, what an `(OS, Node)` **cell** is, and how cells fan out across
your Benches. For the exact config keys, flags, and image tags see the
[Node matrix reference](node-matrix-reference.md); for task recipes see the
[Node matrix how-to guides](node-matrix-how-to.md); for the locked design
decisions read [ADR-0024](adr/0024-node-matrix-tester-images.md) and
[ADR-0025](adr/0025-capacity-aware-fanout-scheduler.md).

## The problem: one Node line is not enough

A test suite that passes on Node 22 can fail on Node 24. A minor release removes
a deprecated API; V8 changes an error message your snapshot pinned; the built-in
`node:test` runner changes how it reports a subtest. None of that shows up if you
only ever run one Node version — you find out in production, or when a contributor
on a different machine opens an issue you cannot reproduce.

The obvious fix — "run the suite again on the other version" — is exactly the kind
of manual, easy-to-skip step that rots. Before v1.6.0, `gsd-test` had a single
axis: OS. Each Tester Image baked in one Node version, and a run produced one
result per OS. Testing a second Node line meant rebuilding images and running
again by hand.

v1.6.0 makes Node a **first-class second axis**, alongside OS. You declare the
Node majors you care about once, and every `gsd-test` run covers all of them.

## Cells: the (OS × Node) grid

The unit of work is now a **cell**: one `(OS, Node major)` pair. If you target
Linux and Windows, and declare Node majors `22` and `24` for Linux and `22` for
Windows, a run has three cells:

```
linux-node22   linux-node24   windows-node22
```

Each cell is independent end to end. It runs in its own container built for that
Node major, on some Bench, and it produces its own result. That identity —
`linux-node22` — is what you see throughout a run: it is the prefix on the live
stream, the key in the verdict's `per_os` map, the name of the `<testsuite>` in
`junit.xml`, and the suffix on the per-cell event file (`test-events-linux-node24.jsonl`).
A single logical failure that only reproduces on Node 24 is now attributable to
exactly that cell, not smeared across "linux".

The run's **exit code is the roll-up of every cell**: any failing cell fails the
whole run, exactly the way any failing OS failed the whole run before. Green on
22 but red on 24 is a red run — which is the entire point.

## Fan-out: cells pull work from your Benches

More cells mean more containers to run. Running them one at a time would make the
matrix a wall-clock tax — the opposite of what you want. So cells are **fanned
out** across your Benches, and the scheduling is deliberately simple: cells are
*dispatched*, not pre-assigned.

For each OS, all of that OS's cells go into one shared queue. Every eligible Bench
for that OS pulls the next cell from the queue whenever it has a free slot. A
Bench that finishes a cell comes back for another; a Bench that is bigger or
faster simply comes back sooner and takes more. This is work-stealing, and it
gives you least-loaded balancing **for free** — there is no load estimate to
compute, no per-Bench share to configure, and a slow Bench can never become the
bottleneck that holds up a fast one.

This is a deliberate change from the pre-v1.6.0 model, where a Bench was chosen
for each OS at plan time by round-robin ([ADR-0016](adr/0016-bench-selector.md)).
Round-robin gives a 16-core Bench and a 4-core Bench the same share regardless of
their real speed. Pull-based dispatch does not: the binding of a cell to a Bench
happens at dispatch time, when a Bench actually asks for work. Pin and exclude
still decide which Benches are *eligible* for an OS; what changed is *when* — and
by what rule — a specific cell lands on a specific Bench. See
[ADR-0025](adr/0025-capacity-aware-fanout-scheduler.md) for the full rationale.

## Capacity: how many cells a Bench runs at once

Parallelism across Benches is only half of it. A single capable Bench should also
run several cells **at once** — that is what a 32-core home-lab box is for. Each
Bench has a **capacity**: the maximum number of cells it runs concurrently. Under
the hood a Bench with capacity *N* contributes *N* workers pulling from its OS's
queue, so it can never exceed *N* running containers, and it naturally drains more
of the queue than a lower-capacity Bench.

The default is chosen to make parallelism the path of least resistance. If you do
not set `capacity`, `gsd-test` uses the Bench's **own CPU count** — probed once
per run via `docker info` against that Bench's daemon, not your workstation's. So
a capable Bench runs many cells side by side with zero configuration. The
trade-off is intentional and worth stating plainly: if a Bench is also doing other
work, defaulting to its full core count can oversubscribe it — that is precisely
when you set `capacity` explicitly to bound it. See
[how to tune Bench capacity](node-matrix-how-to.md#set-how-many-cells-a-bench-runs-at-once).

## Which Node versions run

Absent any configuration, a run uses the **currently-supported Node LTS lines** —
`22` and `24` as of this release (Node 20 reached end-of-life on 2026-04-30; Node
26 enters LTS on 2026-10-28). This set is **data, not logic**: it lives in the
`[node]` config table and in `config.DefaultNodeLTS()`, and the CI image matrix
reads the same list. Moving with the [Node.js release schedule](https://github.com/nodejs/Release)
— dropping a line that reaches EOL, adding one that enters LTS — is a one-line
edit, never a code change. You override the set per project with `[node]`, or for
a single run with `--node`.

## How images encode the Node major

A Tester Image is now published per Node major, distinguished by a **tag suffix**:
`ghcr.io/open-gsd/gsd-tester-linux:v1.6.0-node22` and `…-node24` are the same
release built on different Node bases. The plain release version
(`sh.gsd-test.image-version`) is unchanged and still the sentinel the pipeline
verifies; the Node major rides in the tag and in a companion
`sh.gsd-test.node-major` label. A tag suffix was chosen over separate image
repositories (`gsd-tester-linux-node22`) so there is still one image name per OS
and the node dimension is one extra segment, not a combinatorial explosion of
names ([ADR-0024](adr/0024-node-matrix-tester-images.md)).

The Active-LTS major also keeps the un-suffixed `:v1.6.0` and `:latest` tags, so a
config that never opts into the matrix keeps resolving a working image with no
migration.

## Back-compat and trade-offs

- **Opt-in by construction.** With no `[node]` table and no `capacity` set, a run
  still uses the supported-LTS default and each Bench still runs serially unless
  its cores say otherwise — but a single-OS, single-Bench project sees the same
  shape of run it saw in v1.5.0. Nothing about the OS axis, the verdict schema, or
  the exit codes (`0`/`1`/`2`) changed.
- **`per_os` keys gained a suffix — only when they had to.** The digest/verdict
  key is now `linux-node22` when more than one Node major ran; for a legacy
  single-Node report it collapses back to plain `linux`, so existing tooling that
  reads `per_os["linux"]` keeps working until you widen the matrix.
- **A wider matrix costs more images and more containers.** Two Node majors is
  twice the images to pull (or build) and twice the containers to run. Fan-out
  spends that cost in parallel rather than in series, but it is not free — it is
  your Benches' cores. That is the trade you are opting into, and `capacity` is
  the dial that bounds it.
- **The tag and label scheme is a supported surface.** The `-node<major>` tag
  suffix and the `sh.gsd-test.node-major` label are pinned in
  [ADR-0024](adr/0024-node-matrix-tester-images.md); build tooling on top of them
  safely.
