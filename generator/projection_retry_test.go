package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProjectionWorker_DoesNotSkipFailedEvent guards the worker retry
// semantics. Workers used to log an applyEvent error and move on to the next
// event in the batch; the next success advanced the cursor past the failed
// event, leaving a permanent hole in the read model. The generated test below
// stores [ok, bad, ok] and asserts the cursor stops at the first event.
func TestProjectionWorker_DoesNotSkipFailedEvent(t *testing.T) {
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

	testFile := filepath.Join(dir, "projection", "worker_retry_test.go")
	if err := os.WriteFile(testFile, []byte(workerRetryTestSrc), 0o644); err != nil {
		t.Fatalf("write generated test: %v", err)
	}

	cmd := exec.Command("go", "test", "-run", "TestWorkerDoesNotSkipFailedEvent", "./projection/")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worker skipped a failed event: %v\n%s", err, output)
	}
}

const workerRetryTestSrc = `package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"example.com/shop/domain"
	"example.com/shop/eventstore"
)

func TestWorkerDoesNotSkipFailedEvent(t *testing.T) {
	db, err := NewProjectionDB(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatalf("NewProjectionDB: %v", err)
	}
	repo := eventstore.NewFakeStore()
	ctx := context.Background()

	store := func(id, data string) eventstore.Event {
		e, err := repo.StoreAtomic(ctx, eventstore.Event{
			AggregateName: domain.OrderAggregateName,
			AggregateID:   id,
			EventName:     "OrderPlaced",
			Data:          json.RawMessage(data),
		}, 0)
		if err != nil {
			t.Fatalf("StoreAtomic: %v", err)
		}
		return e
	}
	first := store("o1", ` + "`" + `{"amount":1}` + "`" + `)
	store("o2", ` + "`" + `{"amount":"not-a-number"}` + "`" + `) // applyEvent fails to unmarshal
	store("o3", ` + "`" + `{"amount":3}` + "`" + `)

	w := NewOrderProjectionWorker(repo, db)
	runCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	w.Run(runCtx) // returns once runCtx is done

	var row ProjectionCursorRow
	if err := db.Where("name = ?", "order").First(&row).Error; err != nil {
		t.Fatalf("read cursor row: %v", err)
	}
	if row.AfterID != int64(first.ID) {
		t.Fatalf("cursor = %d, want %d: the failed event was skipped", row.AfterID, first.ID)
	}
}
`
