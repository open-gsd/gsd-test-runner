---
description: Run the GSD test suite on Mac AND in Docker, then compare results.
allowed-tools: Bash(gsd-test-both:*), Bash(gsd-test:*), Bash(gsd-test-local:*), Bash(jq:*), Bash(cat:*), Bash(head:*), Bash(tail:*)
---

Run the project's tests on both platforms (Mac local + Docker Linux) and
analyze the diff.

## Steps

1. Run `gsd-test-both`. It launches `gsd-test-local` and `gsd-test` in parallel,
   writes JSON Lines results to `/tmp/gsd-test-local.jsonl` and
   `/tmp/gsd-test-docker.jsonl`, then prints a comparison summary.

2. Read both JSON Lines files. Parse and categorize failures into:
   - **Both fail** (real bugs — same failure on both platforms)
   - **Mac-only fail** (passes on Docker — Mac-specific issue)
   - **Docker-only fail** (passes on Mac — Linux/container-specific issue)
   - **Missing on one platform** (test discovery differs)

3. Summarize for me:
   - Counts per category
   - For each failure type, list up to 10 with file + test name + first
     line of the error message

4. If anything is Mac-only or Docker-only, those are platform-specific —
   call those out as the most interesting findings. If something only fails
   in Docker, the container environment (different homedir, different fs
   case-sensitivity, etc.) probably matters. If it only fails on Mac,
   probably a macOS quirk.

5. If `gsd-test-both` exited non-zero, surface the stderr tails it printed.

## Useful jq one-liners

    # All failures on a specific platform:
    jq -c 'select(.type=="test:fail") | {file: .data.file, name: .data.name}' /tmp/gsd-test-docker.jsonl

    # Test names grouped by file (most-broken files first):
    jq -r 'select(.type=="test:fail") | .data.file' /tmp/gsd-test-docker.jsonl | sort | uniq -c | sort -rn
