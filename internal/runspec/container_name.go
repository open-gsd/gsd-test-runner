package runspec

import (
	"regexp"
	"strings"
)

// ADR-0029 — Branch-derived container naming and branch-scoped reaper ownership.
//
// The dispatch/Watchdog execution engine names every run container
// `gsd-test-<branch-slug>-<short-runId>` so a Bench operator can tell at a
// glance (`docker ps`) which branch each container is testing and confirm a
// container was spawned by gsd-test-runner. The same slug is carried on a
// `sh.gsd-test.branch` label so the Tier-2 reaper (ADR-0021 Decision 2) can
// scope ownership: `Sweep(ctx, run, now, slug)` reaps only containers whose
// slug matches the current invocation, leaving containers from unrelated
// branches for their own invocations to reap.
//
// The Pipeline engine (ADR-0027) also carries the branch through to its
// StartContainer leg (ADR-0029 §4 amendment): internal/pipeline.New takes a
// ContainerIdentity built by internal/runner from the same SlugifyBranch and
// BuildContainerName used here, so the Tier-2 reaper sees and scopes Pipeline
// containers exactly like dispatch/Watchdog containers.

// slugIllegalRe matches any byte that is not legal in a Docker container name
// after the leading "gsd-test-" prefix. Docker permits [a-zA-Z0-9][a-zA-Z0-9_.-]*;
// we fold case and use the safe subset [a-z0-9._-].
var slugIllegalRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// slugCollapseRe matches runs of '-' produced after illegal-char replacement
// so they collapse to a single '-'.
var slugCollapseRe = regexp.MustCompile(`-{2,}`)

// containerNameCeiling is the effective maximum length of a Docker container
// name (Docker enforces 63 + NUL for the underlying hostname). ContainerName
// never returns more than this many bytes.
const containerNameCeiling = 63

// runIDTailLen is the number of leading RunID characters appended to the
// branch slug to guarantee name uniqueness across concurrent runs of the same
// branch (Docker rejects name collisions on `docker create`). 8 hex chars from
// a UUID v4 = 32 bits of entropy, ample for collision avoidance in practice.
const runIDTailLen = 8

// BranchSlugUnknown and branchSlugEmpty are the sentinel slugs used when no
// branch can be derived from the spec. They are non-empty so ContainerName
// always produces a valid Docker name. BranchSlugUnknown is exported so other
// engines (e.g. Pipeline via internal/runner) that resolve their own branch
// outside of a Spec can fall back to the exact same sentinel value.
const (
	BranchSlugUnknown = "unknown"
	branchSlugEmpty   = "branch"
)

// SlugifyBranch lowercases s, replaces every run of illegal bytes with a
// single '-', trims leading/trailing '-', and returns "branch" when the result
// would otherwise be empty (so the value is always safe to embed in a Docker
// container name).
//
// This is the single implementation of the ADR-0029 slug rules. Any engine
// (dispatch/Watchdog, Pipeline, ...) that needs to derive a branch slug must
// call this function directly so the derivation cannot drift between engines.
func SlugifyBranch(s string) string {
	s = strings.ToLower(s)
	s = slugIllegalRe.ReplaceAllString(s, "-")
	s = slugCollapseRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return branchSlugEmpty
	}
	return s
}

// slugifyBranch is a thin alias retained for existing in-package callers; the
// single implementation body lives in SlugifyBranch.
func slugifyBranch(s string) string {
	return SlugifyBranch(s)
}

// BranchSlug returns the slug identifying which branch this run's container
// belongs to, for both the container name and the sh.gsd-test.branch label.
// Resolution order: PRBranch (the fix being tested), then Base (the trunk a
// bare-repo run executes against), then "unknown" when neither is set. The
// return value is always non-empty.
func (s Spec) BranchSlug() string {
	switch {
	case s.PRBranch != "":
		return SlugifyBranch(s.PRBranch)
	case s.Base != "":
		return SlugifyBranch(s.Base)
	default:
		return BranchSlugUnknown
	}
}

// BuildContainerName produces a Docker --name from an already-slugified
// branch slug, an optional cell identifier, and a run ID:
// `gsd-test-<slug>-<cell>-<short-runId>` when cell is non-empty, or
// `gsd-test-<slug>-<short-runId>` when cell is empty. short-runId is the
// first runIDTailLen characters of runID, or the literal "noid" when runID is
// empty. cell is assumed already slug-safe (caller-supplied) and is never
// re-slugified here.
//
// This is the single implementation of the ADR-0029 §1 name-construction
// rules; Spec.ContainerName delegates to it so the dispatch/Watchdog engine
// and any other engine (e.g. Pipeline) that needs a container name build it
// from the exact same logic and cannot drift apart.
//
// The result is always <= containerNameCeiling bytes. On truncation, only the
// slug is shortened — the cell and the runId tail are preserved verbatim
// because they carry the uniqueness guarantee (Docker rejects duplicate
// --name values). Any trailing "-" left by truncating the slug is trimmed. If
// even a fully-truncated (empty) slug cannot fit, the slug is dropped
// entirely (existing pathological guard).
func BuildContainerName(slug, cell, runID string) string {
	tail := runID
	if len(tail) > runIDTailLen {
		tail = tail[:runIDTailLen]
	}
	// If tail is empty (RunID not yet assigned), fall back to "noid" so the
	// name is still well-formed and unambiguous.
	if tail == "" {
		tail = "noid"
	}
	const prefix = "gsd-test-"

	// tailPart is the fixed suffix appended after the slug: "-cell-tail" when
	// cell is set, or just "-tail" otherwise. It is never truncated.
	var tailPart string
	if cell != "" {
		tailPart = "-" + cell + "-" + tail
	} else {
		tailPart = "-" + tail
	}

	minLen := len(prefix) + len(slug) + len(tailPart)
	if minLen <= containerNameCeiling {
		return prefix + slug + tailPart
	}
	// Truncate the slug, never the cell or the tail. Those carry uniqueness.
	over := minLen - containerNameCeiling
	maxSlug := len(slug) - over
	if maxSlug < 1 {
		// Pathological: even an empty slug exceeds the ceiling (impossible with
		// realistic tail/cell lengths, but guard anyway). Drop the slug (and its
		// separating dash) entirely.
		if cell != "" {
			return prefix + cell + "-" + tail
		}
		return prefix + tail
	}
	slug = strings.TrimRight(slug[:maxSlug], "-")
	return prefix + slug + tailPart
}

// ContainerName returns the deterministic Docker --name for this run's
// container: `gsd-test-<slug>-<short-runId>`. The runId tail guarantees
// uniqueness across concurrent runs of the same branch (Docker rejects name
// collisions on `docker create`); the slug makes the branch legible at a
// glance. The result is always <= containerNameCeiling bytes; on truncation
// the slug is shortened and the runId tail is preserved.
func (s Spec) ContainerName() string {
	return BuildContainerName(s.BranchSlug(), "", s.RunID)
}
