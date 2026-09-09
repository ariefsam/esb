package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/inspector"
)

var showStorageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Print event store detail: mode, per-aggregate counts, locks, migration state",
	Long: `Detailed view of the project's event store — mode, DSN/ESB URL,
per-aggregate event and snapshot counts, held locks, and the last recorded
migration. This is the CLI equivalent of the 'esb ui' /storage page; the
one-line summary embedded in plain 'esb show' stays a single screen on
purpose, so this command is where the detail lives.

Examples:
  esb show storage`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := inspector.PrintStorageDetail(os.Stdout, "."); err != nil {
			return fmt.Errorf("print storage: %w", err)
		}
		return nil
	},
}

func init() {
	showCmd.AddCommand(showStorageCmd)
}
