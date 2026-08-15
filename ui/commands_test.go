package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner is a ProcessRunner used by tests to assert that argv and
// directory reach the runner verbatim, and to inject controllable
// exit codes / errors.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []fakeCall
	exitCode int
	err      error
	delay    time.Duration
}

type fakeCall struct {
	Dir    string
	Argv   []string
	Stdout string
	Stderr string
}

func (f *fakeRunner) Run(ctx context.Context, projectRoot string, argv []string, stdout, stderr io.Writer) (int, error) {
	f.mu.Lock()
	c := fakeCall{Dir: projectRoot, Argv: append([]string{}, argv...)}
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return 0, ErrTimeout
		}
	}
	if f.err != nil {
		return f.exitCode, f.err
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, "ok\n")
	}
	c.Stdout = "ok\n"
	c.Stderr = ""
	if f.exitCode != 0 {
		c.Stderr = "fake failure\n"
		if stderr != nil {
			_, _ = io.WriteString(stderr, "fake failure\n")
		}
		return f.exitCode, nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	return 0, nil
}

func TestBuildArgv_AcceptedCommands(t *testing.T) {
	cases := []struct {
		command string
		form    FormInput
		want    []string
	}{
		{
			command: "add-aggregate",
			form:    FormInput{"name": {"order"}},
			want:    []string{"esb", "add", "aggregate", "order"},
		},
		{
			command: "add-event",
			form:    FormInput{"aggregate": {"order"}, "event": {"OrderPlaced"}, "fields": {"amount:int64", "currency:string"}},
			want:    []string{"esb", "add", "event", "order", "OrderPlaced", "amount:int64", "currency:string"},
		},
		{
			command: "add-projection",
			form:    FormInput{"name": {"sales_report"}, "aggregates": {"order", "payment"}},
			want:    []string{"esb", "add", "projection", "sales_report", "--aggregates", "order,payment"},
		},
		{
			command: "add-handler",
			form:    FormInput{"name": {"place_order"}, "aggregate": {"order"}},
			want:    []string{"esb", "add", "handler", "place_order", "--aggregate", "order"},
		},
		{
			command: "add-query",
			form:    FormInput{"name": {"orders_by_buyer"}, "aggregate": {"order"}},
			want:    []string{"esb", "add", "query", "orders_by_buyer", "--aggregate", "order"},
		},
		{
			command: "add-recipe-crud",
			form:    FormInput{"name": {"product"}, "fields": {"name:string", "price:int64"}},
			want:    []string{"esb", "add", "recipe", "crud", "product", "name:string", "price:int64"},
		},
		{
			command: "show",
			form:    FormInput{},
			want:    []string{"esb", "show"},
		},
		{
			command: "show",
			form:    FormInput{"aggregate": {"order"}},
			want:    []string{"esb", "show", "order"},
		},
	}
	for _, tc := range cases {
		got, err := BuildArgv(tc.command, tc.form)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.command, err)
			continue
		}
		if !equalStrings(got, tc.want) {
			t.Errorf("%s argv = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestBuildArgv_RejectsInvalidNames(t *testing.T) {
	cases := []struct {
		command string
		form    FormInput
	}{
		{"add-aggregate", FormInput{"name": {"order;touch /tmp/pwned"}}},
		{"add-aggregate", FormInput{"name": {"Order"}}},                            // not snake_case
		{"add-aggregate", FormInput{"name": {""}}},                                 // missing required
		{"add-event", FormInput{"aggregate": {"order"}, "event": {"orderPlaced"}}}, // not PascalCase
		{"add-event", FormInput{"aggregate": {"order"}, "event": {"OrderPlaced"}, "fields": {"amount;evil"}}},
		{"add-projection", FormInput{"name": {"x"}, "aggregates": {}}}, // empty list
		{"add-projection", FormInput{"name": {"x"}, "aggregates": {"bad name"}}},
		{"add-handler", FormInput{"name": {"x"}, "aggregate": {"bad name"}}},
		{"add-query", FormInput{"name": {"x"}, "aggregate": {"bad name"}}},
		{"add-recipe-crud", FormInput{"name": {"Product"}}},                                  // not snake_case
		{"add-recipe-crud", FormInput{"name": {"product;rm -rf /"}}},                         // shell metachar
		{"add-recipe-crud", FormInput{"name": {"product"}, "fields": {"price;evil"}}},        // bad field
		{"show", FormInput{"aggregate": {"bad name"}}},
	}
	for _, tc := range cases {
		if _, err := BuildArgv(tc.command, tc.form); err == nil {
			t.Errorf("%s: expected error for %v, got nil", tc.command, tc.form)
		}
	}
}

func TestBuildArgv_RejectsUnknownCommand(t *testing.T) {
	if _, err := BuildArgv("rm-rf", FormInput{}); err == nil {
		t.Fatal("expected error for unknown command id")
	}
}

// TestBuildArgv_MigrateToESB pins the argv shape produced by the
// new catalog entry — the storage page POST handler depends on
// this exact layout.
func TestBuildArgv_MigrateToESB(t *testing.T) {
	got, err := BuildArgv("migrate-to-esb", FormInput{
		"esb_url":    {"http://esb.internal:8080"},
		"tenant_id":  {"demo"},
		"project_id": {"toko"},
	})
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	want := []string{"esb", "migrate", "to-esb", "--source", "app.db", "--esb-url", "http://esb.internal:8080", "--tenant", "demo", "--project", "toko"}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

// TestBuildArgv_MigrateToESBRejectsJSURL guards the URL validator
// against javascript:/file:// schemes — the value flows to a
// cobra positional argument, so a malicious payload could escape
// into another arg if validation is sloppy.
func TestBuildArgv_MigrateToESBRejectsJSURL(t *testing.T) {
	_, err := BuildArgv("migrate-to-esb", FormInput{
		"esb_url":    {"javascript:alert(1)"},
		"tenant_id":  {"demo"},
		"project_id": {"toko"},
	})
	if err == nil {
		t.Fatal("expected javascript: URL to be rejected")
	}
}

// TestBuildArgv_MigrateToESBRejectsMissingTenant exercises the
// required-field path: an empty tenant_id must fail BuildArgv
// before the runner is ever invoked.
func TestBuildArgv_MigrateToESBRejectsMissingTenant(t *testing.T) {
	_, err := BuildArgv("migrate-to-esb", FormInput{
		"esb_url":    {"http://esb.internal:8080"},
		"tenant_id":  {""},
		"project_id": {"toko"},
	})
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
}

// TestBuildArgv_MigrateToEmbedded exercises the rollback path:
// the form must accept esb_url/tenant_id/project_id and emit the
// matching CLI flags. The optional force checkbox maps to --force.
func TestBuildArgv_MigrateToEmbedded(t *testing.T) {
	got, err := BuildArgv("migrate-to-embedded", FormInput{
		"esb_url":    {"http://esb.internal:8080"},
		"tenant_id":  {"demo"},
		"project_id": {"toko"},
		"force":      {"true"},
	})
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	want := []string{
		"esb", "migrate", "to-embedded",
		"--source", "app.db",
		"--esb-url", "http://esb.internal:8080",
		"--tenant", "demo",
		"--project", "toko",
		"--force",
	}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}

	// Force checkbox unchecked → no --force flag.
	got, err = BuildArgv("migrate-to-embedded", FormInput{
		"esb_url":    {"http://esb.internal:8080"},
		"tenant_id":  {"demo"},
		"project_id": {"toko"},
	})
	if err != nil {
		t.Fatalf("BuildArgv (no force): %v", err)
	}
	for _, a := range got {
		if a == "--force" {
			t.Errorf("argv unexpectedly contains --force when checkbox unchecked: %v", got)
		}
	}

	// Missing required field must fail validation.
	if _, err := BuildArgv("migrate-to-embedded", FormInput{
		"tenant_id":  {"demo"},
		"project_id": {"toko"},
	}); err == nil {
		t.Error("expected error when esb_url missing")
	}
}

// TestValidateFieldURL exercises the URL validator directly so the
// rules are locked in: scheme, host presence, character class.
func TestValidateFieldURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://localhost:8080", true},
		{"https://esb.internal", true},
		{"https://esb.internal/path/with/slashes", true},
		{"https://192.168.1.1:9000/api", true},
		{"", false},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
		{"http://", false},
		{"ftp://server", false},
		{"http://host space", false},
		{"http://host?query=1", false},
		{"http://.", false},
		{"http://:8080", false},
		{"http://...", false},
		{"http://-bad.example", false},
	}
	for _, tc := range cases {
		err := validateFieldURL(tc.in)
		if (err == nil) != tc.want {
			t.Errorf("validateFieldURL(%q) error=%v, want valid=%v", tc.in, err, tc.want)
		}
	}
}

