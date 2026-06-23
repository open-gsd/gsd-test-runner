package report_test

import (
	"testing"

	"github.com/open-gsd/gsd-test-runner/internal/report"
)

// TestDeriveLine covers the four required cases from B-22/B-23/B-24:
//
//  1. Message line embeds path:line:col — must not steal from the real frame.
//  2. Empty file + host:port in message (ECONNREFUSED) — must return 0, not the port.
//  3. Longer-filename hijack — xaa.test.js must not steal from a.test.js frame.
//  4. Normal single-frame case — baseline sanity.
func TestDeriveLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		file  string
		stack string
		want  int
	}{
		{
			// B-23: message line 0 embeds "a.test.js:1:2"; real frame is line 1.
			name: "message_col_not_stolen",
			file: "a.test.js",
			stack: "AssertionError: expected 'a.test.js:1:2' to equal 'foo'\n" +
				"    at Test (a.test.js:88:7)\n" +
				"    at callFn (/node/runner.js:12:1)",
			want: 88,
		},
		{
			// B-23 (empty-file fallback): ECONNREFUSED 127.0.0.1:5432 in message;
			// no "at " frames anywhere → 0, not 5432.
			name:  "econnrefused_no_at_frame",
			file:  "",
			stack: "Error: connect ECONNREFUSED 127.0.0.1:5432:0\n",
			want:  0,
		},
		{
			// B-24: xaa.test.js frame appears before a.test.js frame; must anchor
			// on the base-name and not match inside xaa.test.js.
			name: "basename_hijack_prevented",
			file: "a.test.js",
			stack: "Error: boom\n" +
				"    at xaa.test.js:99:1\n" +
				"    at helper (a.test.js:42:11)",
			want: 42,
		},
		{
			// Normal single-frame case.
			name: "normal_single_frame",
			file: "suite.test.js",
			stack: "Error: unexpected\n" +
				"    at Context.<anonymous> (suite.test.js:17:3)",
			want: 17,
		},
		{
			// Empty stack → 0.
			name:  "empty_stack",
			file:  "a.test.js",
			stack: "",
			want:  0,
		},
		{
			// Empty file, real "at " frame present → use it.
			name:  "empty_file_with_at_frame",
			file:  "",
			stack: "Error: boom\n    at helper (/src/util.js:55:3)",
			want:  55,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := report.DeriveLine(tc.file, tc.stack)
			if got != tc.want {
				t.Errorf("DeriveLine(%q, <stack>) = %d; want %d\nstack:\n%s",
					tc.file, got, tc.want, tc.stack)
			}
		})
	}
}
