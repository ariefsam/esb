package inspector

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

// TestScanStorage_DefaultsToEmbeddedWhenEnvMissing verifies the
// inspector treats a project without a .env as embedded mode and
// reports no SQLite file rather than crashing.
func TestScanStorage_DefaultsToEmbeddedWhenEnvMissing(t *testing.T) {
	dir := t.TempDir()
	info := ScanStorage(dir)
	if info.Mode != StorageModeEmbedded {
		t.Errorf("Mode = %q, want embedded", info.Mode)
	}
	if info.HasSQLite {
		t.Errorf("HasSQLite = true, want false for missing DSN")
	}
}

// TestScanStorage_ReadsEnvAndResolvesDSN asserts .env values are
// picked up and a relative DSN is resolved against the project root
// so the UI can render an absolute path.
func TestScanStorage_ReadsEnvAndResolvesDSN(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`
EVENT_STORE_MODE=embedded
EVENT_STORE_DSN=app.db
ESB_URL=http://localhost:9999
`), 0644); err != nil {
		t.Fatal(err)
	}
	info := ScanStorage(dir)
	if info.Mode != StorageModeEmbedded {
		t.Errorf("Mode = %q, want embedded", info.Mode)
	}
	wantDSN := filepath.Join(dir, "app.db")
	if info.DSN != wantDSN {
		t.Errorf("DSN = %q, want %q", info.DSN, wantDSN)
	}
}

// TestScanStorage_EmbeddedDBCountsByAggregate seeds a SQLite file
// in the project directory and verifies ScanStorage reports
// count(*) per aggregate_name from the events table.
func TestScanStorage_EmbeddedDBCountsByAggregate(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := seedEventsDB(t, dsn, []seedEvent{
		{AggregateName: "order", Count: 3},
		{AggregateName: "user", Count: 2},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=embedded\nEVENT_STORE_DSN="+dsn+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	info := ScanStorage(dir)
	if info.Mode != StorageModeEmbedded {
		t.Errorf("Mode = %q, want embedded", info.Mode)
	}
	if !info.HasSQLite {
		t.Errorf("HasSQLite = false, want true after seeding")
	}
	if got := info.Counts["order"]; got != 3 {
		t.Errorf("Counts[order] = %d, want 3", got)
	}
	if got := info.Counts["user"]; got != 2 {
		t.Errorf("Counts[user] = %d, want 2", got)
	}
	if total := info.TotalEvents(); total != 5 {
		t.Errorf("TotalEvents = %d, want 5", total)
	}
	if got := info.SortedAggregateNames(); !sortedEqual(got, []string{"order", "user"}) {
		t.Errorf("SortedAggregateNames = %v, want [order user]", got)
	}
}

// TestScanStorage_ESBServerMode reads ESB_URL out of .env without
// attempting to open any SQLite file.
func TestScanStorage_ESBServerMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=esb-server\nESB_URL=http://esb.internal:8080\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	info := ScanStorage(dir)
	if info.Mode != StorageModeESBServer {
		t.Errorf("Mode = %q, want esb-server", info.Mode)
	}
	if info.ESBURL != "http://esb.internal:8080" {
		t.Errorf("ESBURL = %q", info.ESBURL)
	}
	if info.HasSQLite {
		t.Errorf("HasSQLite = true, want false for esb-server mode")
	}
}

// TestScanStorage_MissingDSNDoesNotCrash guards the regression where
// .env lists EVENT_STORE_MODE=embedded but EVENT_STORE_DSN is empty
// — the inspector must still return a sane struct, not panic on
// sql.Open of an empty path.
func TestScanStorage_MissingDSNDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=embedded\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	info := ScanStorage(dir)
	if info.Mode != StorageModeEmbedded {
		t.Errorf("Mode = %q, want embedded", info.Mode)
	}
	if info.HasSQLite {
		t.Errorf("HasSQLite = true, want false when DSN is missing")
	}
}

// TestScanStorage_DSNAliasToDB verifies that EVENT_STORE_DSN falls
// back to DB_DSN when only DB_DSN is set — the generated .env
// uses DB_DSN as the projection store, and the embedded event store
// may share it.
func TestScanStorage_DSNAliasToDB(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"DB_DSN=shared.db\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	info := ScanStorage(dir)
	want := filepath.Join(dir, "shared.db")
	if info.DSN != want {
		t.Errorf("DSN = %q, want %q (from DB_DSN alias)", info.DSN, want)
	}
}

// TestScanStorage_AcceptsAliasModes checks the legacy spellings
// (local, remote, sqlite, esb) normalize to the canonical mode
// names so a typo in .env does not silently push the app to the
// wrong code path. Unknown spellings must surface as "unknown"
// so the UI can warn the operator.
func TestScanStorage_AcceptsAliasModes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"local", StorageModeEmbedded},
		{"sqlite", StorageModeEmbedded},
		{"EMBEDDED", StorageModeEmbedded},
		{"esb", StorageModeESBServer},
		{"server", StorageModeESBServer},
		{"REMOTE", StorageModeESBServer},
		{"unknown-mode", StorageModeUnknown}, // typo surfaces as warning
		{"esb-sever", StorageModeUnknown},    // common typo for esb-server
	}
	for _, tc := range cases {
		if got := normalizeStorageMode(tc.in); got != tc.want {
			t.Errorf("normalizeStorageMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestScanStorage_ReportsUnknownDBFile covers the case where DSN
// points at a file that exists but is not a SQLite database. The
// inspector must NOT panic; HasSQLite stays false.
func TestScanStorage_ReportsUnknownDBFile(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(dsn, []byte("not a sqlite db"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=embedded\nEVENT_STORE_DSN="+dsn+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	info := ScanStorage(dir)
	if info.HasSQLite {
		t.Errorf("HasSQLite = true, want false for non-SQLite file")
	}
	if info.Counts["order"] != 0 {
		t.Errorf("expected zero counts on corrupt DB")
	}
}

type seedEvent struct {
	AggregateName string
	Count         int
}

// seedEventsDB creates a minimal SQLite file with an events table
// matching the shape LocalStore will eventually write. The events
// table is intentionally minimal — a few required columns only,
// because ScanStorage just GROUPs BY aggregate_name.
func seedEventsDB(t *testing.T, dsn string, events []seedEvent) error {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE events (
		aggregate_name TEXT NOT NULL,
		aggregate_id TEXT NOT NULL,
		event_name TEXT NOT NULL,
		version INTEGER NOT NULL,
		data BLOB
	)`); err != nil {
		return err
	}
	for _, e := range events {
		for i := 0; i < e.Count; i++ {
			if _, err := db.Exec(
				`INSERT INTO events(aggregate_name, aggregate_id, event_name, version, data)
				 VALUES (?, ?, ?, ?, ?)`,
				e.AggregateName, "id-1", "TestEvent", int64(i+1), []byte("{}"),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}