// TestFindCommand_MigrateEntries pins that both new catalog IDs
// are reachable from findCommand — guards against a typo in the
// catalog accidentally hiding the page link.
func TestFindCommand_MigrateEntries(t *testing.T) {
	for _, id := range []string{"migrate-to-esb", "migrate-to-embedded"} {
		if findCommand(id) == nil {
			t.Errorf("findCommand(%q) returned nil", id)
		}
	}
}

// TestMigrateToEmbedded_DeclaresFormFields locks in the field set
// that ui/templates/migrate.html submits for the to-embedded
// direction. If a future refactor empties the catalog entry the
// handleMigrateSubmit handler will reject every form submission
// with "field tidak dikenal" — exactly the regression we hit before.
func TestMigrateToEmbedded_DeclaresFormFields(t *testing.T) {
	cmd := findCommand("migrate-to-embedded")
	if cmd == nil {
		t.Fatal("migrate-to-embedded catalog entry not found")
	}
	want := map[string]bool{
		"source":     true,
		"esb_url":    true,
		"tenant_id":  true,
		"project_id": true,
		"force":      true,
	}
	got := map[string]bool{}
	for _, f := range cmd.Fields {
		got[f.Name] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("migrate-to-embedded catalog missing field %q (have %v)", k, got)
		}
	}
}

