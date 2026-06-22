package digest

import (
	"regexp"
	"sort"
	"strconv"
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
	reAbsPath = regexp.MustCompile(`(?:[a-z]:\\|/)[^\s:]+(?::\d+)*`)
	reDigits  = regexp.MustCompile(`\d+`)
	reWS      = regexp.MustCompile(`\s+`)
)

// normalizeMessage masks volatile tokens so the same logical failure groups
// despite run-to-run noise: lowercase, then mask hex addresses, absolute paths,
// and integer runs (which also collapses :line:col), and collapse whitespace.
// Over-grouping is preferred to under-grouping for the "M unique" headline.
func normalizeMessage(msg string) string {
	s := strings.ToLower(msg)
	s = reAbsPath.ReplaceAllString(s, "path") // before hex/digits so :line:col is consumed
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

var reFrameLineCol = regexp.MustCompile(`:(\d+):\d+`)

// deriveLine extracts a best-effort source line for a failure from the first
// stack frame mentioning the test file's base name (or, when file is empty, the
// first "<path>:<line>:<col>" frame). Returns 0 when nothing matches. The same
// helper backs the live ✗ FAIL line (Option I) and the digest's file:line.
func deriveLine(file, stack string) int {
	if stack == "" {
		return 0
	}
	base := file
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	lines := strings.Split(stack, "\n")
	if base != "" {
		for _, ln := range lines {
			if strings.Contains(ln, base) {
				if n := firstLineCol(ln); n > 0 {
					return n
				}
			}
		}
	}
	for _, ln := range lines {
		if n := firstLineCol(ln); n > 0 {
			return n
		}
	}
	return 0
}

func firstLineCol(s string) int {
	m := reFrameLineCol.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
