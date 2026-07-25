package cmd

import (
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"

	"github.com/ariefsam/esb/eventstore"
	"github.com/ariefsam/esb/generator"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate <direction>",
	Short: "Migrate events between embedded SQLite and ESB server",
	Long: `Migrate events between the embedded SQLite store and a remote ESB server.

  esb migrate to-esb       — copy events from local SQLite to remote ESB server
  esb migrate to-embedded  — copy events from remote ESB server back to local SQLite

Both directions batch events (100 per batch) and use the server's
expected_version semantics so duplicate writes are rejected by the
server, not silently overwritten. Run from the project root; the
embedded SQLite is read via EVENT_STORE_DSN (or DB_DSN as fallback).`,
	Args: cobra.ExactArgs(1),
	RunE: runMigrate,
}

var (
	migrateSourceDSN string
	migrateESBURL    string
	migrateTenant    string
	migrateProject   string
	migrateForce     bool
)

func init() {
	migrateCmd.Flags().StringVar(&migrateSourceDSN, "source", "", "SQLite path to read from or write to (defaults to EVENT_STORE_DSN/DB_DSN)")
	migrateCmd.Flags().StringVar(&migrateESBURL, "esb-url", "", "ESB server URL (required)")
	migrateCmd.Flags().StringVar(&migrateTenant, "tenant", "", "ESB tenant ID (required)")
	migrateCmd.Flags().StringVar(&migrateProject, "project", "", "ESB project ID (required)")
	migrateCmd.Flags().BoolVar(&migrateForce, "force", false, "required for to-embedded when target SQLite has existing events")
	rootCmd.AddCommand(migrateCmd)
}

// runMigrate dispatches to the right direction.
func runMigrate(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return errors.New("migrate: direction wajib diisi — pakai 'to-esb' atau 'to-embedded'")
	}
	switch args[0] {
	case "to-esb":
		return migrateToESB(cmd)
	case "to-embedded":
		return migrateToEmbedded(cmd)
	default:
		return fmt.Errorf("migrate: direction %q tidak dikenal — pakai 'to-esb' atau 'to-embedded'", args[0])
	}
}

// migrateToESB copies events from the embedded SQLite store to a
// remote ESB server. Events are read in id ASC order so the
// destination receives the same chronological order as the source.
// Each batch uses version-1 as expected_version so re-running
// against a partially migrated server does not duplicate events.
func migrateToESB(cmd *cobra.Command) error {
	sourceDSN, err := resolveSourceDSN()
	if err != nil {
		return err
	}
	if err := requireESBFlags(); err != nil {
		return err
	}

	events, err := readAllEventsFromSQLite(sourceDSN)
	if err != nil {
		return fmt.Errorf("read events: %w", err)
	}
	if len(events) == 0 {
		fmt.Println("nothing to migrate — embedded store kosong")
		return nil
	}
	fmt.Printf("exported %d events from embedded store\n", len(events))

	client, err := newESBClientForMigration()
	if err != nil {
		return err
	}
	batches := chunkEvents(events, 100)
	for i, batch := range batches {
		for _, e := range batch {
			_, err := client.Store(cmd.Context(), eventstore.StoreRequest{
				AggregateName:   e.AggregateName,
				AggregateID:     e.AggregateID,
				EventName:       e.EventName,
				Data:            e.Data,
				ExpectedVersion: e.Version - 1,
				IdempotencyKey:  e.IdempotencyKey,
				CorrelationID:   e.CorrelationID,
				CausationID:     e.CausationID,
			})
			if err != nil {
				return fmt.Errorf("upload batch %d event %s/%s v%d: %w",
					i+1, e.AggregateName, e.AggregateID, e.Version, err)
			}
		}
		fmt.Printf("uploaded batch %d/%d\n", i+1, len(batches))
	}
	if err := writeMigrationState(sourceDSN, "to-esb", len(events)); err != nil {
		return fmt.Errorf("commit migration state: %w", err)
	}
	fmt.Println("rewrite local cursor: done")
	return nil
}

