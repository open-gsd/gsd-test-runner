# Architecture Decision Records

This directory holds ADRs — short markdown notes recording load-bearing design decisions and *why* they were made. The goal is so future maintainers (human or AI) skip relitigating settled ground.

## When to write one

Write an ADR when a decision:
- Has a load-bearing reason a future explorer needs to know.
- Would otherwise be re-suggested by routine architecture review.
- Has a non-obvious tradeoff that took real thought to resolve.

Skip ADRs for ephemeral choices ("we'll do X for now") and self-evident ones.

## Naming

`NNNN-kebab-case-title.md`, zero-padded to 4 digits. Example: `0001-heredoc-installer-as-source-of-truth.md`.

## Format

Each ADR has four sections: **Context** (what forced the decision), **Decision** (what was chosen), **Consequences** (what this commits us to, good and bad), and **Status** (Accepted / Superseded by NNNN / Deprecated).
