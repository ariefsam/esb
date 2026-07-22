package inspector

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestScan_Fixture generates a small but representative ESB project on
// disk and runs Scan against it. The fixture is built fresh each run so
// the test does not drift from the file format the generator produces.
//
// Layout mimics what `esb init` + add commands emit:
//
//   - 2 single-aggregate domain files (order, user) with events
//   - 1 handler in server/handler referencing service.OrderService
//   - 1 projection worker (single-aggregate, auto-generated with order)
//   - 1 multi-aggregate projection worker (balance, listens to order+user)
//   - 1 query function in projection/query.go
//   - wire/wire.go with three injection blocks filled in
//   - projection/db.go with two AutoMigrate entries
//   - main.go with two workers in the workers slice
func TestScan_Fixture(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if got.ModuleName != "github.com/example/show-demo" {
		t.Errorf("ModuleName = %q, want github.com/example/show-demo", got.ModuleName)
	}
	if got.PackageName != "showdemo" {
		t.Errorf("PackageName = %q, want showdemo", got.PackageName)
	}

	// Aggregates — sorted by file name.
	if len(got.Aggregate) != 2 {
		t.Fatalf("len(Aggregate) = %d, want 2", len(got.Aggregate))
	}
	if got.Aggregate[0].Name != "order" || got.Aggregate[1].Name != "user" {
		t.Errorf("aggregate order = [%s, %s], want [order, user]",
			got.Aggregate[0].Name, got.Aggregate[1].Name)
	}
	wantOrderEvents := []string{"OrderPlaced", "OrderCancelled"}
	if !equalStrings(got.Aggregate[0].Events, wantOrderEvents) {
		t.Errorf("order events = %v, want %v", got.Aggregate[0].Events, wantOrderEvents)
	}

	// Handlers — place_order resolves to "order" via the service field.
	if len(got.Handler) != 1 {
		t.Fatalf("len(Handler) = %d, want 1", len(got.Handler))
	}
	if got.Handler[0].Name != "place_order" || got.Handler[0].Aggregate != "order" {
		t.Errorf("handler = %+v, want place_order/order", got.Handler[0])
	}

	// Projections — 1 single (order) + 1 multi (balance).
	if len(got.Projection) != 2 {
		t.Fatalf("len(Projection) = %d, want 2", len(got.Projection))
	}
	var orderProj, balanceProj *Projection
	for i := range got.Projection {
		switch got.Projection[i].Name {
		case "order":
			orderProj = &got.Projection[i]
		case "balance":
			balanceProj = &got.Projection[i]
		}
	}
	if orderProj == nil || balanceProj == nil {
		t.Fatalf("expected order + balance projections, got %+v", got.Projection)
	}
	if orderProj.Multi {
		t.Errorf("order projection should be single, got multi")
	}
	if !equalStrings(orderProj.Aggregates, []string{"order"}) {
		t.Errorf("order aggregates = %v, want [order]", orderProj.Aggregates)
	}
	if !balanceProj.Multi {
		t.Errorf("balance projection should be multi")
	}
	if !equalStrings(balanceProj.Aggregates, []string{"order", "user"}) {
		t.Errorf("balance aggregates = %v, want [order, user]", balanceProj.Aggregates)
	}

	// Queries — one function.
	if len(got.Query) != 1 || got.Query[0].Name != "OrdersByBuyer" || got.Query[0].Aggregate != "order" {
		t.Errorf("queries = %+v, want one GetOrderByBuyer on order", got.Query)
	}

	// Wire — two fields declared, two init lines, both stitched.
	if len(got.Wire.Fields) != 2 {
		t.Errorf("len(Wire.Fields) = %d, want 2", len(got.Wire.Fields))
	}
	for _, field := range got.Wire.Fields {
		if !strings.HasPrefix(field.Type, "*") {
			t.Errorf("Wire field %s type = %q, want generated pointer type", field.Field, field.Type)
		}
	}
	if len(got.Wire.Nodes) != 2 {
		t.Errorf("len(Wire.Nodes) = %d, want 2", len(got.Wire.Nodes))
	}

	// Storage — two models migrated, two workers started.
	if !equalStrings(got.Migrate, []string{"OrderRow", "BalanceRow"}) {
		t.Errorf("Migrate = %v, want [OrderRow BalanceRow]", got.Migrate)
	}
	if !equalStrings(got.RunWorker, []string{"OrderProjectionWorker", "BalanceProjectionWorker"}) {
		t.Errorf("RunWorker = %v, want [OrderProjectionWorker, BalanceProjectionWorker]", got.RunWorker)
	}
}

