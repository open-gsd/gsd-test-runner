package digest

import (
	"regexp"
	"sort"
	"strings"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// GroupKey identifies failures considered "the same" across OSes (Option G).
type GroupKey struct {
	File       string
	Name       string
	ErrorClass report.ErrorClass
	NormMsg    string
}

// Group is a set of identical failures across one or more platforms.
type Group struct {
	Key       GroupKey
	Platforms []string          // sorted, de-duplicated OS names that hit this failure
	Sample    report.FailedTest // representative instance (first seen)
	Count     int               // total occurrences across platforms (includes retries)
}

var (
	reHexAddr = regexp.MustCompile(`0x[0-9a-f]+`)
	// reAbsPath matches genuine filesystem absolute paths (B-15 fix).
	//
	// The original `(?:[a-z]:\\|/)[^\s:]+(?::\d+)*` matched ANY token starting
	// with `/`, which collapsed regex literals (/foo/), date strings (matching
	// /01/02 inside 2024/01/02), and URL routes (/v1/users) into "path",
	// merging distinct failures.
	//
	// The fixed pattern requires:
	//   - A token boundary: the match must be preceded by whitespace or
	//     punctuation (space, (, ,, ") — prevents matching sub-tokens like
	//     the /01/02 inside 2024/01/02.
	//   - Multi-separator path shape: at least three path components
	//     (/seg/seg/…) for Unix paths — prevents matching single-component
	//     regex literals (/foo/) and two-component API routes (/v1/users).
	//   - Windows drive paths retain their own branch (C:\…).
	//
	// ReplaceAllStringFunc (see normalizeMessage) preserves the leading
	// whitespace/punctuation that the match consumed.
	reAbsPath = regexp.MustCompile(
		`(?:^|[\s(,"])/[^\s:/]+/[^\s:/]+/[^\s:]*(?::\d+)*` +
			`|(?:^|[\s(,"])[a-z]:\\[^\s]*(?::\d+)*`,
	)
	reDigits = regexp.MustCompile(`\d+`)
	reWS     = regexp.MustCompile(`\s+`)
)

// normalizeMessage masks volatile tokens so the same logical failure groups
// despite run-to-run noise: lowercase, then mask hex addresses, absolute paths,
// and integer runs (which also collapses :line:col), and collapse whitespace.
// Over-grouping is preferred to under-grouping for the "M unique" headline.
func normalizeMessage(msg string) string {
	s := strings.ToLower(msg)
	// Replace abs paths before hex/digits so :line:col is consumed.
	// Use ReplaceAllStringFunc to preserve any leading whitespace/punctuation
	// that the pattern consumed to establish the token boundary (B-15).
	s = reAbsPath.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) > 0 && m[0] != '/' && !(m[0] >= 'a' && m[0] <= 'z') {
			// Leading char is whitespace or punctuation — preserve it.
			return string(m[0]) + "path"
		}
		return "path"
	})
	s = reHexAddr.ReplaceAllString(s, "addr") // digit-free placeholder, survives the int mask
	s = reDigits.ReplaceAllString(s, "n")
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// GroupFailures buckets failures from reps by GroupKey. Each report contributes
// its OS (report.OS) to a group's Platforms. The result is sorted
// deterministically (File, Name, ErrorClass, NormMsg) so the digest is
// reproducible regardless of input or map-iteration order.
func GroupFailures(reps []report.Report) []Group {
	type acc struct {
		g         Group
		platforms map[string]bool
	}
	m := map[GroupKey]*acc{}
	for _, rep := range reps {
		for _, f := range rep.Failures {
			key := GroupKey{
				File:       f.File,
				Name:       f.Name,
				ErrorClass: f.ErrorClass,
				NormMsg:    normalizeMessage(f.Error),
			}
			a := m[key]
			if a == nil {
				a = &acc{g: Group{Key: key, Sample: f}, platforms: map[string]bool{}}
				m[key] = a
			} else {
				// B-16: keep the lexicographically-minimal (Error, Stack, Output)
				// sample so the displayed failure content is deterministic across
				// runs regardless of goroutine-completion order.
				cur := a.g.Sample
				if f.Error < cur.Error ||
					(f.Error == cur.Error && f.Stack < cur.Stack) ||
					(f.Error == cur.Error && f.Stack == cur.Stack && f.Output < cur.Output) {
					a.g.Sample = f
				}
			}
			a.g.Count++
			if rep.OS != "" {
				a.platforms[rep.OS] = true
			}
		}
	}

	groups := make([]Group, 0, len(m))
	for _, a := range m {
		plats := make([]string, 0, len(a.platforms))
		for p := range a.platforms {
			plats = append(plats, p)
		}
		sort.Strings(plats)
		a.g.Platforms = plats
		groups = append(groups, a.g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		gi, gj := groups[i].Key, groups[j].Key
		switch {
		case gi.File != gj.File:
			return gi.File < gj.File
		case gi.Name != gj.Name:
			return gi.Name < gj.Name
		case gi.ErrorClass != gj.ErrorClass:
			return gi.ErrorClass < gj.ErrorClass
		default:
			return gi.NormMsg < gj.NormMsg
		}
	})
	return groups
}

// deriveLine is the shared implementation from internal/report (B-22/B-23/B-24
// fix). Exposed here as a package-level alias so callers in this package read
// naturally. See report.DeriveLine for the full contract and bug-fix notes.
func deriveLine(file, stack string) int {
	return report.DeriveLine(file, stack)
}
