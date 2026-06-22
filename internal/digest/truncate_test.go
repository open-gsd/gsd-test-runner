package digest

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
		{"none", CapMeta{}, "failures.json#/0/stack", ""},
		{"lines", CapMeta{Truncated: true, OmittedLines: 1240}, "failures.json#/0/output",
			"… (truncated 1,240 lines · full at failures.json#/0/output)"},
		{"bytes", CapMeta{Truncated: true, OmittedBytes: 500}, "failures.json#/0/stack",
			"… (truncated 500 bytes · full at failures.json#/0/stack)"},
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

func TestHumanInt(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1240: "1,240", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
}
