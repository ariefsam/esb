package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUI_RegistersCommand(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "ui" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ui subcommand not registered on rootCmd")
	}
}

func TestUI_DefaultAddressRejectsNonESBDir(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	prev := uiAddr
	uiAddr = "127.0.0.1:8787"
	defer func() { uiAddr = prev }()

	err := uiCmd.RunE(uiCmd, []string{})
	if err == nil {
		t.Fatal("expected error for non-ESB project root")
	}
	if !strings.Contains(err.Error(), "bukan proyek ESB") {
		t.Errorf("error = %v, want 'bukan proyek ESB' prefix", err)
	}
}

func TestUI_ValidatesProjectRootBeforeListen(t *testing.T) {
	// The cmd must surface a project-validation error before attempting
	// to bind a TCP socket, otherwise the user gets a confusing
	// "address already in use" instead of the real cause.
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	prev := uiAddr
	uiAddr = "127.0.0.1:1"
	defer func() { uiAddr = prev }()

	err := uiCmd.RunE(uiCmd, []string{})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("project validation should fail before listen: %v", err)
	}
	if !strings.Contains(err.Error(), "bukan proyek ESB") {
		t.Fatalf("error = %v, want 'bukan proyek ESB'", err)
	}
}

func TestUI_BuildsServerWithValidProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// We don't actually start the listener (that would block the
	// test until shutdown), but we make sure RunE doesn't reject the
	// project root. Use a port we expect to fail to listen so the
	// RunE returns a listen error rather than a project error.
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	prev := uiAddr
	uiAddr = "127.0.0.1:1"
	defer func() { uiAddr = prev }()

	err := uiCmd.RunE(uiCmd, []string{})
	if err == nil {
		t.Fatal("expected error from listen on port 1")
	}
	if strings.Contains(err.Error(), "bukan proyek ESB") {
		t.Fatalf("RunE should accept the project, got: %v", err)
	}
}