package eventstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrationDirection_ParsesKnownValues locks in the parser
// output for the two directions the UI surfaces. Without this
// guard a future refactor could break the storage page silently
// when it falls back to "" (unknown).
func TestMigrationDirection_ParsesKnownValues(t *testing.T) {
	cases := []struct {
		name  string
		state string
		want  string
	}{
		{"to-esb", "direction=to-esb\ncount=10\ntimestamp=2026-07-25T00:00:00Z\n", "to-esb"},
		{"to-embedded", "direction=to-embedded\ncount=4\ntimestamp=2026-07-25T00:00:00Z\n", "to-embedded"},
		{"empty", "", ""},
		{"missing key", "count=10\ntimestamp=2026-07-25T00:00:00Z\n", ""},
		{"blank line only", "\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MigrationDirection(tc.state)
			if got != tc.want {
				t.Errorf("MigrationDirection(%q) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestMigrationEventCount_ParsesCounter ensures the count parser
// extracts the integer verbatim, falls back to 0 on bad input, and
// returns 0 when the line is missing. The UI uses this to show
// "N events migrated" without re-opening the SQLite.
func TestMigrationEventCount_ParsesCounter(t *testing.T) {
	cases := []struct {
		name  string
		state string
		want  int
	}{
		{"zero", "direction=to-esb\ncount=0\ntimestamp=2026-07-25T00:00:00Z\n", 0},
		{"positive", "direction=to-esb\ncount=42\ntimestamp=2026-07-25T00:00:00Z\n", 42},
		{"large", "direction=to-esb\ncount=1000000\ntimestamp=2026-07-25T00:00:00Z\n", 1000000},
		{"missing line", "direction=to-esb\ntimestamp=2026-07-25T00:00:00Z\n", 0},
		{"garbage", "direction=to-esb\ncount=not-a-number\ntimestamp=2026-07-25T00:00:00Z\n", 0},
		{"empty", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MigrationEventCount(tc.state)
			if got != tc.want {
				t.Errorf("MigrationEventCount(%q) = %d, want %d", tc.state, got, tc.want)
			}
		})
	}
}

// TestReadMigrationState_MissingFileReturnsEmpty covers the absent
// state file path. The control flow lets the UI render "no
// migration yet" without an error.
func TestReadMigrationState_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	got, err := ReadMigrationState(dsn)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty state, got %q", got)
	}
}

// TestReadMigrationState_RoundTripsWrittenFile confirms the
// read/write pair stays consistent — the migrate command writes
// the state file and the UI's storage page reads it back.
func TestReadMigrationState_RoundTripsWrittenFile(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	statePath := dsn + ".migration_state"
	contents := "direction=to-esb\ncount=7\ntimestamp=2026-07-25T00:00:00Z\n"
	if err := os.WriteFile(statePath, []byte(contents), 0644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	got, err := ReadMigrationState(dsn)
	if err != nil {
		t.Fatalf("ReadMigrationState: %v", err)
	}
	if !strings.Contains(got, "direction=to-esb") {
		t.Errorf("expected direction=to-esb in output, got %q", got)
	}
	if !strings.Contains(got, "count=7") {
		t.Errorf("expected count=7 in output, got %q", got)
	}
}
