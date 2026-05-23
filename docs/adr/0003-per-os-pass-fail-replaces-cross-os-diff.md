# 0003 — Per-OS pass/fail report replaces cross-OS divergence diff

Status: Accepted (2026-05-22)

## Context

The transitional Diff (gsd-test-diff, ~258 lines of Python) compared 2–3 JSON Lines streams across platforms and reported divergences. This made sense for the original framing: "which platform-specific bugs are we catching?" The framing has changed. The goal is now a local CI-equivalent gate before push, which wants a different question answered: "does each target OS pass the suite?"

If Linux passes and Windows fails, the developer reads the Windows report. There is no value in a unified "X passed on Linux but failed on Windows" summary — the interesting information is already in the per-OS failure detail. The Diff's 2-way-vs-3-way mode bifurcation (~70 parallel lines of code for each path) serves the old framing, not the new one.

Path normalization — the Diff's most useful pure function — survives as a per-OS report formatter responsibility. Paths shown to the developer are normalized to their tree-relative form at report-emission time, not at comparison time.

## Decision

Each OS produces an independent Per-OS report — either `pass x/y` or a structured fail report listing failed tests with file, name, and captured output. The Local Engine emits N reports for N target OSes. There is no cross-OS comparison step. Path normalization survives as a per-OS report formatter responsibility; paths shown to the developer are normalized to their tree-relative form.

## Consequences

+ The Diff's 2-way-vs-3-way mode bifurcation goes away entirely (no more parallel ~70-line code paths).
+ Adding a new target OS (e.g., macOS Containers) requires no changes to comparison logic — just another Per-OS report.
+ Per-OS failure context is preserved verbatim instead of being reduced to "failed on platform X."
- A developer who wants to spot cross-OS divergences must compare reports by eye. Acceptable: implied differences are obvious when one OS fails and another passes.
- The Diff's categorization vocabulary (mac_pass_docker_fail, etc.) is retired.

## Alternatives considered

- Keep the cross-OS Diff alongside per-OS reports — Rejected: two report formats to maintain; users would have to pick which to read. Adds shallow modules with no extra leverage.
- Per-OS report plus a separate divergence summary tool — Rejected: same critique; if needed later it can be a downstream consumer of the structured per-OS reports.
