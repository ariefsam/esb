package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunStore_CapRejectsAt1000 locks in the safety net introduced in
// Phase 1 of the approved plan: once the in-memory store reaches
// runStoreCap, Start must reject the next run with ErrStoreFull instead
// of growing without bound. A runaway tab hammering /commands/execute
// cannot exhaust process memory.
func TestRunStore_CapRejectsAt1000(t *testing.T) {
	store := NewRunStore()
	// Seed the store with exactly runStoreCap already-finished runs so
	// the next Start hits the cap.
	for i := 0; i < runStoreCap; i++ {
		id := newRunID(uint64(i))
		store.runs[id] = &Run{
			ID:        id,
			CommandID: "show",
			Argv:      []string{"esb", "show"},
			Dir:       t.TempDir(),
			StartedAt: time.Now(),
			FinishedAt: time.Now(),
			Status:    RunSucceed,
			ExitCode:  0,
		}
	}
	// active is left false so the cap check is the only reason Start
	// can fail here. A conflicting run is a separate code path with
	// its own sentinel.
	_, err := store.Start(context.Background(), t.TempDir(), "show", FormInput{}, &fakeRunner{})
	if !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Start at cap = %v, want ErrStoreFull", err)
	}
	if got := store.Cap(); got != 1000 {
		t.Errorf("Cap() = %d, want 1000", got)
	}
	if got := len(store.runs); got != runStoreCap {
		t.Errorf("runs len after rejection = %d, want %d", got, runStoreCap)
	}
}

// TestRunStore_StartConflictingActiveReturnsErrConflict guards the
// sibling code path so a future refactor of the cap check cannot
// silently swallow the busy signal into ErrStoreFull (or vice versa).
func TestRunStore_StartConflictingActiveReturnsErrConflict(t *testing.T) {
	store := NewRunStore()
	store.active = true
	_, err := store.Start(context.Background(), t.TempDir(), "show", FormInput{}, &fakeRunner{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Start while busy = %v, want ErrConflict", err)
	}
	if len(store.runs) != 0 {
		t.Errorf("runs len after conflict = %d, want 0", len(store.runs))
	}
}
