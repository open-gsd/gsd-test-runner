# Node matrix how-to guides

Task recipes for testing across Node.js versions and tuning how cells fan out
across your Benches. For the concepts behind these tasks see
[The Node version matrix](node-matrix.md); for exact key names, defaults, and the
image tag scheme see the [Node matrix reference](node-matrix-reference.md).

## Test your project against several Node versions

Declare the Node majors you want per OS in a `[node]` table in your `config.toml`:

```toml
[node]
linux   = ["22", "24"]
windows = ["22"]
```

Then run `gsd-test` as usual. Every target OS is now tested against each of its
declared majors — Linux runs `linux-node22` and `linux-node24`; Windows runs
`windows-node22`. You do not pass any extra flag; the matrix comes from config.

If you omit `[node]` entirely, `gsd-test` uses the currently-supported Node LTS
lines, so you get multi-version coverage by default. Add `[node]` only when you
want a set different from the supported-LTS default.

## Test against one Node version for a single run

To narrow a single run to specific majors without editing config — for a quick
reproduction, say — pass `--node`:

```bash
gsd-test --node 24          # Node 24 only, on every target OS this run
gsd-test --node 22,24       # Node 22 and 24 only, this run
```

`--node` overrides the `[node]` config for that run and applies to **every**
target OS. It does not persist; the next plain `gsd-test` goes back to your
config.

> If you pass a major whose image was never published (and cannot be built
> locally), that cell fails loudly at image-ensure time rather than silently
> skipping — the failure names the missing image. Pass only majors you publish.

## Set how many cells a Bench runs at once

Each Bench runs several cells concurrently up to its **capacity**. To bound a
Bench explicitly, set `capacity` on its `[[benches]]` entry:

```toml
[[benches]]
name = "lab-rig-1"
host = "lab-rig-1"
os   = "linux"
capacity = 6        # at most 6 cells (containers) at once on this Bench
```

Choose a value by what else the Bench does:

- **A dedicated Bench** (nothing else runs on it): leave `capacity` unset.
  `gsd-test` uses the Bench's own CPU count, so it self-tunes to the hardware.
- **A shared Bench** (your daytime workstation, a box running other services):
  set `capacity` to a fraction of its cores so a wide matrix does not starve
  everything else.
- **Force serial** (debugging a flaky interaction, or a memory-tight Bench):
  set `capacity = 1`.

Capacity is per Bench, so a fast rig and a slow rig can carry different values;
the pull-based scheduler already sends more work to whichever drains its queue
faster.

## Spread a wide matrix across more Benches

Fan-out spreads cells across every Bench eligible for an OS. To widen it, add more
Benches for that OS — the scheduler needs no further configuration:

```toml
[[benches]]
name = "linux-a"
host = "bench-a.local"
os   = "linux"

[[benches]]
name = "linux-b"
host = "bench-b.local"
os   = "linux"
```

All Linux cells now pull from a shared queue that both `linux-a` and `linux-b`
drain in parallel, each up to its own capacity. To keep a run on one specific
Bench, use `--bench <name>`; to remove a Bench from a run, use `--exclude <name>`
(unchanged from before — these gate *eligibility*, the scheduler does the rest).

## Read per-cell results

Each `(OS, Node)` cell reports separately. To see which Node version a failure
came from:

- **Live**, read the stream prefix: `[linux-node24] ✗ FAIL …` is a Node 24
  failure on Linux.
- **From the verdict** (the last line of stdout), read the `per_os` map — it is
  keyed per cell:

  ```bash
  gsd-test --json-events | tail -1 | jq '.per_os'
  # { "linux-node22": {"passed":12,…}, "linux-node24": {"passed":11,"failed":1,…} }
  ```

- **From CI**, point your report viewer at the saved `junit.xml`; each cell is its
  own `<testsuite>` named `linux-node24`, so a dashboard shows per-version rows.
- **The raw events** for one cell are in `test-events-<cell>.jsonl` in the run's
  artifact directory (the verdict's `artifacts.dir`).

The run's exit code is the roll-up: `0` only if every cell passed, `1` if any cell
had a test failure, `2` for an infrastructure problem in any cell.

## Add or drop a supported Node LTS line

When the [Node release schedule](https://github.com/nodejs/Release) moves — a line
reaches end-of-life, or a new one enters LTS — update the set in the two places
that hold it:

1. **The published-images list** in `.github/workflows/publish-tester-images.yml`
   — the `strategy.matrix.node` array (and `DEFAULT_NODE_MAJOR` if the Active LTS
   changed). This controls which `-node<major>` images CI publishes.
2. **The default set** in `config.DefaultNodeLTS()` (`internal/config/config.go`)
   — this is what a project without a `[node]` table gets.

Keep the two in sync. A project that pins its own `[node]` table is unaffected by
the default; it changes its own list when it is ready.

## Keep a project on one Node version

If you deliberately want single-version behaviour — you support exactly one Node
line — pin it in config so the intent is explicit and the run is fast:

```toml
[node]
linux = ["22"]
```

Every Linux run is now just `linux-node22`, and its `per_os` key collapses to
plain `linux` (a single-Node report is not suffixed), so any tooling that read the
old un-suffixed key keeps working.
