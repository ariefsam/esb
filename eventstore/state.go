package eventstore

import (
	"os"
	"strconv"
	"strings"
)

// Migration state is recorded in a plain-text file alongside the
// SQLite so the UI can show "last migration direction: to-esb"
// without re-querying the server. The format is intentionally
// simple — there is no authoritative schema because migration is a
// one-shot tool.

// ReadMigrationState returns the recorded migration state for a
// SQLite path, or "" when no run has been recorded.
func ReadMigrationState(dsn string) (string, error) {
	data, err := os.ReadFile(dsn + ".migration_state")
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// MigrationDirection parses the recorded state file into the most
// recent direction token ("to-esb" / "to-embedded" / "").
func MigrationDirection(state string) string {
	for _, line := range strings.Split(state, "\n") {
		if strings.HasPrefix(line, "direction=") {
			return strings.TrimPrefix(line, "direction=")
		}
	}
	return ""
}

// MigrationEventCount parses the count line from state. Returns 0
// when the line is missing or unparseable.
func MigrationEventCount(state string) int {
	for _, line := range strings.Split(state, "\n") {
		if strings.HasPrefix(line, "count=") {
			n, _ := strconv.Atoi(strings.TrimPrefix(line, "count="))
			return n
		}
	}
	return 0
}