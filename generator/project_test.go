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

