package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "esb",
	Short: "Event Sourcing Boilerplate — scaffold Go projects backed by Event Sourcing Builder",
	// Execute prints the error once; cobra would otherwise print it too,
	// followed by the full usage text, burying the actual message.
	SilenceErrors: true,
	SilenceUsage:  true,
}

func Execute() {
	if c, err := rootCmd.ExecuteC(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprintf(os.Stderr, "Run '%s --help' for usage.\n", c.CommandPath())
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(uiCmd)
}
