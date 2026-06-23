package digest

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCap_CRLFNormalized verifies that CRLF (\r\n) input does not leave bare
// \r characters on kept lines in the output (B-18, Windows test output).
func TestCap_CRLFNormalized(t *testing.T) {
	in := "line1\r\nline2\r\nline3\r\n"
	got, _ := Cap(in, CapOpts{MaxLines: 10, MaxBytes: 1000})
	if strings.Contains(got, "\r") {
		t.Errorf("Cap left bare \\r in output (B-18): %q", got)
	}
	// The lines themselves must be intact (minus the CR).
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Errorf("Cap lost line content after CRLF normalization: %q", got)
	}
}

// TestCap_CRLFNormalizedTail verifies CRLF normalization in tail mode (B-18).
func TestCap_CRLFNormalizedTail(t *testing.T) {
	in := "line1\r\nline2\r\nline3\r\n"
	got, _ := Cap(in, CapOpts{MaxLines: 2, MaxBytes: 1000, Tail: true})
	if strings.Contains(got, "\r") {
		t.Errorf("Cap (tail) left bare \\r in output (B-18): %q", got)
	}
}

func TestCap_NoTruncation(t *testing.T) {
	in := "line1\nline2\nline3"
	got, meta := Cap(in, CapOpts{MaxLines: 10, MaxBytes: 1000})
	if got != in {
		t.Errorf("expected unchanged %q, got %q", in, got)
	}
	if meta.Truncated {
		t.Errorf("expected not truncated, got %+v", meta)
	}
}

func TestCap_LineCapHead(t *testing.T) {
	in := "a\nb\nc\nd\ne"
	got, meta := Cap(in, CapOpts{MaxLines: 2, MaxBytes: 1000})
	if got != "a\nb" {
		t.Errorf("expected first 2 lines, got %q", got)
	}
	if !meta.Truncated || meta.OmittedLines != 3 {
		t.Errorf("expected 3 omitted lines, got %+v", meta)
	}
}

func TestCap_LineCapTail(t *testing.T) {
	in := "a\nb\nc\nd\ne"
	got, meta := Cap(in, CapOpts{MaxLines: 2, MaxBytes: 1000, Tail: true})
	if got != "d\ne" {
		t.Errorf("expected last 2 lines, got %q", got)
	}
	if meta.OmittedLines != 3 {
		t.Errorf("expected 3 omitted lines, got %+v", meta)
	}
}

func TestCap_ByteCapHead(t *testing.T) {
	in := strings.Repeat("x", 100) // single line, no newlines
	got, meta := Cap(in, CapOpts{MaxLines: 10, MaxBytes: 10})
	if len(got) != 10 {
		t.Errorf("expected 10 bytes kept, got %d", len(got))
	}
	if !meta.Truncated || meta.OmittedBytes != 90 {
		t.Errorf("expected 90 omitted bytes, got %+v", meta)
	}
}

func TestCap_UTF8Boundary(t *testing.T) {
	// Each "😀" is 4 bytes; cap at 6 bytes must not split a rune.
	in := strings.Repeat("😀", 5) // 20 bytes
	got, _ := Cap(in, CapOpts{MaxLines: 10, MaxBytes: 6})
	if !utf8.ValidString(got) {
		t.Errorf("Cap split a rune: %q is not valid UTF-8", got)
	}
	if len(got) > 6 {
		t.Errorf("expected <=6 bytes, got %d", len(got))
	}
}

func TestCap_UTF8BoundaryTail(t *testing.T) {
	in := strings.Repeat("😀", 5)
	got, _ := Cap(in, CapOpts{MaxLines: 10, MaxBytes: 6, Tail: true})
	if !utf8.ValidString(got) {
		t.Errorf("Cap (tail) split a rune: %q is not valid UTF-8", got)
	}
}