// TestScan_NotFound asserts that Scan reports a clear *NotFound error
// when there is no go.mod in the project directory.
func TestScan_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := Scan(dir)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var nf *NotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected *NotFound, got %T: %v", err, err)
	}
	if !strings.Contains(nf.Error(), "bukan proyek ESB") {
		t.Errorf("error message should be in Bahasa Indonesia, got %q", nf.Error())
	}
}

// TestScan_EmptyProject exercises a fresh `esb init` layout with no adds.
// Only the module line should be populated; everything else is empty.
func TestScan_EmptyProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/example/empty\n\ngo 1.22.0\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got.ModuleName != "github.com/example/empty" {
		t.Errorf("ModuleName = %q", got.ModuleName)
	}
	if len(got.Aggregate) != 0 || len(got.Projection) != 0 || len(got.Handler) != 0 {
		t.Errorf("expected empty project, got %+v", got)
	}
}

// TestScan_MissingMarkerIsTolerant — when the user has edited a marker
// out, Scan should still succeed and report empty slices for that block
// rather than crash.
func TestScan_MissingMarkerIsTolerant(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// wire.go has no markers, just a stub App.
	mkDir(t, dir, "wire")
	if err := os.WriteFile(filepath.Join(dir, "wire", "wire.go"), []byte("package wire\n"), 0644); err != nil {
		t.Fatalf("write wire.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Wire.Fields) != 0 {
		t.Errorf("Wire.Fields should be empty when markers missing, got %d", len(got.Wire.Fields))
	}
}

// TestScan_FindsAggregatesByDeclaration verifies unrelated domain files do
// not appear as aggregates merely because they have a .go extension.
func TestScan_FindsAggregatesByDeclaration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "domain")
	files := map[string]string{
		"order.go": `package domain
const OrderAggregateName = "order"
type Order struct{}
`,
		"money.go": `package domain
type Money struct { Currency string }
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, "domain", name), []byte(src), 0644); err != nil {
			t.Fatalf("write domain/%s: %v", name, err)
		}
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Aggregate) != 1 || got.Aggregate[0].Name != "order" {
		t.Fatalf("Aggregate = %+v, want only order", got.Aggregate)
	}
}

func TestScan_EventsExcludeUnrelatedDomainStructs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "domain")
	if err := os.WriteFile(filepath.Join(dir, "domain", "order.go"), []byte(`package domain

const OrderAggregateName = "order"

type Order struct{}
type Money struct { Amount int64 }
type PlaceOrderCommand struct { Total Money }
type OrderPlaced struct { Total int64 }

func (o *Order) Apply(eventName string, data []byte) error {
	switch eventName {
	case "OrderPlaced":
		return nil
	default:
		return nil
	}
}
`), 0644); err != nil {
		t.Fatalf("write domain/order.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Aggregate) != 1 {
		t.Fatalf("Aggregate = %+v, want one order aggregate", got.Aggregate)
	}
	if !equalStrings(got.Aggregate[0].Events, []string{"OrderPlaced"}) {
		t.Fatalf("Events = %v, want only the Apply event OrderPlaced", got.Aggregate[0].Events)
	}
}

func TestScan_UsesDeclaredAggregateNameForKebabCaseAggregates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "domain")
	if err := os.WriteFile(filepath.Join(dir, "domain", "bank_account.go"), []byte(`package domain
const BankAccountAggregateName = "bank-account"
type BankAccount struct{}
type BankAccountOpened struct{}
func (b *BankAccount) Apply(eventName string, data []byte) error {
	switch eventName {
	case "BankAccountOpened":
		return nil
	default:
		return nil
	}
}
`), 0644); err != nil {
		t.Fatalf("write domain/bank_account.go: %v", err)
	}
	mkDir(t, dir, "projection")
	if err := os.WriteFile(filepath.Join(dir, "projection", "bank_account_worker.go"), []byte("package projection\n"), 0644); err != nil {
		t.Fatalf("write projection/bank_account_worker.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projection", "query.go"), []byte(`package projection
func BankAccounts(ctx context.Context, db *gorm.DB) ([]BankAccountRow, error) { return nil, nil }
`), 0644); err != nil {
		t.Fatalf("write projection/query.go: %v", err)
	}
	mkDir(t, dir, "server", "handler")
	if err := os.WriteFile(filepath.Join(dir, "server", "handler", "open_bank_account.go"), []byte(`package handler
type OpenBankAccountHandler struct { svc *service.BankAccountService }
`), 0644); err != nil {
		t.Fatalf("write server/handler/open_bank_account.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Aggregate) != 1 {
		t.Fatalf("Aggregate = %+v, want one bank-account aggregate", got.Aggregate)
	}
	if got.Aggregate[0].Name != "bank-account" {
		t.Fatalf("Aggregate[0].Name = %q, want bank-account from the generated declaration", got.Aggregate[0].Name)
	}
	if !equalStrings(got.Aggregate[0].Events, []string{"BankAccountOpened"}) {
		t.Fatalf("Aggregate[0].Events = %v, want BankAccountOpened", got.Aggregate[0].Events)
	}
	if len(got.Projection) != 1 || !equalStrings(got.Projection[0].Aggregates, []string{"bank-account"}) {
		t.Fatalf("Projection = %+v, want the bank-account store name", got.Projection)
	}
	if len(got.Handler) != 1 || got.Handler[0].Aggregate != "bank-account" {
		t.Fatalf("Handler = %+v, want the bank-account store name", got.Handler)
	}
	if len(got.Query) != 1 || got.Query[0].Aggregate != "bank-account" {
		t.Fatalf("Query = %+v, want the bank-account store name", got.Query)
	}
}

func TestScan_MultiProjectionUsesMatchingAggregateNamesDeclaration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "projection")
	if err := os.WriteFile(filepath.Join(dir, "projection", "balance_worker.go"), []byte(`package projection

var unrelated = []string{"not-an-aggregate"}
var balanceAggregateNames = []string{"order", "payment"}
`), 0644); err != nil {
		t.Fatalf("write projection/balance_worker.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Projection) != 1 {
		t.Fatalf("Projection = %+v, want one projection", got.Projection)
	}
	if !equalStrings(got.Projection[0].Aggregates, []string{"order", "payment"}) {
		t.Fatalf("Aggregates = %v, want the matching declaration values", got.Projection[0].Aggregates)
	}
}

// TestScan_MultiProjectionIgnoresUnrelatedAggregateNamesDeclaration is the
// regression case the review flagged: when a *_worker.go file declares an
// unrelated *AggregateNames slice before the matching one, the projection
// must NOT silently inherit the unrelated aggregate list.
func TestScan_MultiProjectionIgnoresUnrelatedAggregateNamesDeclaration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "projection")
	// The legacy slice appears FIRST. If the prefix is not verified
	// against the worker name, the parser will mis-attribute
	// "legacy-aggregate" to balance.
	if err := os.WriteFile(filepath.Join(dir, "projection", "balance_worker.go"), []byte(`package projection

var legacyAggregateNames = []string{"legacy-aggregate", "another-legacy"}
var balanceAggregateNames = []string{"order", "payment"}
`), 0644); err != nil {
		t.Fatalf("write projection/balance_worker.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Projection) != 1 {
		t.Fatalf("Projection = %+v, want one projection", got.Projection)
	}
	if !equalStrings(got.Projection[0].Aggregates, []string{"order", "payment"}) {
		t.Fatalf("Aggregates = %v, want [order payment] from the matching declaration", got.Projection[0].Aggregates)
	}
	if got.Projection[0].Multi != true {
		t.Errorf("Multi = false, want true because the matching declaration binds to this worker")
	}
}

// TestScan_MultiProjectionDerivesAggregatesFromSwitch exercises the
// switch-only shape: a worker with `switch e.AggregateName` but no
// matching aggregate-names declaration. Without derivation, the
// projection is multi but lists zero aggregates and disappears from the
// focused view.
func TestScan_MultiProjectionDerivesAggregatesFromSwitch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "projection")
	if err := os.WriteFile(filepath.Join(dir, "projection", "balance_worker.go"), []byte(`package projection

func (w *BalanceProjectionWorker) applyEvent(_ interface{}, e eventstore.Event) error {
	switch e.AggregateName {
	case "order":
		return nil
	case "payment":
		return nil
	default:
		return nil
	}
}
`), 0644); err != nil {
		t.Fatalf("write projection/balance_worker.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Projection) != 1 {
		t.Fatalf("Projection = %+v, want one projection", got.Projection)
	}
	if !got.Projection[0].Multi {
		t.Errorf("Multi = false, want true because switch on e.AggregateName was detected")
	}
	if !equalStrings(got.Projection[0].Aggregates, []string{"order", "payment"}) {
		t.Fatalf("Aggregates = %v, want the case branches [order payment]", got.Projection[0].Aggregates)
	}
}

// TestScan_ExtractEventsIgnoresLaterCasesWithIndentedCloseBrace guards
// against the bug where Apply() detection exited on an unindented `}`.
// A non-gofmt Apply with an indented closing brace used to leave
// inApply enabled, so case statements in unrelated functions were
// reported as events.
func TestScan_ExtractEventsIgnoresLaterCasesWithIndentedCloseBrace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mkDir(t, dir, "domain")
	// Intentionally non-gofmt: closing brace of Apply is indented.
	if err := os.WriteFile(filepath.Join(dir, "domain", "order.go"), []byte(`package domain

