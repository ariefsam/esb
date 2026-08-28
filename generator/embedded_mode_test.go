package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readGenerated(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}

// TestEmbeddedMode_WorkerDoesNotDependOnHTTPClient guards the regression
// where a generated projection worker took *eventstore.Client. In embedded
// mode wire never builds that client, so every generated project panicked
// with a nil pointer dereference the moment its first worker polled — the
// default `esb init` + `make run` path.
func TestEmbeddedMode_WorkerDoesNotDependOnHTTPClient(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatalf("AddAggregate() error = %v", err)
	}

	worker := readGenerated(t, dir, "projection", "order_worker.go")
	if strings.Contains(worker, "eventstore.Client") {
		t.Error("worker depends on *eventstore.Client, which is nil in embedded mode")
	}
	if !strings.Contains(worker, "events eventstore.EventRepository") {
		t.Error("worker does not read through the EventRepository")
	}
	// Draining the stream must not turn into a spin loop: the embedded
	// store returns immediately instead of long-polling.
	if !strings.Contains(worker, "if len(batch) == 0 {") {
		t.Error("worker has no idle backoff when the stream is drained")
	}

	wire := readGenerated(t, dir, "wire", "wire.go")
	if !strings.Contains(wire, "projection.NewOrderProjectionWorker(eventRepo, db)") {
		t.Error("wire does not hand the worker the event repository")
	}
}

// TestGenerated_ErrConflictAliasesStoreSentinel guards the regression where
// domain declared its own ErrConflict. Command code that checked
// errors.Is(err, domain.ErrConflict) never matched what StoreAtomic
// returned, so lost optimistic-locking races looked like unknown failures.
func TestGenerated_ErrConflictAliasesStoreSentinel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	if got := readGenerated(t, dir, "domain", "event.go"); !strings.Contains(got, "ErrConflict = eventstore.ErrConflict") {
		t.Error("domain.ErrConflict is not an alias of eventstore.ErrConflict")
	}
	if got := readGenerated(t, dir, "domain", "errors.go"); strings.Contains(got, "ErrConflict") {
		t.Error("domain/errors.go still declares a second ErrConflict sentinel")
	}
}

// TestGenerated_KeygenIsPortable guards the regression where `make keygen`
// used base64 flags that exist on GNU coreutils but not on macOS, so the
// target failed on the exact platform the README's setup path assumes.
func TestGenerated_KeygenIsPortable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	makefile := readGenerated(t, dir, "Makefile")
	if strings.Contains(makefile, "base64 -b 0") || strings.Contains(makefile, "base64 -w 0") {
		t.Error("keygen uses platform-specific base64 flags")
	}
	if !strings.Contains(makefile, "openssl base64 -A -in public.pem") {
		t.Error("keygen does not use the portable openssl base64 form")
	}
}
