package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/ui"
)

var (
	uiAddr   string
	uiNoOpen bool
)

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Start the local admin UI for the current ESB project",
	Long: `Serve a local-only web dashboard for the ESB project in the current
working directory. The UI parses the generated project files, lets you
browse the domain at a glance, and runs allow-listed esb commands
against the project root.

Defaults to http://127.0.0.1:8787. Pass --addr to change the bind
address. The server only writes back to the project directory; it does
not touch the event store, scheduler, or external services.

Examples:
  esb ui
  esb ui --addr 127.0.0.1:9001
  esb ui --no-open`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := ui.ProjectRoot(".")
		if err != nil {
			return err
		}
		if !ui.IsValidProjectRoot(root) {
			return fmt.Errorf("bukan proyek ESB — jalankan 'esb init' di %s dulu", root)
		}

		srv, err := ui.NewServer(ui.Options{ProjectRoot: root})
		if err != nil {
			return err
		}

		ln, err := net.Listen("tcp", uiAddr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", uiAddr, err)
		}

		httpSrv := &http.Server{
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
		}

		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			if !uiNoOpen {
				fmt.Printf("esb ui listening on http://%s\n", ln.Addr().String())
			}
			fmt.Printf("project root: %s\n", root)
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
				return
			}
			errCh <- nil
		}()

		select {
		case <-ctx.Done():
			fmt.Println("\nesb ui shutting down...")
		case err := <-errCh:
			if err != nil {
				cancel()
				return err
			}
			return nil
		}

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		// Drain the serve goroutine so its error (if any) is not lost.
		select {
		case err := <-errCh:
			return err
		case <-time.After(time.Second):
			return nil
		}
	},
}

func init() {
	uiCmd.Flags().StringVar(&uiAddr, "addr", "127.0.0.1:8787", "address to bind the UI to (host:port)")
	uiCmd.Flags().BoolVar(&uiNoOpen, "no-open", false, "do not print the local URL after start")
}
