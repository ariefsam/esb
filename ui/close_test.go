package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// blockingRunner simulates a long generator run: it blocks until its context
// is cancelled, then returns the way a killed child process does.
type blockingRunner struct{ started chan struct{} }

func (b blockingRunner) Run(ctx context.Context, projectRoot string, argv []string, stdout, stderr io.Writer) (int, error) {
	close(b.started)
	<-ctx.Done()
	return -1, nil
}

// TestServerClose_CancelsRunningCommand guards the shutdown path: runs used
// to be parented on context.Background(), so a command kept running for up
// to runTimeout after `esb ui` was stopped.
func TestServerClose_CancelsRunningCommand(t *testing.T) {
	runner := blockingRunner{started: make(chan struct{})}
	srv := newTestServer(t, runner)

	run, err := srv.RunStore().Start(srv.runContext(), srv.ProjectRoot(), "show", FormInput{}, runner)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	closed := make(chan struct{})
	go func() {
		srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the running command")
	}

	status, _, ok := srv.RunStore().StatusSnapshot(run.ID)
	if !ok || status != RunFailed {
		t.Fatalf("status = %v (found %v), want %v", status, ok, RunFailed)
	}
	if got := srv.RunStore().Get(run.ID).Err; !strings.Contains(got, "cancelled") {
		t.Fatalf("run error = %q, want a cancellation message", got)
	}
}
