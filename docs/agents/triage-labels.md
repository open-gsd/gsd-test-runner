# Triage Labels

The skills speak in terms of **canonical triage roles** (five *state* roles + two
*category* roles). This file maps those roles to the actual label strings used in
this repo's GitHub Issues. **This per-repo file takes precedence over the global
`triage-labels.md` template.**

This repo uses the **canonical names as the actual label strings**. All five state
labels exist in the tracker.

## State roles

| Canonical role    | Label in this repo | Meaning                                  |
| ----------------- | ------------------ | ---------------------------------------- |
| `needs-triage`    | `needs-triage`     | Maintainer needs to evaluate this issue  |
| `needs-info`      | `needs-info`       | Waiting on reporter for more information |
| `ready-for-agent` | `ready-for-agent`  | Fully specified, ready for an AFK agent  |
| `ready-for-human` | `ready-for-human`  | Fully specified, requires human implementation |
| `wontfix`         | `wontfix`          | Will not be actioned                     |

## Category roles

| Canonical role | Label in this repo | Meaning |
| -------------- | ------------------ | ------- |
| `bug`          | `bug`              | Reported bug (not yet reproduced) |
| `enhancement`  | `enhancement`      | New feature / improvement |

## Org-convention companion labels (this repo also carries these)

These are **not** part of the five canonical state roles, but they exist in the
tracker as part of the `open-gsd` workflow vocabulary. Apply them in addition to
the canonical state label, not instead of it:

| Label                  | When to apply |
| ---------------------- | ------------- |
| `confirmed-bug`        | A bug whose reproduction has been **verified** during triage. Apply alongside `bug` + `ready-for-agent` so the issue is visible to `confirmed-bug`-scoped fix workflows. |
| `approved-enhancement` | An enhancement a maintainer has approved for implementation. |
| `approved-feature`     | A new feature a maintainer has approved for implementation. |
| `documentation`        | Category tag for docs-only issues (used alongside the canonical state label). |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the
corresponding label string from the table above. Edit these tables if the vocabulary
changes.