const OrderAggregateName = "order"

func (o *Order) Apply(eventName string, data []byte) error {
	switch eventName {
	case "OrderPlaced":
		return nil
	}
	}

func (o *Order) SomeOtherFunction() {
	case "PhantomEvent":
		_ = 0
}
`), 0644); err != nil {
		t.Fatalf("write domain/order.go: %v", err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Aggregate) != 1 {
		t.Fatalf("Aggregate = %+v, want one order aggregate", got.Aggregate)
	}
	if !equalStrings(got.Aggregate[0].Events, []string{"OrderPlaced"}) {
		t.Fatalf("Events = %v, want only [OrderPlaced]; PhantomEvent must not leak in", got.Aggregate[0].Events)
	}
}

// TestScan_ReadModuleNamePropagatesIOErrors verifies that an unreadable
// go.mod surfaces a contextual error instead of being reported as a
// missing "module" directive. The chmod path exercises os.Open, but the
// scanner.Err() check (the actual regression) is hit when a single line
// exceeds bufio.Scanner's 64 KiB buffer.
func TestScan_ReadModuleNamePropagatesIOErrors(t *testing.T) {
	t.Run("unreadable file", func(t *testing.T) {
		dir := t.TempDir()
		goModPath := filepath.Join(dir, "go.mod")
		if err := os.WriteFile(goModPath, []byte("module example.com/demo\n"), 0644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.Chmod(goModPath, 0000); err != nil {
			t.Fatalf("chmod go.mod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(goModPath, 0644) })

		_, err := Scan(dir)
		if err == nil {
			t.Skip("sandbox allows reading mode-0000 file; cannot exercise scanner.Err() path here")
		}
		if !strings.Contains(err.Error(), goModPath) {
			t.Fatalf("Scan error = %q, want it to mention %s", err, goModPath)
		}
		if strings.Contains(err.Error(), "module directive not found") {
			t.Fatalf("Scan error = %q, want it to surface the I/O failure rather than 'module directive not found'", err)
		}
	})

	t.Run("line exceeds scanner buffer", func(t *testing.T) {
		dir := t.TempDir()
		// bufio.Scanner has a default max token size of 64 KiB. A line
		// longer than that makes scanner.Scan return false with a
		// non-nil scanner.Err(), which the old code discarded. The huge
		// line must come before the "module" line so the scanner fails
		// before finding the directive.
		huge := strings.Repeat("a", 80*1024)
		goModPath := filepath.Join(dir, "go.mod")
		if err := os.WriteFile(goModPath, []byte(huge+"\nmodule example.com/demo\n"), 0644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}

		_, err := Scan(dir)
		if err == nil {
			t.Fatal("Scan returned nil for a go.mod whose line exceeds scanner buffer")
		}
		if !strings.Contains(err.Error(), goModPath) {
			t.Fatalf("Scan error = %q, want it to mention %s", err, goModPath)
		}
		if strings.Contains(err.Error(), "module directive not found") {
			t.Fatalf("Scan error = %q, want it to surface the scanner failure rather than 'module directive not found'", err)
		}
	})
}

func TestScan_PropagatesEntityReadErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{
			name: "aggregate",
			path: filepath.Join("domain", "order.go"),
		},
		{
			name: "handler",
			path: filepath.Join("server", "handler", "place_order.go"),
		},
		{
			name: "projection",
			path: filepath.Join("projection", "order_worker.go"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n"), 0644); err != nil {
				t.Fatalf("write go.mod: %v", err)
			}
			path := filepath.Join(dir, tt.path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
			}
			if err := os.Symlink(filepath.Join(dir, "missing-source.go"), path); err != nil {
				t.Fatalf("symlink %s: %v", path, err)
			}

			_, err := Scan(dir)
			if err == nil {
				t.Fatalf("Scan returned nil for unreadable %s", tt.path)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("Scan error = %q, want file context %q", err, path)
			}
		})
	}
}

// equalStrings compares two slices ignoring order, since the scan step
// sorts internally but the fixtures may emit fields in source order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// writeFixture emits a representative ESB project into root. The contents
// match the exact format the generator produces — header guard, marker
// comments, etc. — so any drift here also reflects a generator break.
func writeFixture(t *testing.T, root string) {
	t.Helper()
	const src = `module github.com/example/show-demo

