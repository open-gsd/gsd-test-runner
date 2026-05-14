# /test — run GSD tests on both platforms and diff

Run the project tests on both Mac (local) AND in Docker on a Linux host, in
parallel, and analyze any platform-specific differences.

## How

Execute this shell command:

    gsd-test-both

It runs `gsd-test-local` (Mac) and `gsd-test` (Docker) in parallel, captures
JSON Lines output from each to `/tmp/gsd-test-local.jsonl` and
`/tmp/gsd-test-docker.jsonl`, then prints a comparison.

## Then analyze

Read both files. Categorize failures:

- **Both fail** = real bugs, fix first
- **Mac fail only** = macOS-specific issue
- **Docker fail only** = Linux/container-specific issue (different homedir,
  case-sensitive fs, missing tools, etc.)

For each interesting failure, show file + test name + first line of the
error. Don't paste full stack traces.

## Useful one-liners

    jq -c 'select(.type=="test:fail") | {file: .data.file, name: .data.name}' /tmp/gsd-test-docker.jsonl
    jq -r 'select(.type=="test:fail") | .data.file' /tmp/gsd-test-docker.jsonl | sort | uniq -c | sort -rn
