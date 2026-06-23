// Package digest builds the failure-first run artifacts (issue #84, ADR-0023):
// a deterministic, capped FAILURES.md + failures.json (and optional per-failure
// files) written under the per-run XDG artifact dir, plus the loud last-line
// machine verdict. It operates on one or more report.Report values (N for the
// multi-OS standard path, 1 for run-and-die) so both execution paths share one
// contract. All new shapes live here; the ADR-0013-frozen report package is
// untouched.
package digest

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Default truncation caps (Option E, ADR-0023). A node --test assertion + stack
// is typically 15–30 lines; 40 lines / 8 KiB captures the actionable head while
// bounding a pathological multi-MB blob (parse.go reads up to a 4 MB line).
const (
	DefaultMaxLines = 40
	DefaultMaxBytes = 8 * 1024
)

// CapOpts bounds a text blob. A zero value means "use the defaults, head-first".
type CapOpts struct {
	MaxLines int  // <=0 → DefaultMaxLines
	MaxBytes int  // <=0 → DefaultMaxBytes
	Tail     bool // keep the trailing lines instead of the leading ones
}

func (o CapOpts) maxLines() int {
	if o.MaxLines <= 0 {
		return DefaultMaxLines
	}
	return o.MaxLines
}

func (o CapOpts) maxBytes() int {
	if o.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	return o.MaxBytes
}

// CapMeta describes what Cap removed. OmittedLines counts whole lines dropped by
// the line cap; OmittedBytes counts bytes dropped by the byte cap (the two caps
// are independent, so both can be non-zero).
type CapMeta struct {
	Truncated    bool
	OmittedLines int
	OmittedBytes int
}

// Cap truncates text to at most opts.MaxLines lines and opts.MaxBytes bytes,
// whichever binds first, cutting on a line boundary and never splitting a UTF-8
// rune. With opts.Tail it keeps the trailing portion (useful for captured
// output, whose actionable lines are at the end). It returns the kept text and
// metadata describing what was removed.
func Cap(text string, opts CapOpts) (string, CapMeta) {
	if text == "" {
		return "", CapMeta{}
	}
	// B-18: normalize CRLF → LF once at entry so no bare \r survives on kept
	// lines. Windows test reporters emit \r\n; without this, strings.Split on
	// "\n" retains the trailing \r and writeIndented emits stray CRs.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	maxLines := opts.maxLines()
	maxBytes := opts.maxBytes()

	var meta CapMeta
	lines := strings.Split(text, "\n")
	origLineCount := len(lines) // preserved for B-9 recomputation after byte cap
	if len(lines) > maxLines {
		meta.Truncated = true
		meta.OmittedLines = len(lines) - maxLines
		if opts.Tail {
			lines = lines[len(lines)-maxLines:]
		} else {
			lines = lines[:maxLines]
		}
	}
	kept := strings.Join(lines, "\n")

	if len(kept) > maxBytes {
		meta.Truncated = true
		if opts.Tail {
			cut := len(kept) - maxBytes
			for cut < len(kept) && !utf8.RuneStart(kept[cut]) {
				cut++ // advance to the next rune boundary
			}
			// Seek forward to the next \n so we don't truncate mid-line (B-10).
			// If there is no newline (single oversized line), fall through with
			// the rune-boundary cut — no better boundary exists.
			nl := strings.IndexByte(kept[cut:], '\n')
			if nl >= 0 {
				cut += nl + 1 // skip past the newline
			}
			meta.OmittedBytes = cut
			kept = kept[cut:]
		} else {
			cut := maxBytes
			for cut > 0 && !utf8.RuneStart(kept[cut]) {
				cut-- // back off to a rune boundary
			}
			// Seek back to the previous \n so we don't truncate mid-line (B-10).
			// If there is no newline before cut (single oversized line), fall
			// through with the rune-boundary cut — no better boundary exists.
			nl := strings.LastIndexByte(kept[:cut], '\n')
			if nl >= 0 {
				cut = nl // keep up to (but not including) the newline
			}
			meta.OmittedBytes = len(kept) - cut
			kept = kept[:cut]
		}
		// B-9: recompute OmittedLines as original-minus-final-kept so that the
		// line count reflects ALL lines removed (by either cap), not just the
		// lines removed by the line cap alone.
		finalLineCount := strings.Count(kept, "\n") + 1
		if kept == "" {
			finalLineCount = 0
		}
		meta.OmittedLines = origLineCount - finalLineCount
		if meta.OmittedLines < 0 {
			meta.OmittedLines = 0
		}
	}
	return kept, meta
}

// Pointer renders the explicit truncation footer Option E specifies, e.g.
//
//	… (truncated 1,240 lines · full at failures.json#/failures/0/output)
//
// The fragment is an RFC-6901 JSON Pointer into the produced failures.json
// document (FailuresDoc.Failures is the top-level "failures" array, so the
// pointer form is /failures/<index>/<field>).
// It returns "" when nothing was truncated.
func Pointer(meta CapMeta, ref string) string {
	if !meta.Truncated {
		return ""
	}
	var what string
	if meta.OmittedLines > 0 {
		what = humanInt(meta.OmittedLines) + " lines"
	} else {
		what = humanInt(meta.OmittedBytes) + " bytes"
	}
	return fmt.Sprintf("… (truncated %s · full at %s)", what, ref)
}

// humanInt formats n with thousands separators (1240 → "1,240").
func humanInt(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}