go 1.22.0
`
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(src), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// --- domain/order.go ---
	mkDir(t, root, "domain")
	if err := os.WriteFile(filepath.Join(root, "domain", "order.go"), []byte(expandTabs(orderDomain)), 0644); err != nil {
		t.Fatalf("write domain/order.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "domain", "user.go"), []byte(expandTabs(userDomain)), 0644); err != nil {
		t.Fatalf("write domain/user.go: %v", err)
	}

	// --- server/handler/place_order.go ---
	mkDir(t, root, "server", "handler")
	if err := os.WriteFile(filepath.Join(root, "server", "handler", "place_order.go"), []byte(expandTabs(handlerSrc)), 0644); err != nil {
		t.Fatalf("write handler: %v", err)
	}

	// --- projection/ ---
	mkDir(t, root, "projection")
	for name, content := range map[string]string{
		"order_worker.go":   orderWorker,
		"balance_worker.go": balanceWorker,
		"query.go":          querySrc,
		"db.go":             dbSrc,
	} {
		if err := os.WriteFile(filepath.Join(root, "projection", name), []byte(expandTabs(content)), 0644); err != nil {
			t.Fatalf("write projection/%s: %v", name, err)
		}
	}

	// --- wire/wire.go ---
	mkDir(t, root, "wire")
	if err := os.WriteFile(filepath.Join(root, "wire", "wire.go"), []byte(expandTabs(wireSrc)), 0644); err != nil {
		t.Fatalf("write wire.go: %v", err)
	}

	// --- main.go ---
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(expandTabs(mainSrc)), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
}

func mkDir(t *testing.T, root string, parts ...string) {
	t.Helper()
	all := append([]string{root}, parts...)
	if err := os.MkdirAll(filepath.Join(all...), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Join(all...), err)
	}
}

// Source fragments emulating generator output. The "tag" line is a
// placeholder that the fixture loader replaces with a real tab so the
// generated files have correct Go indentation.
const ind = "<TAB>"

// expandTabs replaces the <TAB> sentinel with a real tab byte. Used so
// the test source below stays readable in editors and JSON-encoded
// payloads while still producing properly indented Go files at runtime.
func expandTabs(s string) string {
	return strings.ReplaceAll(s, ind, "\t")
}

const orderDomain = `package domain

