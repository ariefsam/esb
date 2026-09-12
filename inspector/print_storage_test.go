package inspector

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrintStorageDetail_EmbeddedWithData covers the CLI equivalent of the
// 'esb ui' /storage page: mode, DSN, per-aggregate counts, and the section
// headers should all render when the embedded SQLite has data.
func TestPrintStorageDetail_EmbeddedWithData(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "events.db")
	if err := seedEventsDB(t, dsn, []seedEvent{
		{AggregateName: "order", EventName: "OrderPlaced", Count: 3},
		{AggregateName: "user", EventName: "UserCreated", Count: 2},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=embedded\nEVENT_STORE_DSN="+dsn+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := PrintStorageDetail(&buf, dir); err != nil {
		t.Fatalf("PrintStorageDetail: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"Storage",
		"mode:      embedded",
		"dsn:       " + dsn,
		"events:    5 total across 2 aggregates",
		"Per aggregate",
		"order",
		"user",
		"Locks",
		"Last migration",
		"(belum pernah migrate)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestPrintStorageDetail_ESBServerMode must never attempt to open a SQLite
// file and should print the ESB URL line instead of a DSN line.
func TestPrintStorageDetail_ESBServerMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=esb-server\nESB_URL=https://esb.example.com\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := PrintStorageDetail(&buf, dir); err != nil {
		t.Fatalf("PrintStorageDetail: %v", err)
	}
	got := buf.String()

	if !strings.Contains(got, "esb url:   https://esb.example.com") {
		t.Errorf("missing esb url line in output:\n%s", got)
	}
	if strings.Contains(got, "dsn:") {
		t.Errorf("esb-server mode should not print a dsn line:\n%s", got)
	}
}

// TestPrintStorageDetail_NoDataYet — a freshly initialised embedded project
// with no SQLite file yet must render placeholders, never crash.
func TestPrintStorageDetail_NoDataYet(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	if err := PrintStorageDetail(&buf, dir); err != nil {
		t.Fatalf("PrintStorageDetail: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"mode:      embedded",
		"(tidak ada data",
		"(tidak ada)",
		"(belum pernah migrate)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}
