package cmd

import (
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <subcommand>",
	Short: "Remove a component from an existing project",
}

func init() {
	rootCmd.AddCommand(deleteCmd)
}