import "encoding/json"

const OrderAggregateName = "order"

// OrderPlaced event.
type OrderPlaced struct {
` + ind + `Amount   int64  ` + "`json:\"amount\"`" + `
` + ind + `Currency string ` + "`json:\"currency\"`" + `
` + ind + `OccurredAt int64 ` + "`json:\"occurred_at\"`" + `
}

type Order struct {
` + ind + `AggregateID string
` + ind + `Version     int64
}

func (o *Order) Apply(eventName string, data json.RawMessage) error {
` + ind + `switch eventName {
` + ind + `case "OrderPlaced":
` + ind + ind + `var evt OrderPlaced
` + ind + ind + `if err := json.Unmarshal(data, &evt); err != nil {
` + ind + ind + ind + `return err
` + ind + ind + `}
` + ind + ind + `o.Version++
` + ind + ind + `return nil
` + ind + `case "OrderCancelled":
` + ind + ind + `var evt OrderCancelled
` + ind + ind + `if err := json.Unmarshal(data, &evt); err != nil {
` + ind + ind + ind + `return err
` + ind + ind + `}
` + ind + ind + `o.Version++
` + ind + ind + `return nil
` + ind + `// esb:inject:apply-cases
` + ind + `default:
` + ind + ind + `return nil
` + ind + `}
}
`

const userDomain = `package domain

import "encoding/json"

const UserAggregateName = "user"

// UserRegistered event.
type UserRegistered struct {
` + ind + `Email      string ` + "`json:\"email\"`" + `
` + ind + `OccurredAt int64  ` + "`json:\"occurred_at\"`" + `
}

type User struct {
` + ind + `AggregateID string
` + ind + `Version     int64
}

func (u *User) Apply(eventName string, data json.RawMessage) error {
` + ind + `switch eventName {
` + ind + `case "UserRegistered":
` + ind + ind + `var evt UserRegistered
` + ind + ind + `if err := json.Unmarshal(data, &evt); err != nil {
` + ind + ind + ind + `return err
` + ind + ind + `}
` + ind + ind + `u.Version++
` + ind + ind + `return nil
` + ind + `// esb:inject:apply-cases
` + ind + `default:
` + ind + ind + `return nil
` + ind + `}
}
`

const handlerSrc = `package handler

import "github.com/example/show-demo/service"

type PlaceOrderHandler struct {
` + ind + `svc *service.OrderService
}
`

const orderWorker = `package projection

import (
` + ind + `"github.com/example/show-demo/eventstore"
` + ind + `"gorm.io/gorm"
)

type OrderProjectionWorker struct {
` + ind + `esClient *eventstore.Client
` + ind + `db       *gorm.DB
}

func NewOrderProjectionWorker(esClient *eventstore.Client, db *gorm.DB) *OrderProjectionWorker {
` + ind + `return &OrderProjectionWorker{esClient: esClient, db: db}
}

func (w *OrderProjectionWorker) applyEvent(_ /*ctx*/ interface{}, _ /*e*/ eventstore.Event) error {
` + ind + `// esb:inject:applyevent-cases
` + ind + `return nil
}
`

const balanceWorker = `package projection

import (
` + ind + `"github.com/example/show-demo/eventstore"
` + ind + `"gorm.io/gorm"
)

var balanceAggregateNames = []string{
` + ind + `"order",
` + ind + `"user",
}

type BalanceProjectionWorker struct {
` + ind + `esClient *eventstore.Client
` + ind + `db       *gorm.DB
}

func NewBalanceProjectionWorker(esClient *eventstore.Client, db *gorm.DB) *BalanceProjectionWorker {
` + ind + `return &BalanceProjectionWorker{esClient: esClient, db: db}
}

func (w *BalanceProjectionWorker) applyEvent(_ /*ctx*/ interface{}, e eventstore.Event) error {
` + ind + `switch e.AggregateName {
` + ind + `// esb:inject:applyevent-cases
` + ind + `default:
` + ind + ind + `return nil
` + ind + `}
` + ind + `return nil
}
`

const querySrc = `package projection

import (
` + ind + `"context"

` + ind + `"gorm.io/gorm"
)

// OrdersByBuyer queries Order rows.
func OrdersByBuyer(ctx context.Context, db *gorm.DB) ([]OrderRow, error) {
` + ind + `var rows []OrderRow
` + ind + `err := db.WithContext(ctx).Find(&rows).Error
` + ind + `return rows, err
}
`

const dbSrc = `package projection

import (
` + ind + `"gorm.io/driver/sqlite"
` + ind + `"gorm.io/gorm"
)

func NewProjectionDB(dsn string) (*gorm.DB, error) {
` + ind + `db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
` + ind + `if err != nil {
` + ind + ind + `return nil, err
` + ind + `}
` + ind + `if err := db.AutoMigrate(
` + ind + ind + `&ProjectionCursorRow{},
` + ind + ind + `// esb:inject:automigrate-models
` + ind + ind + `&OrderRow{},
` + ind + ind + `&BalanceRow{},
` + ind + `); err != nil {
` + ind + ind + `return nil, err
` + ind + `}
` + ind + `return db, nil
}

type ProjectionCursorRow struct {
` + ind + `Name string ` + "`gorm:\"primaryKey\"`" + `
}
`

const wireSrc = `package wire

import (
` + ind + `"github.com/example/show-demo/projection"
)

type App struct {
` + ind + `Env     *Env
` + ind + `Handler interface{}
` + ind + `// esb:inject:app-fields
` + ind + `OrderProjectionWorker   *projection.OrderProjectionWorker
` + ind + `BalanceProjectionWorker *projection.BalanceProjectionWorker
}

func NewApp() (*App, error) {
` + ind + `// esb:inject:app-init
` + ind + `orderWorker := projection.NewOrderProjectionWorker(nil, nil)
` + ind + `balanceWorker := projection.NewBalanceProjectionWorker(nil, nil)
` + ind + `return &App{
` + ind + ind + `// esb:inject:app-return-fields
` + ind + ind + `OrderProjectionWorker:   orderWorker,
` + ind + ind + `BalanceProjectionWorker: balanceWorker,
` + ind + `}, nil
}

type Env struct{}
`

const mainSrc = `package main

import (
` + ind + `"github.com/example/show-demo/projection"
)

func main() {
` + ind + `_ = projection.Worker(nil)
` + ind + `workers := []projection.Worker{
` + ind + ind + `// esb:inject:projection-workers
` + ind + ind + `app.OrderProjectionWorker,
` + ind + ind + `app.BalanceProjectionWorker,
` + ind + `}
` + ind + `_ = workers
}
`
