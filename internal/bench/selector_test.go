package bench

import (
	"errors"
	"sync"
	"testing"
)

// helpers

func linuxBench(name string) Bench {
	return Bench{Name: name, Host: name, OS: "linux"}
}

func windowsBench(name string) Bench {
	return Bench{Name: name, Host: name, OS: "windows"}
}

// TestNewSelector_EmptyRegistry verifies that NewSelector succeeds with an
// empty registry; Pick then returns NoBenchForOSError.
func TestNewSelector_EmptyRegistry(t *testing.T) {
	t.Run("NewSelector succeeds on empty registry", func(t *testing.T) {
		sel, err := NewSelector(nil, Options{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		_, pickErr := sel.Pick("linux")
		var target *NoBenchForOSError
		if !errors.As(pickErr, &target) {
			t.Fatalf("expected *NoBenchForOSError, got %v", pickErr)
		}
		if target.OS != "linux" {
			t.Errorf("expected OS=linux in error, got %q", target.OS)
		}
	})
}

// TestNewSelector_NoOptions verifies round-robin with no filters applied.
func TestNewSelector_NoOptions(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
		linuxBench("lab-3"),
	}
	sel, err := NewSelector(registry, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("first pick returns lab-1", func(t *testing.T) {
		b, err := sel.Pick("linux")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Name != "lab-1" {
			t.Errorf("expected lab-1, got %s", b.Name)
		}
	})
	t.Run("second pick returns lab-2", func(t *testing.T) {
		b, err := sel.Pick("linux")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Name != "lab-2" {
			t.Errorf("expected lab-2, got %s", b.Name)
		}
	})
	t.Run("third pick returns lab-3", func(t *testing.T) {
		b, err := sel.Pick("linux")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Name != "lab-3" {
			t.Errorf("expected lab-3, got %s", b.Name)
		}
	})
}

// TestPick_CursorWrapsAround verifies that the 4th pick on a 3-bench registry
// returns the first bench again.
func TestPick_CursorWrapsAround(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
		linuxBench("lab-3"),
	}
	sel, err := NewSelector(registry, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain the three benches
	for i := 0; i < 3; i++ {
		if _, err := sel.Pick("linux"); err != nil {
			t.Fatalf("pick %d failed: %v", i+1, err)
		}
	}

	// 4th pick should wrap back to lab-1
	b, err := sel.Pick("linux")
	if err != nil {
		t.Fatalf("unexpected error on 4th pick: %v", err)
	}
	if b.Name != "lab-1" {
		t.Errorf("expected lab-1 on wrap-around, got %s", b.Name)
	}
}

// TestPick_PerOSCursorsAreIndependent verifies that Pick("linux") and
// Pick("windows") maintain separate cursors.
func TestPick_PerOSCursorsAreIndependent(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-linux-1"),
		linuxBench("lab-linux-2"),
		windowsBench("lab-win-1"),
		windowsBench("lab-win-2"),
	}
	sel, err := NewSelector(registry, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Advance linux cursor twice
	for i := 0; i < 2; i++ {
		if _, err := sel.Pick("linux"); err != nil {
			t.Fatalf("linux pick %d failed: %v", i+1, err)
		}
	}

	// Windows cursor should still be at index 0
	wb, err := sel.Pick("windows")
	if err != nil {
		t.Fatalf("unexpected error picking windows: %v", err)
	}
	if wb.Name != "lab-win-1" {
		t.Errorf("expected lab-win-1 (cursor at 0), got %s", wb.Name)
	}

	// Linux cursor should now be at index 2 (wraps back to lab-linux-1)
	lb, err := sel.Pick("linux")
	if err != nil {
		t.Fatalf("unexpected error picking linux: %v", err)
	}
	if lb.Name != "lab-linux-1" {
		t.Errorf("expected lab-linux-1 (wrapped), got %s", lb.Name)
	}
}

// TestNewSelector_Pin verifies that Options.Pin filters the registry to the
// single named bench.
func TestNewSelector_Pin(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
		linuxBench("lab-3"),
	}
	sel, err := NewSelector(registry, Options{Pin: "lab-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All picks should return lab-1 (only member of filtered registry)
	for i := 0; i < 3; i++ {
		b, err := sel.Pick("linux")
		if err != nil {
			t.Fatalf("pick %d failed: %v", i+1, err)
		}
		if b.Name != "lab-1" {
			t.Errorf("pick %d: expected lab-1, got %s", i+1, b.Name)
		}
	}
}

// TestNewSelector_PinNotInRegistry verifies *PinnedBenchNotInRegistryError
// when Pin names a bench absent from the registry.
func TestNewSelector_PinNotInRegistry(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
	}
	_, err := NewSelector(registry, Options{Pin: "nonexistent"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *PinnedBenchNotInRegistryError
	if !errors.As(err, &target) {
		t.Fatalf("expected *PinnedBenchNotInRegistryError, got %T: %v", err, err)
	}
	if target.Pin != "nonexistent" {
		t.Errorf("expected Pin=nonexistent, got %q", target.Pin)
	}
	if len(target.Available) != 2 {
		t.Errorf("expected 2 available benches, got %d: %v", len(target.Available), target.Available)
	}
}

// TestNewSelector_Exclude verifies that Options.Exclude removes named benches.
func TestNewSelector_Exclude(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
		linuxBench("lab-3"),
	}
	sel, err := NewSelector(registry, Options{Exclude: []string{"lab-1"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Pick should cycle through lab-2, lab-3 only
	b1, err := sel.Pick("linux")
	if err != nil {
		t.Fatalf("pick 1 failed: %v", err)
	}
	if b1.Name == "lab-1" {
		t.Error("excluded bench lab-1 was returned by Pick")
	}

	b2, err := sel.Pick("linux")
	if err != nil {
		t.Fatalf("pick 2 failed: %v", err)
	}
	if b2.Name == "lab-1" {
		t.Error("excluded bench lab-1 was returned by Pick")
	}

	if b1.Name == b2.Name {
		t.Errorf("expected different benches on consecutive picks, got %s twice", b1.Name)
	}
}

// TestNewSelector_PinExcludeConflict verifies *PinExcludeConflictError when
// Pin is also present in Exclude.
func TestNewSelector_PinExcludeConflict(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
	}
	_, err := NewSelector(registry, Options{
		Pin:     "lab-1",
		Exclude: []string{"lab-1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *PinExcludeConflictError
	if !errors.As(err, &target) {
		t.Fatalf("expected *PinExcludeConflictError, got %T: %v", err, err)
	}
	if target.Pin != "lab-1" {
		t.Errorf("expected Pin=lab-1, got %q", target.Pin)
	}
}

// TestNewSelector_PinWithNonOverlappingExclude verifies that when Pin and
// Exclude don't overlap, Pin filtering is honored and the non-overlapping
// excludes are a no-op against the already-pinned set.
func TestNewSelector_PinWithNonOverlappingExclude(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
		linuxBench("lab-3"),
	}
	sel, err := NewSelector(registry, Options{
		Pin:     "lab-1",
		Exclude: []string{"lab-2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, err := sel.Pick("linux")
	if err != nil {
		t.Fatalf("unexpected error picking: %v", err)
	}
	if b.Name != "lab-1" {
		t.Errorf("expected lab-1 (pinned), got %s", b.Name)
	}
}

// TestPick_NoBenchForOS verifies *NoBenchForOSError when OS has no matching
// benches in the post-filter registry.
func TestPick_NoBenchForOS(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
	}
	sel, err := NewSelector(registry, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, pickErr := sel.Pick("windows")
	if pickErr == nil {
		t.Fatal("expected error, got nil")
	}
	var target *NoBenchForOSError
	if !errors.As(pickErr, &target) {
		t.Fatalf("expected *NoBenchForOSError, got %T: %v", pickErr, pickErr)
	}
	if target.OS != "windows" {
		t.Errorf("expected OS=windows in error, got %q", target.OS)
	}
}

// TestPick_Concurrent verifies that concurrent Pick calls under the race
// detector don't produce data races.
func TestPick_Concurrent(t *testing.T) {
	registry := []Bench{
		linuxBench("lab-1"),
		linuxBench("lab-2"),
		linuxBench("lab-3"),
	}
	sel, err := NewSelector(registry, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			b, err := sel.Pick("linux")
			if err != nil {
				t.Errorf("concurrent Pick failed: %v", err)
				return
			}
			if b.OS != "linux" {
				t.Errorf("unexpected OS %q from concurrent Pick", b.OS)
			}
		}()
	}
	wg.Wait()
}
