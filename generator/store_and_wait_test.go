package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initOrderProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatalf("AddAggregate(order) error = %v", err)
	}
	fields, err := ParseFields([]string{"amount:int64"})
	if err != nil {
		t.Fatalf("ParseFields() error = %v", err)
	}
	if err := AddEvent("order", "OrderPlaced", fields); err != nil {
		t.Fatalf("AddEvent(order, OrderPlaced) error = %v", err)
	}
	return dir
}

func goIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// The hold is released by the worker reaching the event's ID: right
// after StoreAndWaitProjectionWorker returns, the cursor is past it.
func TestStoreAndWait_HoldsUntilTheWorkerAppliedTheEvent(t *testing.T) {
	dir := initOrderProject(t)
	if err := os.WriteFile(filepath.Join(dir, "projection", "store_and_wait_test.go"), []byte(storeAndWaitTestSrc), 0o644); err != nil {
		t.Fatalf("write generated test: %v", err)
	}
	goIn(t, dir, "test", "-race", "-run", "TestStoreAndWait", "./projection/")
	goIn(t, dir, "vet", "./...")
}

const storeAndWaitTestSrc = `package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"example.com/shop/domain"
	"example.com/shop/eventstore"
)

func newOrderEvent(id string) eventstore.Event {
	return eventstore.Event{
		AggregateName: domain.OrderAggregateName,
		AggregateID:   id,
		EventName:     "OrderPlaced",
		Data:          json.RawMessage(` + "`" + `{"amount":1}` + "`" + `),
	}
}

func cursorOf(t *testing.T, w *OrderProjectionWorker) int64 {
	t.Helper()
	c, err := w.readCursor(context.Background())
	if err != nil {
		t.Fatalf("readCursor: %v", err)
	}
	return c
}

func TestStoreAndWait_ReleasesOnceApplied(t *testing.T) {
	db, err := NewProjectionDB(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("NewProjectionDB: %v", err)
	}
	repo := eventstore.NewFakeStore()
	worker := NewOrderProjectionWorker(repo, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	for i, id := range []string{"o1", "o2", "o3"} {
		stored, err := domain.StoreAndWaitProjectionWorker(ctx, repo, newOrderEvent(id), 0, worker)
		if err != nil {
			t.Fatalf("store %s: %v", id, err)
		}
		if stored.ID != uint(i+1) {
			t.Fatalf("stored ID = %d, want %d", stored.ID, i+1)
		}
		if got := cursorOf(t, worker); got < int64(stored.ID) {
			t.Fatalf("returned with the cursor at %d, before event %d", got, stored.ID)
		}
	}
}

// A worker that never runs cannot hold a command forever, and the
// command still succeeds: its event is stored.
func TestStoreAndWait_TimeoutDoesNotFailTheCommand(t *testing.T) {
	db, err := NewProjectionDB(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("NewProjectionDB: %v", err)
	}
	repo := eventstore.NewFakeStore()
	worker := NewOrderProjectionWorker(repo, db) // never started
	defer func(d time.Duration) { domain.ProjectionWaitTimeout = d }(domain.ProjectionWaitTimeout)
	domain.ProjectionWaitTimeout = 100 * time.Millisecond

	start := time.Now()
	stored, err := domain.StoreAndWaitProjectionWorker(context.Background(), repo, newOrderEvent("o1"), 0, worker)
	if err != nil || stored.ID == 0 {
		t.Fatalf("store = %+v, %v; want stored, no error", stored, err)
	}
	if took := time.Since(start); took < 100*time.Millisecond || took > 2*time.Second {
		t.Fatalf("held for %v, want about the timeout", took)
	}
}

// A worker that does not project the event's aggregate does not hold.
func TestStoreAndWait_OtherAggregateIsNotWaitedFor(t *testing.T) {
	db, err := NewProjectionDB(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("NewProjectionDB: %v", err)
	}
	waiter := NewCursorWaiter(db, "order", domain.OrderAggregateName)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := waiter.WaitPast(ctx, "invoice", 99); err != nil {
		t.Fatalf("WaitPast: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("waited for an aggregate the worker does not project")
	}
}
`

