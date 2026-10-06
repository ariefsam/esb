package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var (
	updateClientMode   string
	updateClientDryRun bool
	updateClientBuild  bool
)

var updateClientCmd = &cobra.Command{
	Use:   "update-client",
	Short: "Bring the event store client up to this esb version, keeping hand edits",
	Long: `Updates the event store client of the project in the current directory
to the templates of this esb version — the embedded SQLite store, the ESB
server client, or both:

  eventstore/client.go               ESB server   (--mode esb)
  repository/eventstore_adapter.go   ESB server   (--mode esb)
  eventstore/local_store.go          embedded     (--mode embedded)
  repository/local_adapter.go        embedded     (--mode embedded)
  eventstore/fake_store.go           every mode
  domain/projection_wait.go          every mode   (StoreAndWaitProjectionWorker)
  projection/wait.go                 every mode   (CursorWaiter)
  service/*.go, projection/*.go      every mode   (storeAndWait, WaitPast only)

A missing file is created. An existing one only gains the top-level
declarations it does not have yet, with the imports they need. Matching is
by name through the Go AST — a function by name, a method by receiver and
name, a type, var or const by name — so nothing the project declares is
changed, moved or removed, and hand edits survive. A declaration the
project has but with different code is listed as "differs" and left alone:
compare it with the template yourself.

Services and projection workers generated before storeAndWait only gain
that feature's methods, never other template declarations: each
...ProjectionWorker without WaitPast gets one, built from its own cursor
name and FetchAll aggregates; each service gets storeEvent and storeAndWait
only while its store is still the one esb generated. A store changed by
hand is reported with "!" and left alone, since storeEvent repeats the
generated body and would bypass the change.

Afterwards it runs 'go build ./...' so a merge that does not compile is
reported straight away (--build=false to skip). Run with --dry-run first to
see what would change.

Examples:
  esb update-client --dry-run
  esb update-client
  esb update-client --mode embedded`,
	Args: cobra.NoArgs,
	RunE: runUpdateClient,
}

func init() {
	updateClientCmd.Flags().StringVar(&updateClientMode, "mode", generator.ClientModeAll, "which client to update: all, embedded or esb")
	updateClientCmd.Flags().BoolVar(&updateClientDryRun, "dry-run", false, "report what would change without writing")
	updateClientCmd.Flags().BoolVar(&updateClientBuild, "build", true, "run 'go build ./...' after writing")
	rootCmd.AddCommand(updateClientCmd)
}

func runUpdateClient(cmd *cobra.Command, args []string) error {
	changes, err := generator.UpdateClient(generator.UpdateClientOptions{Mode: updateClientMode, DryRun: updateClientDryRun})
	if err != nil {
		return err
	}
	verb := map[bool]string{true: "would ", false: ""}[updateClientDryRun]
	written, differs := 0, 0
	for _, c := range changes {
		switch {
		case c.Created:
			fmt.Printf("  %screate  %s\n", verb, c.Path)
		case len(c.Added) > 0:
			fmt.Printf("  %supdate  %s\n", verb, c.Path)
		default:
			fmt.Printf("  ok      %s\n", c.Path)
		}
		for _, a := range c.Added {
			fmt.Printf("            + %s\n", shortDecl(a))
		}
		for _, i := range c.Imports {
			fmt.Printf("            + import %q\n", i)
		}
		for _, d := range c.Differs {
			fmt.Printf("            ~ %s (differs from the template; left as it is)\n", shortDecl(d))
		}
		for _, s := range c.Skipped {
			fmt.Printf("            ! %s\n", s)
		}
		if c.Changed() {
			written++
		}
		differs += len(c.Differs)
	}
	fmt.Println()
	switch {
	case written == 0:
		fmt.Println("The client is up to date.")
	case updateClientDryRun:
		fmt.Printf("%d file(s) would change. Run without --dry-run to write them.\n", written)
	default:
		fmt.Printf("%d file(s) changed.\n", written)
	}
	if differs > 0 {
		fmt.Printf("%d declaration(s) differ from the template and were not touched.\n", differs)
	}

	if updateClientDryRun || written == 0 || !updateClientBuild {
		return nil
	}
	fmt.Println("\n  run     go build ./...")
	build := exec.Command("go", "build", "./...")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("the updated project does not build: fix it, or restore the files with git: %w", err)
	}
	return nil
}

// shortDecl keeps a declaration keyed by its code (var _ = …, func init)
// to one readable line.
func shortDecl(key string) string {
	line, _, _ := strings.Cut(key, "\n")
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
}
