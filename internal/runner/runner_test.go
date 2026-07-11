package runner

import "testing"

func TestCommaSplit_Empty(t *testing.T) {
	got := commaSplit("")
	if got != nil {
		t.Errorf("commaSplit(%q): got %v, want nil", "", got)
	}
}

func TestCommaSplit_Single(t *testing.T) {
	got := commaSplit("linux")
	if len(got) != 1 || got[0] != "linux" {
		t.Errorf("commaSplit(%q): got %v, want [linux]", "linux", got)
	}
}

func TestCommaSplit_Multiple(t *testing.T) {
	got := commaSplit("linux,windows,macos")
	if len(got) != 3 {
		t.Errorf("commaSplit(%q): got len=%d, want 3: %v", "linux,windows,macos", len(got), got)
		return
	}
	want := []string{"linux", "windows", "macos"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("commaSplit index %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestCommaSplit_TrimsWhitespace(t *testing.T) {
	got := commaSplit(" linux , windows ")
	if len(got) != 2 {
		t.Errorf("commaSplit(whitespace): got len=%d, want 2: %v", len(got), got)
		return
	}
	if got[0] != "linux" {
		t.Errorf("commaSplit[0]: got %q, want %q", got[0], "linux")
	}
	if got[1] != "windows" {
		t.Errorf("commaSplit[1]: got %q, want %q", got[1], "windows")
	}
}
