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
// The Pipeline engine (ADR-0027) does not carry the branch through to its
// StartContainer leg yet; pipeline naming is tracked as a follow-up.

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

// branchSlugUnknown and branchSlugEmpty are the sentinel slugs used when no
// branch can be derived from the spec. They are non-empty so ContainerName
// always produces a valid Docker name.
const (
	branchSlugUnknown = "unknown"
	branchSlugEmpty   = "branch"
)

// slugifyBranch lowercases s, replaces every run of illegal bytes with a
// single '-', trims leading/trailing '-', and returns "branch" when the result
// would otherwise be empty (so the value is always safe to embed in a Docker
// container name).
func slugifyBranch(s string) string {
	s = strings.ToLower(s)
	s = slugIllegalRe.ReplaceAllString(s, "-")
	s = slugCollapseRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return branchSlugEmpty
	}
	return s
}

// BranchSlug returns the slug identifying which branch this run's container
// belongs to, for both the container name and the sh.gsd-test.branch label.
// Resolution order: PRBranch (the fix being tested), then Base (the trunk a
// bare-repo run executes against), then "unknown" when neither is set. The
// return value is always non-empty.
func (s Spec) BranchSlug() string {
	switch {
	case s.PRBranch != "":
		return slugifyBranch(s.PRBranch)
	case s.Base != "":
		return slugifyBranch(s.Base)
	default:
		return branchSlugUnknown
	}
}

// ContainerName returns the deterministic Docker --name for this run's
// container: `gsd-test-<slug>-<short-runId>`. The runId tail guarantees
// uniqueness across concurrent runs of the same branch (Docker rejects name
// collisions on `docker create`); the slug makes the branch legible at a
// glance. The result is always <= containerNameCeiling bytes; on truncation
// the slug is shortened and the runId tail is preserved.
func (s Spec) ContainerName() string {
	slug := s.BranchSlug()
	tail := s.RunID
	if len(tail) > runIDTailLen {
		tail = tail[:runIDTailLen]
	}
	// If tail is empty (RunID not yet assigned), fall back to "noid" so the
	// name is still well-formed and unambiguous.
	if tail == "" {
		tail = "noid"
	}
	const prefix = "gsd-test-"
	// Fixed overhead: prefix + slug + "-" + tail.
	minLen := len(prefix) + len(slug) + 1 + len(tail)
	if minLen <= containerNameCeiling {
		return prefix + slug + "-" + tail
	}
	// Truncate the slug, never the tail. The tail carries run-uniqueness.
	over := minLen - containerNameCeiling
	maxSlug := len(slug) - over
	if maxSlug < 1 {
		// Pathological: even an empty slug exceeds the ceiling (impossible with
		// realistic tail lengths, but guard anyway). Drop the slug entirely.
		return prefix + tail
	}
	slug = strings.TrimRight(slug[:maxSlug], "-")
	return prefix + slug + "-" + tail
}
