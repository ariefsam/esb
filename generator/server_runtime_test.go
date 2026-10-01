package generator

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGeneratedServer_TimeoutsAndGracefulShutdown builds a generated app and
// runs it for real. It asserts that (1) a client that never finishes its
// request headers is disconnected by ReadHeaderTimeout instead of holding the
// connection forever (slowloris), and (2) SIGINT drains and exits cleanly
// with the projection worker running.
func TestGeneratedServer_TimeoutsAndGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatalf("AddAggregate(order) error = %v", err)
	}

	bin := filepath.Join(dir, "app")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build generated app: %v\n%s", err, out)
	}

	addr := freeAddr(t)
	var logs bytes.Buffer
	app := exec.Command(bin)
	app.Dir = dir
	app.Env = append(os.Environ(),
		"EVENT_STORE_MODE=embedded",
		"EVENT_STORE_DSN=",
		"DB_DSN="+filepath.Join(dir, "app.db"),
		"ADDR="+addr,
	)
	app.Stdout, app.Stderr = &logs, &logs
	if err := app.Start(); err != nil {
		t.Fatalf("start generated app: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- app.Wait() }()
	t.Cleanup(func() {
		if app.ProcessState == nil {
			app.Process.Kill() //nolint:errcheck
		}
	})

	waitForListener(t, addr, exited, &logs)

	// (1) Slowloris: send an incomplete request and never finish it.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatalf("write partial request: %v", err)
	}
	start := time.Now()
	conn.SetReadDeadline(start.Add(12 * time.Second)) //nolint:errcheck
	_, err = conn.Read(make([]byte, 512))
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("server kept a half-sent request open for %s: no ReadHeaderTimeout", time.Since(start).Round(time.Second))
	}

	// (2) Graceful shutdown.
	if err := app.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("app did not exit cleanly on SIGINT: %v\n%s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("app still running 15s after SIGINT\n%s", logs.String())
	}
}

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// waitForListener polls until addr accepts connections or the app exits.
func waitForListener(t *testing.T, addr string, exited <-chan error, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("app exited during startup: %v\n%s", err, logs.String())
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("app never listened on %s\n%s", addr, logs.String())
}
