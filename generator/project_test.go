package generator

import (
	"os/exec"
	"path/filepath"
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
