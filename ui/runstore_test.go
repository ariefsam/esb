package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunStore_CapEvictsOldestCompleted locks in the bounded-history
// behavior: once the in-memory store reaches runStoreCap, Start evicts
// the oldest *completed* run to make room instead of permanently
// rejecting new runs. A long-lived `esb ui` session stays usable past
// runStoreCap executions while memory remains bounded.
func TestRunStore_CapEvictsOldestCompleted(t *testing.T) {
	store := NewRunStore()
	base := time.Now()
	// Seed exactly runStoreCap already-finished runs with strictly
	// increasing StartedAt so the oldest is deterministic (index 0).
	for i := 0; i < runStoreCap; i++ {
		id := newRunID(uint64(i))
		store.runs[id] = &Run{
			ID:         id,
			CommandID:  "show",
			Argv:       []string{"esb", "show"},
			Dir:        t.TempDir(),
			StartedAt:  base.Add(time.Duration(i) * time.Millisecond),
			FinishedAt: base.Add(time.Duration(i) * time.Millisecond),
			Status:     RunSucceed,
			ExitCode:   0,
		}
	}
	oldestID := newRunID(0)
	// Give the next run a fresh id so it can't collide with a seeded one.
	store.nextID = uint64(runStoreCap)

	run, err := store.Start(context.Background(), t.TempDir(), "show", FormInput{}, &fakeRunner{})
	if err != nil {
		t.Fatalf("Start at cap = %v, want nil (eviction should make room)", err)
	}
	if got := len(store.runs); got != runStoreCap {
		t.Errorf("runs len after eviction = %d, want %d", got, runStoreCap)
	}
	if _, stillThere := store.runs[oldestID]; stillThere {
		t.Errorf("oldest run %q was not evicted", oldestID)
	}
	if _, ok := store.runs[run.ID]; !ok {
		t.Errorf("new run %q not stored", run.ID)
	}
	if got := store.Cap(); got != 1000 {
		t.Errorf("Cap() = %d, want 1000", got)
	}
}

// TestRunStore_CapFullOfRunningReturnsErrStoreFull covers the defensive
// fallback: if every slot holds an in-flight run (impossible under the
// single-active invariant, but guarded anyway), Start must refuse rather
// than grow unbounded or evict a run that is actively streaming.
func TestRunStore_CapFullOfRunningReturnsErrStoreFull(t *testing.T) {
	store := NewRunStore()
	for i := 0; i < runStoreCap; i++ {
		id := newRunID(uint64(i))
		store.runs[id] = &Run{ID: id, Status: RunRunning, StartedAt: time.Now()}
	}
	// active is false so the cap check (not the busy check) is exercised.
	_, err := store.Start(context.Background(), t.TempDir(), "show", FormInput{}, &fakeRunner{})
	if !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Start at cap-of-running = %v, want ErrStoreFull", err)
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
