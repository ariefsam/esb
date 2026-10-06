package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariefsam/esb/injector"
)

// downgradeToOldService turns service/order.go back into what esb
// generated before storeAndWait: the old self-contained store, and no
// storeEvent or storeAndWait.
func downgradeToOldService(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "service", "order.go")
	for _, sig := range []string{
		"func (s *OrderService) storeAndWait(",
		"func (s *OrderService) storeEvent(",
		"func (s *OrderService) store(",
	} {
		cutFunc(t, path, sig)
	}
	legacy, err := renderTemplate("legacy_service_store.go.tmpl", AggregateData{AggregateNamePascal: "Order"})
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(legacy, "\n")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(src, body...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func changeFor(changes []ClientFileChange, path string) ClientFileChange {
	for _, c := range changes {
		if c.Path == path {
			return c
		}
	}
	return ClientFileChange{}
}

// A project from before storeAndWait gets storeEvent and storeAndWait in
// its untouched services and WaitPast in its workers — the same code a
// new project is generated with — and still builds.
func TestUpdateClient_AddsWaitMethodsToOldServicesAndWorkers(t *testing.T) {
	dir := initOrderProject(t)
	if err := AddProjection("sales_report", []string{"order"}); err != nil {
		t.Fatal(err)
	}
	read := func(p string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	current := map[string][]byte{
		"service/order.go":                  read("service/order.go"),
		"projection/order_worker.go":        read("projection/order_worker.go"),
		"projection/sales_report_worker.go": read("projection/sales_report_worker.go"),
	}
	downgradeToOldService(t, dir)
	cutFunc(t, filepath.Join(dir, "projection", "order_worker.go"), "func (w *OrderProjectionWorker) WaitPast(")
	cutFunc(t, filepath.Join(dir, "projection", "sales_report_worker.go"), "func (w *SalesReportProjectionWorker) WaitPast(")
	goIn(t, dir, "build", "./...")

	changes, err := UpdateClient(UpdateClientOptions{})
	if err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}
	for path, keys := range map[string][]string{
		"service/order.go":                  {"method OrderService.storeAndWait", "method OrderService.storeEvent"},
		"projection/order_worker.go":        {"method OrderProjectionWorker.WaitPast"},
		"projection/sales_report_worker.go": {"method SalesReportProjectionWorker.WaitPast"},
	} {
		c := changeFor(changes, path)
		after := read(path)
		for _, key := range keys {
			if !contains(c.Added, key) {
				t.Errorf("%s: %s not reported added (got %q)", path, key, c.Added)
			}
			if same, err := injector.SameDecl(after, current[path], key); err != nil || !same {
				t.Errorf("%s: %s is not the code a new project gets (err %v)", path, key, err)
			}
		}
	}
	goIn(t, dir, "build", "./...")
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

// storeEvent repeats the generated store's body, so a store changed by
// hand would be bypassed: such a service is reported and left alone.
func TestUpdateClient_LeavesAHandEditedStoreAlone(t *testing.T) {
	dir := initOrderProject(t)
	path := downgradeToOldService(t, dir)
	src, _ := os.ReadFile(path)
	edited := strings.Replace(string(src), "\texpectedVersion := agg.Version\n",
		"\texpectedVersion := agg.Version\n\tif eventName == \"\" {\n\t\treturn nil\n\t}\n", 1)
	if edited == string(src) {
		t.Fatal("could not edit the old store")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := UpdateClient(UpdateClientOptions{})
	if err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}
	c := changeFor(changes, "service/order.go")
	if c.Changed() || len(c.Skipped) != 1 || !strings.Contains(c.Skipped[0], "OrderService.storeAndWait not added") {
		t.Errorf("service/order.go change = %+v, want one skip and no write", c)
	}
	if after, _ := os.ReadFile(path); string(after) != edited {
		t.Error("the hand-edited service was rewritten")
	}
}

// A current service whose storeAndWait was deleted on purpose does not
// get it back.
func TestUpdateClient_DoesNotRestoreARemovedStoreAndWait(t *testing.T) {
	dir := initOrderProject(t)
	path := filepath.Join(dir, "service", "order.go")
	cutFunc(t, path, "func (s *OrderService) storeAndWait(")
	before, _ := os.ReadFile(path)

	changes, err := UpdateClient(UpdateClientOptions{})
	if err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}
	if c := changeFor(changes, "service/order.go"); c.Path != "" {
		t.Errorf("service/order.go reported: %+v", c)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("service/order.go was rewritten")
	}
}