func TestPointer(t *testing.T) {
	cases := []struct {
		name string
		meta CapMeta
		ref  string
		want string
	}{
		{"none", CapMeta{}, "failures.json#/failures/0/stack", ""},
		{"lines", CapMeta{Truncated: true, OmittedLines: 1240}, "failures.json#/failures/0/output",
			"… (truncated 1,240 lines · full at failures.json#/failures/0/output)"},
		{"bytes", CapMeta{Truncated: true, OmittedBytes: 500}, "failures.json#/failures/0/stack",
			"… (truncated 500 bytes · full at failures.json#/failures/0/stack)"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := Pointer(tc.meta, tc.ref); got != tc.want {
				t.Errorf("Pointer = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCap_ByteCapCutsOnLineBoundaryHead verifies that the byte cap does not
// truncate the output mid-line (B-10, head mode).
func TestCap_ByteCapCutsOnLineBoundaryHead(t *testing.T) {
	// 4 lines, each 30 bytes. Total = 124 bytes (incl. 3 newlines).
	// Cap at 50 bytes → mid-way through line 2 if we didn't seek to newline.
	// We want the result to end on a line boundary (no partial line).
	line := strings.Repeat("a", 30)
	in := line + "\n" + line + "\n" + line + "\n" + line
	got, meta := Cap(in, CapOpts{MaxLines: 100, MaxBytes: 50})
	if !meta.Truncated {
		t.Fatal("expected truncation")
	}
	// No line in the output should be a partial copy of the original line.
	for _, ln := range strings.Split(got, "\n") {
		if len(ln) > 0 && len(ln) < 30 && strings.TrimLeft(ln, "a") == "" {
			t.Errorf("byte cap left a partial line of length %d (want line boundary cut): %q", len(ln), ln)
		}
	}
	// The kept text must not end mid-rune or mid-line (no trailing partial segment).
	if strings.HasSuffix(got, "a") {
		// The last character is 'a' — check it's a complete line.
		lastNL := strings.LastIndexByte(got, '\n')
		tail := got[lastNL+1:]
		if len(tail) > 0 && len(tail) < 30 {
			t.Errorf("tail after last newline is partial (%d bytes): %q", len(tail), tail)
		}
	}
}

// TestCap_ByteCapCutsOnLineBoundaryTail verifies the same for tail mode (B-10).
func TestCap_ByteCapCutsOnLineBoundaryTail(t *testing.T) {
	line := strings.Repeat("b", 30)
	in := line + "\n" + line + "\n" + line + "\n" + line
	got, meta := Cap(in, CapOpts{MaxLines: 100, MaxBytes: 50, Tail: true})
	if !meta.Truncated {
		t.Fatal("expected truncation (tail)")
	}
	for _, ln := range strings.Split(got, "\n") {
		if len(ln) > 0 && len(ln) < 30 && strings.TrimLeft(ln, "b") == "" {
			t.Errorf("byte cap (tail) left a partial line of length %d: %q", len(ln), ln)
		}
	}
}

// TestCap_ByteCapSingleOversizedLine verifies the guard for a single line that
// exceeds maxBytes (B-10): when there is no newline boundary available, Cap
// falls back to a rune-boundary cut (no partial UTF-8 rune, but possibly
// partial line — documented relaxation for the single-line case).
func TestCap_ByteCapSingleOversizedLine(t *testing.T) {
	bigLine := strings.Repeat("z", 200)
	got, meta := Cap(bigLine, CapOpts{MaxLines: 100, MaxBytes: 50})
	if !meta.Truncated {
		t.Fatal("expected truncation")
	}
	// Single-line fallback: rune-boundary cut keeps up to maxBytes bytes.
	if len(got) > 50 {
		t.Errorf("single-oversized-line head: got %d bytes, want <= 50", len(got))
	}
	_ = meta
}

// TestCap_BothCapsFireOmissionCount verifies that when BOTH the line cap AND the
// byte cap fire, OmittedLines reflects the total lines NOT present in the output,
// not just the lines removed by the line cap alone (B-9).
func TestCap_BothCapsFireOmissionCount(t *testing.T) {
	// Build a blob with 50 lines each 300 bytes long → 15 000 bytes total.
	// Line cap is 40 → 10 lines removed by line cap (kept = 40 lines, ~12 000 bytes).
	// Byte cap is 8192 → additional lines removed from kept.
	const lineLen = 300
	const numLines = 50
	line := strings.Repeat("x", lineLen)
	in := strings.Repeat(line+"\n", numLines)
	// Trim trailing \n so Split gives exactly numLines entries.
	in = strings.TrimRight(in, "\n")

	got, meta := Cap(in, CapOpts{MaxLines: 40, MaxBytes: 8 * 1024})

	if !meta.Truncated {
		t.Fatal("expected Truncated=true")
	}

	// Count how many lines actually appear in the output.
	finalLines := strings.Split(got, "\n")
	// OmittedLines must equal original count minus final kept count.
	wantOmitted := numLines - len(finalLines)
	if wantOmitted <= 10 {
		// Sanity: byte cap must have fired too (removed more than just line cap).
		t.Fatalf("test setup wrong: wantOmitted=%d should be >10 (byte cap should also fire)", wantOmitted)
	}
	if meta.OmittedLines != wantOmitted {
		t.Errorf("OmittedLines = %d, want %d (original %d lines - kept %d lines); "+
			"byte cap removed additional lines that were not counted (B-9)",
			meta.OmittedLines, wantOmitted, numLines, len(finalLines))
	}
}

// TestCap_BothCapsFireTailOmissionCount is the tail variant of B-9.
func TestCap_BothCapsFireTailOmissionCount(t *testing.T) {
	const lineLen = 300
	const numLines = 50
	line := strings.Repeat("y", lineLen)
	in := strings.Repeat(line+"\n", numLines)
	in = strings.TrimRight(in, "\n")

	got, meta := Cap(in, CapOpts{MaxLines: 40, MaxBytes: 8 * 1024, Tail: true})

	if !meta.Truncated {
		t.Fatal("expected Truncated=true")
	}
	finalLines := strings.Split(got, "\n")
	wantOmitted := numLines - len(finalLines)
	if meta.OmittedLines != wantOmitted {
		t.Errorf("OmittedLines (tail) = %d, want %d (B-9)", meta.OmittedLines, wantOmitted)
	}
}

func TestHumanInt(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1240: "1,240", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
}
