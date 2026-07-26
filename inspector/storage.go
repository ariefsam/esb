package inspector

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// StorageMode values the inspector recognizes in EVENT_STORE_MODE.
const (
	StorageModeEmbedded  = "embedded"
	StorageModeESBServer = "esb-server"
	// StorageModeUnknown marks an unrecognised EVENT_STORE_MODE value.
	// The UI displays it as a warning so a typo (e.g. "esb-sever")
	// is not silently treated as embedded.
	StorageModeUnknown = "unknown"
)

// StorageInfo describes how the project's event store is currently
// configured. It is read-only — the inspector never mutates the
// project. Embedded mode points at a local SQLite file; esb-server
// mode points at a remote HTTP endpoint.
type StorageInfo struct {
	// Mode is "embedded" or "esb-server". When the .env has no
	// EVENT_STORE_MODE entry the inspector defaults to "embedded"
	// because that is what `esb init` generates today.
	Mode string
	// DSN is the SQLite path used by the embedded event store, or
	// empty when mode == esb-server. The path is resolved against
	// the project root so relative DSNs ("app.db") display as
	// "<root>/app.db" in the UI.
	DSN string
	// ESBURL is the remote endpoint when mode == esb-server, or
	// empty otherwise.
	ESBURL string
	// Counts maps aggregate_name -> event row count, populated from
	// the embedded SQLite file when mode == embedded and the file
	// exists. Counts is nil otherwise.
	Counts map[string]int
	// HasSQLite reports whether the embedded SQLite file was
	// opened successfully. When false, Counts is nil even if the
	// DSN points somewhere — a useful signal for the UI so the
	// page can render "no events yet" instead of pretending the
	// table exists.
	HasSQLite bool
}

// Storage exposes the storage mode + per-aggregate event counts for
// the UI. It is populated by ScanStorage and attached to the
// ProjectModel by Scan.
type Storage struct {
	Info StorageInfo
}

// ScanStorage inspects rootDir for the project's event store
// configuration. It is tolerant: a missing .env, an unparseable
// EVENT_STORE_MODE, or a missing SQLite file all surface as a
// zero-value StorageInfo with sensible defaults rather than errors.
//
// The function is intentionally cheap — it runs on every UI page
// load. SQLite is opened read-only with no busy timeout; a corrupt
// file surfaces as HasSQLite=false without failing the rest of the
// scan.
func ScanStorage(rootDir string) StorageInfo {
	info := StorageInfo{
		Mode:   StorageModeEmbedded,
		Counts: map[string]int{},
	}

	envPath := filepath.Join(rootDir, ".env")
	env, err := readEnvFile(envPath)
	if err == nil {
		if mode, ok := env["EVENT_STORE_MODE"]; ok && mode != "" {
			info.Mode = normalizeStorageMode(mode)
		}
		switch info.Mode {
		case StorageModeEmbedded:
			dsn := env["EVENT_STORE_DSN"]
			if dsn == "" {
				dsn = env["DB_DSN"]
			}
			if dsn != "" {
				if filepath.IsAbs(dsn) {
					info.DSN = dsn
				} else {
					info.DSN = filepath.Join(rootDir, dsn)
				}
			}
		case StorageModeESBServer:
			info.ESBURL = env["ESB_URL"]
		}
	}

	if info.Mode == StorageModeEmbedded && info.DSN != "" {
		counts, ok := scanSQLiteCounts(info.DSN)
		if ok {
			info.Counts = counts
			info.HasSQLite = true
		}
	}
	return info
}

// readEnvFile parses KEY=VALUE entries from path. Quoted values are
// unquoted. Lines that start with '#' and blank lines are skipped.
// A missing file returns a nil error and an empty map so callers
// can treat absent .env as "no overrides".
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq == -1 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		out[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeStorageMode maps any spelling the user might write into
// the canonical mode token. Recognised aliases ("local", "sqlite",
// "esb", "remote", "server") collapse to their canonical form so
// `esb show` stays stable across small spelling variants. Truly
// unknown values fall back to StorageModeUnknown so a typo
// ("esb-sever") surfaces in the UI as a warning rather than being
// silently treated as embedded — that is exactly the bug where a
// production deploy would have its event history split between
// SQLite and the ESB server.
func normalizeStorageMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case StorageModeEmbedded, "local", "sqlite":
		return StorageModeEmbedded
	case StorageModeESBServer, "esb", "remote", "server":
		return StorageModeESBServer
	}
	return StorageModeUnknown
}

// scanSQLiteCounts opens dsn in read-only mode and returns
// count(*) per aggregate_name from the events table. The function
// returns ok=false when the file is missing, corrupt, or has no
// events table — the inspector never treats this as fatal.
func scanSQLiteCounts(dsn string) (map[string]int, bool) {
	// file::...?mode=ro opens the database read-only. _pragma=query
	// turns off the journal — harmless for read-only access and
	// ensures the file is not locked when another process (the
	// running service) has it open for writing.
	dsnRO := dsn + "?mode=ro"
	if strings.Contains(dsn, "?") {
		dsnRO = dsn + "&mode=ro"
	}
	db, err := sql.Open("sqlite", dsnRO)
	if err != nil {
		return nil, false
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return nil, false
	}

	rows, err := db.Query("SELECT aggregate_name, COUNT(*) FROM events GROUP BY aggregate_name")
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, false
		}
		out[name] = count
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	return out, true
}

// SortedAggregateNames returns the aggregate names from the count
// map in lexical order. The UI uses it to render a stable table
// without depending on Go's randomized map iteration.
func (s StorageInfo) SortedAggregateNames() []string {
	if len(s.Counts) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.Counts))
	for name := range s.Counts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TotalEvents sums Counts into a single number for the dashboard
// card. Returns 0 when Counts is empty.
func (s StorageInfo) TotalEvents() int {
	total := 0
	for _, c := range s.Counts {
		total += c
	}
	return total
}

// String renders a one-line summary suitable for the inspector's
// CLI output. Keep it stable — the snapshot golden file pins the
// exact wording.
func (s StorageInfo) String() string {
	switch s.Mode {
	case StorageModeESBServer:
		if s.ESBURL == "" {
			return fmt.Sprintf("event store: %s (ESB_URL belum di-set)", s.Mode)
		}
		return fmt.Sprintf("event store: %s -> %s", s.Mode, s.ESBURL)
	default:
		if s.DSN == "" {
			return fmt.Sprintf("event store: %s (DSN belum di-set)", s.Mode)
		}
		if !s.HasSQLite {
			return fmt.Sprintf("event store: %s -> %s (file belum ada)", s.Mode, s.DSN)
		}
		return fmt.Sprintf("event store: %s -> %s, %d events across %d aggregates",
			s.Mode, s.DSN, s.TotalEvents(), len(s.Counts))
	}
}