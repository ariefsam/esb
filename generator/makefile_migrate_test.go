package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMakefile_MigrateTargetsCallEsbCLI guards the generated migrate targets.
// They used to run `go run . migrate ...`, which starts the generated app's
// HTTP server (it ignores arguments) instead of migrating; read config from
// the shell only (make does not load .env); and always pass --force, which
// disabled the CLI's refusal to overwrite a non-empty SQLite target.
func TestMakefile_MigrateTargetsCallEsbCLI(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "app")
	if err := InitProject("example.com/app", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	env := "EVENT_STORE_DSN=\nESB_URL=http://esb.test:8080\nTENANT_ID=t1\nPROJECT_ID=p1\nDB_DSN=app-data.db\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}

	dryRun := func(args ...string) string {
		cmd := exec.Command("make", append([]string{"-n"}, args...)...)
		cmd.Dir = dir
		// Keep the developer's shell from leaking into the expectation.
		cmd.Env = append(os.Environ(), "ESB_URL=", "TENANT_ID=", "PROJECT_ID=", "DB_DSN=", "EVENT_STORE_DSN=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	flags := `--source "app-data.db" --esb-url "http://esb.test:8080" --tenant "t1" --project "p1"`
	cases := map[string]string{
		"migrate-to-esb":              `esb migrate to-esb ` + flags,
		"migrate-to-embedded":         `esb migrate to-embedded ` + flags,
		"migrate-to-embedded FORCE=1": `esb migrate to-embedded ` + flags + ` --force`,
	}
	for args, want := range cases {
		if got := dryRun(strings.Fields(args)...); got != want {
			t.Errorf("make %s runs:\n  %s\nwant:\n  %s", args, got, want)
		}
	}
}