func TestBuildMigrateCommandsPassExplicitSource(t *testing.T) {
	form := FormInput{
		"source": {"data/events.db"}, "esb_url": {"https://esb.example.com"},
		"tenant_id": {"demo"}, "project_id": {"shop"},
	}
	for _, id := range []string{"migrate-to-esb", "migrate-to-embedded"} {
		argv, err := BuildArgv(id, form)
		if err != nil {
			t.Fatalf("BuildArgv(%s): %v", id, err)
		}
		if !strings.Contains(strings.Join(argv, " "), "--source data/events.db") {
			t.Fatalf("%s argv does not pass source: %v", id, argv)
		}
	}
}

func TestParseForm_SplitsTextareaListValues(t *testing.T) {
	form := ParseForm(map[string][]string{
		"fields": {"text\namount:int64\n currency:string "},
	})
	got := form["fields"]
	want := []string{"text", "amount:int64", "currency:string"}
	if !equalStrings(got, want) {
		t.Errorf("parsed textarea fields = %v, want %v", got, want)
	}
}

func TestRunStore_GetReturnsIndependentSnapshot(t *testing.T) {
	store := NewRunStore()
	runner := &fakeRunner{}
	run, err := store.Start(context.Background(), "/tmp", "show", FormInput{}, runner)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	snapshot := store.Get(run.ID)
	if snapshot == nil {
		t.Fatal("missing snapshot")
	}
	snapshot.Status = RunFailed
	snapshot.Argv[0] = "changed"
	stored := store.Get(run.ID)
	if stored.Status == RunFailed || stored.Argv[0] == "changed" {
		t.Fatalf("Get returned mutable store data: %+v", stored)
	}
}
func TestRunStore_OneActiveRun(t *testing.T) {
	store := NewRunStore()
	runner := &fakeRunner{delay: 50 * time.Millisecond}

	run1, err := store.Start(context.Background(), "/tmp", "show", FormInput{}, runner)
	if err != nil {
		t.Fatalf("start run1: %v", err)
	}
	if !store.Busy() {
		t.Fatal("expected busy after first start")
	}

	// Second start while first is running must conflict.
	if _, err := store.Start(context.Background(), "/tmp", "show", FormInput{}, runner); !errors.Is(err, ErrConflict) {
		t.Errorf("second start error = %v, want ErrConflict", err)
	}

	// Wait for run1 to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, ok := store.StatusSnapshot(run1.ID); ok && status != RunRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r := store.Get(run1.ID); r == nil || r.Status != RunSucceed {
		t.Errorf("expected run1 to succeed, got %+v", r)
	}
	if store.Busy() {
		t.Errorf("store still busy after completion")
	}
}

func TestRunStore_FailureRecordsExitCode(t *testing.T) {
	store := NewRunStore()
	runner := &fakeRunner{exitCode: 1}

	run, err := store.Start(context.Background(), "/tmp", "show", FormInput{}, runner)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, exitCode, ok := store.StatusSnapshot(run.ID)
		if ok && status != RunRunning {
			if status != RunFailed {
				t.Errorf("status = %s, want failed", status)
			}
			if exitCode != 1 {
				t.Errorf("exit code = %d, want 1", exitCode)
			}
			r := store.Get(run.ID)
			if r != nil && !strings.Contains(r.Stderr, "fake failure") {
				t.Errorf("stderr missing fake failure: %q", r.Stderr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run never finished")
}

func TestRunStore_TimeoutMarksTimedOut(t *testing.T) {
	store := NewRunStore()
	runner := &fakeRunner{delay: 100 * time.Millisecond}

	prevTimeout := runTimeout
	runTimeoutMu.Lock()
	runTimeout = 30 * time.Millisecond
	runTimeoutMu.Unlock()
	defer func() {
		runTimeoutMu.Lock()
		runTimeout = prevTimeout
		runTimeoutMu.Unlock()
	}()

	run, err := store.Start(context.Background(), "/tmp", "show", FormInput{}, runner)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, ok := store.StatusSnapshot(run.ID); ok && status != RunRunning {
			if status != RunTimedOut {
				t.Errorf("status = %s, want timed_out", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run never finished")
}

func TestCapWriter_TruncatesAtLimit(t *testing.T) {
	w := &capWriter{limit: 8}
	_, _ = w.Write([]byte("hello-world"))
	_, _ = w.Write([]byte("more"))
	if !strings.Contains(w.String(), "truncated at 1 MiB") {
		t.Errorf("expected truncation marker in %q", w.String())
	}
	if !strings.Contains(w.String(), "hello-wo") {
		t.Errorf("expected first 8 bytes preserved, got %q", w.String())
	}
}

func TestProjectRoot_Absolutizes(t *testing.T) {
	root, err := ProjectRoot(".")
	if err != nil {
		t.Fatalf("ProjectRoot: %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Errorf("ProjectRoot = %q, want absolute", root)
	}
}

func TestIsValidProjectRoot(t *testing.T) {
	dir := t.TempDir()
	if IsValidProjectRoot(dir) {
		t.Fatal("empty dir should not validate as ESB project")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsValidProjectRoot(dir) {
		t.Fatal("dir with go.mod should validate")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guard against accidental fmt import loss.
var _ = fmt.Sprintf
