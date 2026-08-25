package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitProject_GeneratedEmbeddedProjectCompiles(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "generated-app")
	if err := InitProject("example.com/generated-app", dest); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dest
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated project does not compile: %v\n%s", err, output)
	}
}

// TestInitProject_GeneratedEmbeddedProjectStartsWithoutKey is the
// regression test for the embedded mode startup contract. README
// states that a freshly-generated project must boot in embedded mode
// without `make keygen` — only esb-server mode requires the PEM.
// We exercise NewApp from the generated wire package so the test
// fails if a future template change resurrects the unconditional
// mustLoadKey() call.
func TestInitProject_GeneratedEmbeddedProjectStartsWithoutKey(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "generated-app")
	if err := InitProject("example.com/generated-app", dest); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	// Run a tiny test inside the generated project that calls
	// wire.NewApp(). No private.pem is present, so a regression
	// would surface here as a panic or load failure.
	testSrc := `package generated_app_test

import (
	"os"
	"testing"

	"example.com/generated-app/wire"
)

func TestEmbeddedModeStartsWithoutPrivateKey(t *testing.T) {
	// Make sure no PEM is lurking in the project root.
	os.Remove("private.pem")
	if _, err := wire.NewApp(); err != nil {
		t.Fatalf("wire.NewApp() in embedded mode without private.pem: %v", err)
	}
}
`
	if err := os.MkdirAll(filepath.Join(dest, "startup"), 0755); err != nil {
		t.Fatalf("mkdir startup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "startup", "startup_test.go"), []byte(testSrc), 0644); err != nil {
		t.Fatalf("write startup test: %v", err)
	}

	cmd := exec.Command("go", "test", "-run", "TestEmbeddedModeStartsWithoutPrivateKey", "./...")
	cmd.Dir = dest
	if output, err := cmd.CombinedOutput(); err != nil {
		out := string(output)
		// Surface a focused error so the regression is obvious.
		if strings.Contains(out, "load private key") {
			t.Fatalf("embedded mode still requires private.pem:\n%s", out)
		}
		t.Fatalf("generated project did not start in embedded mode: %v\n%s", err, out)
	}
}

func TestInitProject_LocalStoreRejectsWrongExpectedVersion(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "generated-app")
	if err := InitProject("example.com/generated-app", dest); err != nil {
		t.Fatal(err)
	}
	testSrc := `package eventstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWrongExpectedVersion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "events.db")), &gorm.Config{})
	if err != nil { t.Fatal(err) }
	if err := MigrateLocalStore(db); err != nil { t.Fatal(err) }
	store := NewLocalStore(db)
	_, err = store.StoreAtomic(context.Background(), Event{AggregateName: "order", AggregateID: "a", EventName: "Placed"}, 10)
	if !errors.Is(err, ErrConflict) { t.Fatalf("error = %v, want ErrConflict", err) }
}
`
	if err := os.WriteFile(filepath.Join(dest, "eventstore", "local_store_test.go"), []byte(testSrc), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "./eventstore")
	cmd.Dir = dest
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated local store accepted wrong expected version: %v\n%s", err, output)
	}
}

func TestInitProject_WireReusesProjectionDBForDefaultEmbeddedStore(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "generated-app")
	if err := InitProject("example.com/generated-app", dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "wire", "wire.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "buildEventRepository(env, esClient, db)") {
		t.Fatal("generated wiring does not pass the projection DB handle to the embedded store")
	}
}

// TestFullAddWorkflow_GeneratesCompilableProject exercises the complete
// scaffolding sequence: init → add aggregate → add event → add handler →
// add query → add projection, then verifies the result compiles.
// This is the regression test for C1/C2 — ensures all add commands preserve
// compilability.
func TestFullAddWorkflow_GeneratesCompilableProject(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "test-shop")
	if err := InitProject("example.com/test-shop", dest); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	// Change to project dir for all subsequent operations.
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() = %v", err)
	}
	if err := os.Chdir(dest); err != nil {
		t.Fatalf("Chdir(%s) = %v", dest, err)
	}
	defer os.Chdir(oldCwd)

	// Sequence: add aggregate → add event → add handler → add query → add projection.
	if err := AddAggregate("order"); err != nil {
		t.Fatalf("AddAggregate(order) = %v", err)
	}

	if err := AddEvent("order", "OrderPlaced", []FieldDef{
		{NamePascal: "Amount", JSONTag: "amount", Type: "int64"},
		{NamePascal: "CustomerID", JSONTag: "customer_id", Type: "string"},
	}); err != nil {
		t.Fatalf("AddEvent(OrderPlaced) = %v", err)
	}

	if err := AddEvent("order", "OrderCanceled", []FieldDef{
		{NamePascal: "Reason", JSONTag: "reason", Type: "string"},
	}); err != nil {
		t.Fatalf("AddEvent(OrderCanceled) = %v", err)
	}

	if err := AddHandler("place_order", "order"); err != nil {
		t.Fatalf("AddHandler(place_order, order) = %v", err)
	}

	if err := AddHandler("cancel_order", "order"); err != nil {
		t.Fatalf("AddHandler(cancel_order, order) = %v", err)
	}

	if err := AddQuery("orders_by_customer", "order"); err != nil {
		t.Fatalf("AddQuery(orders_by_customer) = %v", err)
	}

	if err := AddProjection("customer_orders", []string{"order"}); err != nil {
		t.Fatalf("AddProjection(customer_orders) = %v", err)
	}

	// Verify the project compiles after all modifications.
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dest
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated project does not compile after full add workflow:\nerror: %v\noutput:\n%s", err, output)
	}
}
