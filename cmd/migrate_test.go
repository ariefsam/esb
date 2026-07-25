package cmd

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ariefsam/esb/eventstore"
)

// TestMigrate_RequiresDirection asserts that the migrate command
// refuses to run without a direction argument. Without this guard,
// a typo silently sends the operator to the unknown-direction
// branch — covered here so the error wording stays stable.
func TestMigrate_RequiresDirection(t *testing.T) {
	err := runMigrate(migrateCmd, []string{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "direction") {
		t.Errorf("error = %q, want it to mention direction", err)
	}
}

// TestMigrate_RejectsUnknownDirection locks in the bad-input path
// so a future refactor cannot silently accept a typo'd direction.
func TestMigrate_RejectsUnknownDirection(t *testing.T) {
	err := runMigrate(migrateCmd, []string{"to-nowhere"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tidak dikenal") {
		t.Errorf("error = %q, want unknown-direction message", err)
	}
}

// TestMigrate_ResolvesSourceDSNFromEnv checks that EVENT_STORE_DSN
// and DB_DSN are honoured and resolveSourceDSN returns an absolute
// path so the runner can open the SQLite from any cwd.
func TestMigrate_ResolvesSourceDSNFromEnv(t *testing.T) {
	dir := t.TempDir()
	_, restore := setCwd(t, dir)
	defer restore()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENT_STORE_DSN", "")
	t.Setenv("DB_DSN", "app.db")
	got, err := resolveSourceDSN()
	if err != nil {
		t.Fatalf("resolveSourceDSN: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("DSN = %q, want absolute path", got)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+"app.db") {
		t.Errorf("DSN = %q, want suffix app.db", got)
	}
}

// TestMigrate_ResolvesSourceDSNMissing verifies the error message
// when neither --source nor env vars are set.
func TestMigrate_ResolvesSourceDSNMissing(t *testing.T) {
	dir := t.TempDir()
	_, restore := setCwd(t, dir)
	defer restore()

	t.Setenv("EVENT_STORE_DSN", "")
	t.Setenv("DB_DSN", "")
	_, err := resolveSourceDSN()
	if err == nil {
		t.Fatal("expected error when no DSN is configured")
	}
}

// TestMigrate_RequiresESBFlags asserts all three flags must be set
// before the runner attempts to talk to the server.
func TestMigrate_RequiresESBFlags(t *testing.T) {
	cases := []struct {
		name string
		fn   func() error
		want string
	}{
		{"esb-url missing", func() error { migrateESBURL = ""; migrateTenant = "t"; migrateProject = "p"; return requireESBFlags() }, "--esb-url"},
		{"tenant missing", func() error { migrateESBURL = "http://x"; migrateTenant = ""; migrateProject = "p"; return requireESBFlags() }, "--tenant"},
		{"project missing", func() error { migrateESBURL = "http://x"; migrateTenant = "t"; migrateProject = ""; return requireESBFlags() }, "--project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// TestMigrate_ChunkEvents verifies the chunker splits an event
// list into batches of n and the final batch can be shorter.
func TestMigrate_ChunkEvents(t *testing.T) {
	events := make([]eventstore.Event, 7)
	for i := range events {
		events[i] = eventstore.Event{ID: uint(i + 1)}
	}
	batches := chunkEvents(events, 3)
	if len(batches) != 3 {
		t.Fatalf("chunks = %d, want 3", len(batches))
	}
	if len(batches[0]) != 3 || len(batches[1]) != 3 || len(batches[2]) != 1 {
		t.Errorf("chunk sizes = [%d,%d,%d], want [3,3,1]",
			len(batches[0]), len(batches[1]), len(batches[2]))
	}
}

// TestMigrate_ChunkEventsEmptyReturnsOriginal covers the no-data
// edge case so the runner does not crash on an empty store.
func TestMigrate_ChunkEventsEmptyReturnsOriginal(t *testing.T) {
	got := chunkEvents(nil, 5)
	if len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("chunkEvents(nil) = %v, want [[]]", got)
	}
}

// TestMigrate_ReadAllEventsFromSQLite exercises the read path
// against a seeded SQLite fixture so the field mapping is locked
// in: id is returned, ordering is id ASC, JSON data is preserved.
func TestMigrate_ReadAllEventsFromSQLite(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := seedMigrateFixture(dsn); err != nil {
		t.Fatalf("seed: %v", err)
	}
	events, err := readAllEventsFromSQLite(dsn)
	if err != nil {
		t.Fatalf("readAllEventsFromSQLite: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len = %d, want 3", len(events))
	}
	if events[0].ID >= events[1].ID || events[1].ID >= events[2].ID {
		t.Errorf("events not id ASC: %d,%d,%d", events[0].ID, events[1].ID, events[2].ID)
	}
	if events[0].AggregateName != "order" {
		t.Errorf("aggregate_name = %q, want order", events[0].AggregateName)
	}
}

// TestMigrate_InsertEventsIntoSQLite asserts the write path
// persists events and a re-insert is silently ignored thanks to
// the unique index (idempotent migration).
func TestMigrate_InsertEventsIntoSQLite(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := seedMigrateFixture(dsn); err != nil {
		t.Fatalf("seed: %v", err)
	}
	events, err := readAllEventsFromSQLite(dsn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Re-insert the same batch — should be a no-op thanks to
	// isUniqueConstraintError handling.
	if err := insertEventsIntoSQLite(dsn, events); err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	count, err := countEventsInSQLite(dsn)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("count after re-insert = %d, want 3", count)
	}
}

// TestMigrate_InsertIsIdempotent locks in the resumable import:
// re-inserting the same batch must not fail (the unique index +
// ON CONFLICT DO NOTHING path) and must not duplicate rows. This
// is what allows a partial import to be retried safely.
func TestMigrate_InsertIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := ensureSchema(dsn); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	batch := []eventstore.Event{
		{AggregateName: "order", AggregateID: "a", EventName: "Placed", Version: 1, Data: json.RawMessage(`{"k":"v"}`)},
		{AggregateName: "order", AggregateID: "a", EventName: "Shipped", Version: 2, Data: json.RawMessage(`{"k":"v"}`)},
	}
	if err := insertEventsIntoSQLite(dsn, batch); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Re-insert identical batch — must be a silent no-op.
	if err := insertEventsIntoSQLite(dsn, batch); err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	count, err := countEventsInSQLite(dsn)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("count after idempotent re-insert = %d, want 2", count)
	}
}

// TestMigrate_WriteAndReadMigrationState guards the on-disk state
// file format — the inspector parses it for the storage page.
func TestMigrate_WriteAndReadMigrationState(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := writeMigrationState(dsn, "to-esb", 42); err != nil {
		t.Fatal(err)
	}
	state, err := eventstore.ReadMigrationState(dsn)
	if err != nil {
		t.Fatalf("ReadMigrationState: %v", err)
	}
	if eventstore.MigrationDirection(state) != "to-esb" {
		t.Errorf("direction = %q, want to-esb", eventstore.MigrationDirection(state))
	}
	if eventstore.MigrationEventCount(state) != 42 {
		t.Errorf("count = %d, want 42", eventstore.MigrationEventCount(state))
	}
}

// TestMigrate_ReadMigrationStateMissingReturnsEmpty keeps the
// "no prior migration" path quiet — the UI relies on "" meaning
// "no state yet".
func TestMigrate_ReadMigrationStateMissingReturnsEmpty(t *testing.T) {
	state, err := eventstore.ReadMigrationState(filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	if state != "" {
		t.Errorf("state = %q, want empty", state)
	}
}

// TestMigrate_CountMissingTableReturnsZero exercises the fallback
// for a fresh DB without the events table — the runner must treat
// that as "0 events" rather than crashing.
func TestMigrate_CountMissingTableReturnsZero(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "fresh.db")
	// Touch the file so sqlite can open it, but never run migrations.
	f, err := os.Create(dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	count, err := countEventsInSQLite(dsn)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

// TestMigrate_RootCmdRegistersSubcommand guards the registration
// so a future refactor of init() cannot silently drop the migrate
// subcommand from the CLI surface.
func TestMigrate_RootCmdRegistersSubcommand(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "migrate" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("migrate subcommand not registered on rootCmd")
	}
}

// TestMigrate_EnsureSchemaCreatesTable is the regression test for
// "migrate to-embedded against a fresh database". Before the fix
// the first batch INSERT failed with "no such table: events".
func TestMigrate_EnsureSchemaCreatesTable(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "fresh.db")
	if _, err := os.Create(dsn); err != nil {
		t.Fatal(err)
	}
	if err := ensureSchema(dsn); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	// Insert must now succeed — the table exists.
	if err := insertEventsIntoSQLite(dsn, []eventstore.Event{
		{AggregateName: "order", AggregateID: "a", EventName: "Placed", Version: 1, Data: json.RawMessage(`{"k":"v"}`)},
	}); err != nil {
		t.Fatalf("insert after ensureSchema: %v", err)
	}
}

// TestMigrate_EnsureSchemaIdempotent guards the "CREATE TABLE IF
// NOT EXISTS" semantics so calling ensureSchema twice in a row
// does not error and does not lose data.
func TestMigrate_EnsureSchemaIdempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := ensureSchema(dsn); err != nil {
		t.Fatalf("ensureSchema (first): %v", err)
	}
	if err := insertEventsIntoSQLite(dsn, []eventstore.Event{
		{AggregateName: "order", AggregateID: "a", EventName: "Placed", Version: 1, Data: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Second call must not drop the row.
	if err := ensureSchema(dsn); err != nil {
		t.Fatalf("ensureSchema (second): %v", err)
	}
	count, err := countEventsInSQLite(dsn)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

// TestMigrate_TruncateEventsClearsAll locks in the --force
// overwrite semantics. Without this, a re-run against a non-empty
// SQLite would silently merge local history with the server's
// history rather than replace it.
func TestMigrate_TruncateEventsClearsAll(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := seedMigrateFixture(dsn); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pre, err := countEventsInSQLite(dsn)
	if err != nil {
		t.Fatalf("count pre: %v", err)
	}
	if pre == 0 {
		t.Fatal("seed produced empty table")
	}
	if err := truncateEvents(dsn); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	post, err := countEventsInSQLite(dsn)
	if err != nil {
		t.Fatalf("count post: %v", err)
	}
	if post != 0 {
		t.Errorf("count after truncate = %d, want 0", post)
	}
}

// --- helpers ---

type stringError string

func (e stringError) Error() string { return string(e) }

var (
	errUnique = stringError("constraint failed: UNIQUE constraint failed: events.aggregate_name")
	errSome   = stringError("database is locked")
)

func seedMigrateFixture(dsn string) error {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		aggregate_name TEXT NOT NULL,
		aggregate_id TEXT NOT NULL,
		event_name TEXT NOT NULL,
		version INTEGER NOT NULL,
		data BLOB,
		time_millis INTEGER NOT NULL DEFAULT 0,
		correlation_id TEXT,
		causation_id TEXT,
		idempotency_key TEXT,
		UNIQUE(aggregate_name, aggregate_id, version)
	)`); err != nil {
		return err
	}
	for i, name := range []string{"order", "order", "user"} {
		if _, err := db.Exec(
			`INSERT INTO events(aggregate_name, aggregate_id, event_name, version, data)
			 VALUES (?, ?, ?, ?, ?)`,
			name, "id-"+string(rune('1'+i)), "TestEvent", int64(i+1), []byte(`{"k":"v"}`),
		); err != nil {
			return err
		}
	}
	return nil
}

// setCwd swaps the process working directory for the duration of
// the test and restores the previous value via t.Cleanup.
func setCwd(t *testing.T, dir string) (string, func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return prev, func() { _ = os.Chdir(prev) }
}