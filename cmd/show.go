package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/inspector"
)

var showCmd = &cobra.Command{
	Use:   "show [aggregate-name]",
	Short: "Print a one-screen summary of the current ESB project",
	Long: `Read-only summary of an ESB project: aggregates, handlers, projection
workers, storage, and wire graph. Optionally focus on a single aggregate
to filter the output.

Examples:
  esb show
  esb show order
  esb show bank_account`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		focus := ""
		if len(args) == 1 {
			focus = args[0]
		}

		m, err := inspector.Scan(".")
		if err != nil {
			return err
		}

		if err := inspector.Print(os.Stdout, m, focus); err != nil {
			return fmt.Errorf("print summary: %w", err)
		}
		return nil
	},
}