// A project generated before update-client existed: the wait files are
// missing and the client lacks a method; it also has a hand edit.
// update-client restores what is missing, keeps the edit, and the
// project builds. A second run changes nothing.
func TestUpdateClient_OldProjectGainsWhatIsMissingAndKeepsEdits(t *testing.T) {
	dir := initOrderProject(t)
	for _, f := range []string{"domain/projection_wait.go", "projection/wait.go"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatalf("remove %s: %v", f, err)
		}
	}
	// The old worker had no WaitPast either.
	cutFunc(t, filepath.Join(dir, "projection", "order_worker.go"), "func (w *OrderProjectionWorker) WaitPast(")
	clientPath := filepath.Join(dir, "eventstore", "client.go")
	cutFunc(t, clientPath, "func (c *Client) Events(")
	src, _ := os.ReadFile(clientPath)
	edited := strings.Replace(string(src), "func (c *Client) Store(ctx context.Context, req StoreRequest) (Event, error) {",
		"func (c *Client) Store(ctx context.Context, req StoreRequest) (Event, error) {\n\t// hand edit: keep me\n\tif req.AggregateID == \"\" {\n\t\treturn Event{}, ErrConflict\n\t}", 1)
	if edited == string(src) {
		t.Fatal("could not find Client.Store to edit")
	}
	if err := os.WriteFile(clientPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	dry, err := UpdateClient(UpdateClientOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "projection", "wait.go")); err == nil {
		t.Fatal("dry run wrote a file")
	}
	byPath := map[string]ClientFileChange{}
	for _, c := range dry {
		byPath[c.Path] = c
	}
	if !byPath["domain/projection_wait.go"].Created || !byPath["projection/wait.go"].Created {
		t.Errorf("wait files not planned for creation: %+v", dry)
	}
	if got := byPath["eventstore/client.go"]; len(got.Added) != 1 || got.Added[0] != "method Client.Events" {
		t.Errorf("client.go additions = %q, want Client.Events", got.Added)
	}
	if got := byPath["eventstore/client.go"]; !contains(got.Differs, "method Client.Store") {
		t.Errorf("client.go differs = %q, want the edited Client.Store", got.Differs)
	}

	if _, err := UpdateClient(UpdateClientOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := os.ReadFile(clientPath)
	if !strings.Contains(string(after), "// hand edit: keep me") {
		t.Error("the hand edit was lost")
	}
	if !strings.Contains(string(after), "func (c *Client) Events(") {
		t.Error("Client.Events was not restored")
	}
	goIn(t, dir, "build", "./...")
	// An old worker without WaitPast is waited on through its cursor row.
	goIn(t, dir, "vet", "./...")

	again, err := UpdateClient(UpdateClientOptions{})
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	for _, c := range again {
		if c.Changed() {
			t.Errorf("second run changed %s: %+v", c.Path, c)
		}
	}
}

func TestUpdateClient_ModeLimitsTheFiles(t *testing.T) {
	initOrderProject(t)
	changes, err := UpdateClient(UpdateClientOptions{Mode: ClientModeEmbedded, DryRun: true})
	if err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}
	for _, c := range changes {
		if c.Path == "eventstore/client.go" || c.Path == "repository/eventstore_adapter.go" {
			t.Errorf("embedded mode touched %s", c.Path)
		}
	}
	if _, err := UpdateClient(UpdateClientOptions{Mode: "postgres"}); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// `esb add aggregate` in a project without the wait files creates them,
// since the worker and service it generates use them.
func TestAddAggregate_OldProjectGetsTheWaitFiles(t *testing.T) {
	dir := initOrderProject(t)
	for _, f := range []string{"domain/projection_wait.go", "projection/wait.go"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatalf("remove %s: %v", f, err)
		}
	}
	if err := AddAggregate("invoice"); err != nil {
		t.Fatalf("AddAggregate(invoice) error = %v", err)
	}
	for _, f := range []string{"domain/projection_wait.go", "projection/wait.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not created: %v", f, err)
		}
	}
	goIn(t, dir, "build", "./...")
}

// cutFunc removes the function starting at sig from the file.
func cutFunc(t *testing.T, path, sig string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, sig)
	if start < 0 {
		t.Fatalf("%s not found in %s", sig, path)
	}
	// Take the doc comment with it.
	for {
		prev := strings.LastIndex(s[:start-1], "\n")
		if prev < 0 || !strings.HasPrefix(s[prev+1:], "//") {
			break
		}
		start = prev + 1
	}
	end := strings.Index(s[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("end of %s not found", sig)
	}
	s = s[:start] + s[start+end+3:]
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
