package generator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMakeKeygen_PrivateKeyIsOwnerOnly runs the generated `make keygen` under
// a permissive umask and asserts private.pem is 0600, and that a second run
// refuses to overwrite the existing key.
func TestMakeKeygen_PrivateKeyIsOwnerOnly(t *testing.T) {
	for _, tool := range []string{"make", "openssl", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}

	dir := filepath.Join(t.TempDir(), "app")
	if err := InitProject("example.com/app", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	keygen := func() ([]byte, error) {
		cmd := exec.Command("sh", "-c", "umask 022 && make keygen")
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}

	if out, err := keygen(); err != nil {
		t.Fatalf("make keygen: %v\n%s", err, out)
	}
	keyPath := filepath.Join(dir, "private.pem")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("private.pem mode = %o, want 600", perm)
	}

	before, _ := os.ReadFile(keyPath)
	if out, err := keygen(); err == nil {
		t.Fatalf("second make keygen succeeded, want refusal to overwrite:\n%s", out)
	}
	after, _ := os.ReadFile(keyPath)
	if !bytes.Equal(before, after) {
		t.Fatal("second make keygen overwrote the existing private.pem")
	}
}
