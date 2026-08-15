package injector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTx_CommitWritesAllStagedFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "routes.txt")
	if err := os.WriteFile(existing, []byte("a\n// marker\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "sub", "new.txt")

	tx := NewTx()
	tx.Create(created, "hello\n")
	if err := tx.InjectAfterMarker(existing, "// marker", "INSERTED"); err != nil {
		t.Fatal(err)
	}
	// Nothing should be on disk yet for the created file.
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatal("created file exists before Commit")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	got, _ := os.ReadFile(existing)
	if string(got) != "a\n// marker\nINSERTED\nb\n" {
		t.Errorf("existing content = %q", got)
	}
	got, _ = os.ReadFile(created)
	if string(got) != "hello\n" {
		t.Errorf("created content = %q", got)
	}
}

func TestTx_MissingMarkerLeavesFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wire.txt")
	original := "package x\n// present\n"
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "new.txt")

	tx := NewTx()
	tx.Create(created, "new content\n")
	if err := tx.InjectAfterMarker(target, "// present", "OK"); err != nil {
		t.Fatal(err)
	}
	// This marker does not exist — the mutation must fail...
	if err := tx.InjectAfterMarker(target, "// missing", "X"); err == nil {
		t.Fatal("expected error for missing marker")
	}
	// ...and because we never Commit, nothing must reach disk.
	got, _ := os.ReadFile(target)
	if string(got) != original {
		t.Errorf("target modified despite no commit: %q", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Error("created file written despite no commit")
	}
}

func TestTx_CommitRejectsInvalidGo(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "bad.go")

	tx := NewTx()
	tx.Create(goFile, "package x\nthis is not go\n")
	err := tx.Commit()
	if err == nil {
		t.Fatal("Commit() = nil, want error for invalid Go")
	}
	// The invalid file must not have been written.
	if _, statErr := os.Stat(goFile); !os.IsNotExist(statErr) {
		t.Error("invalid Go file was written to disk")
	}
}

func TestTx_CommitFormatsGo(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "ok.go")

	tx := NewTx()
	tx.Create(goFile, "package x\nfunc  F( ){}\n")
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	got, _ := os.ReadFile(goFile)
	// gofmt collapses the double space and normalizes the signature.
	if want := "package x\n\nfunc F() {}\n"; string(got) != want {
		t.Errorf("formatted content = %q, want %q", got, want)
	}
}