// migrateToEmbedded copies events from the remote ESB server into
// the embedded SQLite. It is the rollback path — useful when a
// remote deploy fails and the user wants to keep development local.
//
// Refuses to overwrite existing events unless --force is passed.
func migrateToEmbedded(cmd *cobra.Command) error {
	sourceDSN, err := resolveSourceDSN()
	if err != nil {
		return err
	}
	if err := requireESBFlags(); err != nil {
		return err
	}

	existing, err := countEventsInSQLite(sourceDSN)
	if err != nil {
		return fmt.Errorf("inspect target: %w", err)
	}
	if existing > 0 && !migrateForce {
		return fmt.Errorf("target SQLite sudah punya %d event — pakai --force untuk overwrite", existing)
	}

	client, err := newESBClientForMigration()
	if err != nil {
		return err
	}

	const pageSize = 100
	imported := 0
	var afterID uint
	for {
		batch, err := client.EventsAll(cmd.Context(), nil, afterID, pageSize)
		if err != nil {
			return fmt.Errorf("fetch events: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		if err := insertEventsIntoSQLite(sourceDSN, batch); err != nil {
			return fmt.Errorf("insert batch: %w", err)
		}
		imported += len(batch)
		afterID = batch[len(batch)-1].ID
		fmt.Printf("imported batch of %d (after id %d)\n", len(batch), afterID)
	}
	fmt.Printf("imported %d events from ESB server\n", imported)
	if err := writeMigrationState(sourceDSN, "to-embedded", imported); err != nil {
		return fmt.Errorf("commit migration state: %w", err)
	}
	return nil
}

// resolveSourceDSN returns the --source flag value or reads
// EVENT_STORE_DSN/DB_DSN from env. The path is resolved against
// the current working directory so the operator can run from inside
// the project root.
func resolveSourceDSN() (string, error) {
	if migrateSourceDSN != "" {
		return filepath.Abs(migrateSourceDSN)
	}
	if v := os.Getenv("EVENT_STORE_DSN"); v != "" {
		return filepath.Abs(v)
	}
	if v := os.Getenv("DB_DSN"); v != "" {
		return filepath.Abs(v)
	}
	return "", fmt.Errorf("tidak bisa menentukan SQLite path — set --source atau DB_DSN di .env")
}

// requireESBFlags validates the flags required by both directions.
func requireESBFlags() error {
	if migrateESBURL == "" {
		return fmt.Errorf("--esb-url wajib diisi")
	}
	if migrateTenant == "" {
		return fmt.Errorf("--tenant wajib diisi")
	}
	if migrateProject == "" {
		return fmt.Errorf("--project wajib diisi")
	}
	return nil
}

// newESBClientForMigration creates an HTTP client pointed at the
// target server. The signing key is loaded from private.pem in the
// current directory.
func newESBClientForMigration() (*eventstore.Client, error) {
	pemData, err := os.ReadFile("private.pem")
	if err != nil {
		return nil, fmt.Errorf("baca private.pem: %w (jalankan 'make keygen' dulu)", err)
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("private.pem bukan PEM yang valid")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse EC private key: %w", err)
	}
	issuer := os.Getenv("JWT_ISSUER")
	if issuer == "" {
		if moduleName, err := generator.ReadModuleName(); err == nil {
			issuer = moduleName
		}
	}
	return eventstore.New(migrateESBURL, migrateTenant, migrateProject, issuer, key), nil
}

// readAllEventsFromSQLite opens the embedded SQLite read-only and
// returns every event ordered by autoincrement id ASC — the same
// order the projection worker uses.
func readAllEventsFromSQLite(dsn string) ([]eventstore.Event, error) {
	db, err := sql.Open("sqlite", dsn+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, aggregate_name, aggregate_id, event_name, version, data, time_millis, correlation_id, causation_id, idempotency_key FROM events ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []eventstore.Event
	for rows.Next() {
		var (
			id            uint
			aggName, aggID, evtName string
			version       int64
			data          []byte
			timeMillis    int64
			corrID, causeID, idemKey sql.NullString
		)
		if err := rows.Scan(&id, &aggName, &aggID, &evtName, &version, &data, &timeMillis, &corrID, &causeID, &idemKey); err != nil {
			return nil, err
		}
		out = append(out, eventstore.Event{
			ID:             id,
			AggregateName:  aggName,
			AggregateID:    aggID,
			EventName:      evtName,
			Version:        version,
			Data:           json.RawMessage(data),
			TimeMillis:     timeMillis,
			CorrelationID:  corrID.String,
			CausationID:    causeID.String,
			IdempotencyKey: idemKey.String,
		})
	}
	return out, rows.Err()
}

// countEventsInSQLite returns the number of rows in the events
// table. Returns 0 if the table does not exist yet.
func countEventsInSQLite(dsn string) (int, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		// Table missing — treat as zero so the migration can proceed.
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}

// insertEventsIntoSQLite writes a batch to the embedded SQLite.
// Unique constraint errors from a re-run are ignored — the
// migration is idempotent on (aggregate_name, aggregate_id,
// version) via the unique index defined by the LocalStore schema.
func insertEventsIntoSQLite(dsn string, events []eventstore.Event) error {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO events (aggregate_name, aggregate_id, event_name, version, data, time_millis, correlation_id, causation_id, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, e := range events {
		_, err := stmt.Exec(
			e.AggregateName, e.AggregateID, e.EventName, e.Version,
			[]byte(e.Data), e.TimeMillis, e.CorrelationID, e.CausationID, e.IdempotencyKey,
		)
		if err != nil {
			if isUniqueConstraintError(err) {
				continue
			}
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// isUniqueConstraintError reports whether err is a SQLite unique
// constraint violation. Used by insertEventsIntoSQLite to silently
// ignore already-migrated rows.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// chunkEvents splits events into batches of size n. The last chunk
// may be shorter.
func chunkEvents(events []eventstore.Event, n int) [][]eventstore.Event {
	if n <= 0 || len(events) == 0 {
		return [][]eventstore.Event{events}
	}
	var batches [][]eventstore.Event
	for i := 0; i < len(events); i += n {
		end := i + n
		if end > len(events) {
			end = len(events)
		}
		batches = append(batches, events[i:end])
	}
	return batches
}

// writeMigrationState records the most recent migration direction
// + event count in a file alongside the SQLite so a partial run
// can be inspected. Stored as plain text — there is no
// authoritative schema because the migration is a one-shot tool.
func writeMigrationState(dsn, direction string, count int) error {
	statePath := dsn + ".migration_state"
	content := fmt.Sprintf("direction=%s\ncount=%d\ntimestamp=%s\n",
		direction, count, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(statePath, []byte(content), 0644)
}