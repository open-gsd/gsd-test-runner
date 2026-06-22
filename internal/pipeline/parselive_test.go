package pipeline

import "testing"

// TestParseLiveTestEvent_FailCarriesEvidence verifies that the live-tail parser
// surfaces the one-line error, the error_class, and a best-effort source line
// derived from the stack so the renderer can print the real-time ✗ FAIL line
// (Option I, #84).
func TestParseLiveTestEvent_FailCarriesEvidence(t *testing.T) {
	line := []byte(`{"type":"test_event","kind":"fail","name":"s > x","file":"a.test.js",` +
		`"error":"expected 1, got 2\nsecond line","error_class":"assertion",` +
		`"stack":"AssertionError\n    at a (a.test.js:12:5)\n    at z (other.js:3:1)"}`)

	ev, ok := parseLiveTestEvent(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if ev.Kind != "fail" {
		t.Errorf("kind = %q, want fail", ev.Kind)
	}
	if ev.ErrorClass != "assertion" {
		t.Errorf("error_class = %q, want assertion", ev.ErrorClass)
	}
	if ev.Error != "expected 1, got 2" {
		t.Errorf("error = %q, want first line only", ev.Error)
	}
	if ev.Line != 12 {
		t.Errorf("derived line = %d, want 12 (first frame matching a.test.js)", ev.Line)
	}
}

func TestParseLiveTestEvent_PassMinimal(t *testing.T) {
	ev, ok := parseLiveTestEvent([]byte(`{"type":"test_event","kind":"pass","name":"ok","file":"b.test.js"}`))
	if !ok {
		t.Fatal("expected parse ok")
	}
	if ev.Kind != "pass" || ev.File != "b.test.js" || ev.Line != 0 {
		t.Errorf("unexpected pass event: %+v", ev)
	}
}

func TestParseLiveTestEvent_RejectsNonTestEvent(t *testing.T) {
	if _, ok := parseLiveTestEvent([]byte(`{"type":"diagnostic"}`)); ok {
		t.Error("non-test_event line should be rejected")
	}
	if _, ok := parseLiveTestEvent([]byte(`not json`)); ok {
		t.Error("malformed line should be rejected")
	}
}
