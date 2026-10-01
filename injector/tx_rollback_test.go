package injector

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTx_CommitRollsBackOnWriteFailure: validation used to be all-or-nothing
// but the writes were not — a failure on the third file left the first two
// already renamed into place.
func TestTx_CommitRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(existing, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "new.txt")
	// "blocker" is a regular file, so writing blocker/x.txt must fail.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0644); err != nil {
		t.Fatal(err)
	}

	tx := NewTx()
	tx.Create(created, "new\n")
	if err := tx.Append(existing, "appended\n"); err != nil {
		t.Fatal(err)
	}
	tx.Create(filepath.Join(blocker, "x.txt"), "unreachable\n")

	if err := tx.Commit(); err == nil {
		t.Fatal("Commit() = nil, want write error")
	}
	if got, _ := os.ReadFile(existing); string(got) != "original\n" {
		t.Errorf("existing file = %q after failed commit, want original content", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("created file still exists after failed commit (stat err = %v)", err)
	}
}

// TestTx_CommitPreservesFileMode: atomicWrite's temp file is 0600, so every
// file touched by a generator step used to end up owner-only.
func TestTx_CommitPreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(existing, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0755); err != nil { // bypass umask
		t.Fatal(err)
	}
	created := filepath.Join(dir, "new.txt")

	tx := NewTx()
	if err := tx.Append(existing, "echo hi\n"); err != nil {
		t.Fatal(err)
	}
	tx.Create(created, "new\n")
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	for path, want := range map[string]os.FileMode{existing: 0755, created: 0644} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", filepath.Base(path), got, want)
		}
	}
}
