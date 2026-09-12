package cmd

import (
	"strings"
	"testing"

	"github.com/ariefsam/esb/inspector"
)

// TestDeleteEventWarning_SafeWhenVerifiedZero mirrors the UI's RequireAck
// rule: a verified zero count is the only case that proceeds silently.
func TestDeleteEventWarning_SafeWhenVerifiedZero(t *testing.T) {
	storage := inspector.StorageInfo{
		Mode:        inspector.StorageModeEmbedded,
		HasSQLite:   true,
		EventCounts: map[string]map[string]int{},
	}
	warning, requireAck := deleteEventWarning(storage, "order", "OrderPlaced")
	if requireAck {
		t.Errorf("requireAck = true for a verified zero count, want false (warning: %q)", warning)
	}
}

// TestDeleteEventWarning_WarnsWhenStoredRowsExist covers the case the CLI
// used to skip entirely: stored events of this type already exist.
func TestDeleteEventWarning_WarnsWhenStoredRowsExist(t *testing.T) {
	storage := inspector.StorageInfo{
		Mode:      inspector.StorageModeEmbedded,
		HasSQLite: true,
		EventCounts: map[string]map[string]int{
			"order": {"OrderPlaced": 3},
		},
	}
	warning, requireAck := deleteEventWarning(storage, "order", "OrderPlaced")
	if !requireAck {
		t.Fatal("requireAck = false, want true when stored rows exist")
	}
	if !strings.Contains(warning, "3 stored") {
		t.Errorf("warning = %q, want it to mention the count", warning)
	}
}

// TestDeleteEventWarning_WarnsWhenUnverifiable covers esb-server mode and
// the no-SQLite-file case — both must warn rather than silently assume safe.
func TestDeleteEventWarning_WarnsWhenUnverifiable(t *testing.T) {
	cases := []struct {
		name    string
		storage inspector.StorageInfo
	}{
		{"esb-server mode", inspector.StorageInfo{Mode: inspector.StorageModeESBServer}},
		{"no sqlite file", inspector.StorageInfo{Mode: inspector.StorageModeEmbedded, HasSQLite: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warning, requireAck := deleteEventWarning(tc.storage, "order", "OrderPlaced")
			if !requireAck {
				t.Fatal("requireAck = false, want true when the count cannot be verified")
			}
			if !strings.Contains(warning, "cannot verify") {
				t.Errorf("warning = %q, want it to say the count cannot be verified", warning)
			}
		})
	}
}
