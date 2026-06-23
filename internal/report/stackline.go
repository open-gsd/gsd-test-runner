package report

import (
	"regexp"
	"strconv"
	"strings"
)

// reFrameLineCol matches a ":line:col" suffix anywhere in a string.
var reFrameLineCol = regexp.MustCompile(`:(\d+):\d+`)

// DeriveLine extracts a best-effort source line number from the first stack
// frame that names the test file. It is the single authoritative implementation
// shared by internal/digest (group.go) and internal/pipeline (parse.go) —
// B-22 consolidation, fixing B-23 and B-24.
//
// Fixes applied vs. the original duplicate:
//
//   - B-23: only lines whose TrimSpace'd content starts with "at " are
//     considered stack frames, both in the base-name loop and in the empty-file
//     fallback. This prevents assertion messages that embed "path:line:col" or
//     "host:port" (e.g. ECONNREFUSED 127.0.0.1:5432) from stealing the line.
//
//   - B-24: the base-name match is anchored at a path-boundary character
//     (/, \, (, space) or at the start of the trimmed token so that a frame for
//     "xaa.test.js" does not satisfy a lookup for "a.test.js".
//
// Returns 0 when no suitable frame is found or when stack is empty.
func DeriveLine(file, stack string) int {
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
			// B-23 fix: only consider genuine stack frames.
			if !isAtFrame(ln) {
				continue
			}
			// B-24 fix: require a path-boundary before the base name.
			if containsBasename(ln, base) {
				if n := firstLineCol(ln); n > 0 {
					return n
				}
			}
		}
	}

	// Empty-file fallback: first genuine "at " frame with a :line:col token.
	for _, ln := range lines {
		// B-23 fix: only consider genuine stack frames.
		if !isAtFrame(ln) {
			continue
		}
		if n := firstLineCol(ln); n > 0 {
			return n
		}
	}
	return 0
}

// isAtFrame reports whether ln (after trimming leading whitespace) starts with
// "at ", which is the canonical Node.js / V8 stack-frame prefix.
func isAtFrame(ln string) bool {
	return strings.HasPrefix(strings.TrimSpace(ln), "at ")
}

// containsBasename reports whether ln contains base anchored at a path
// boundary (/, \, (, space, or start of the trimmed token). This prevents a
// longer filename like "xaa.test.js" from satisfying a lookup for "a.test.js".
func containsBasename(ln, base string) bool {
	idx := 0
	for {
		pos := strings.Index(ln[idx:], base)
		if pos < 0 {
			return false
		}
		abs := idx + pos
		// Accept if at position 0 of the (trimmed) token or preceded by a
		// path-boundary character.
		if abs == 0 {
			return true
		}
		prev := ln[abs-1]
		if prev == '/' || prev == '\\' || prev == '(' || prev == ' ' || prev == '\t' {
			return true
		}
		idx = abs + 1
	}
}

// firstLineCol returns the first line number captured by reFrameLineCol, or 0.
func firstLineCol(s string) int {
	m := reFrameLineCol.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
