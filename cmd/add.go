package cmd

import (
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <subcommand>",
	Short: "Add a component to an existing project",
}

func init() {
	addCmd.AddCommand(addAggregateCmd)
	addCmd.AddCommand(addEventCmd)
	addCmd.AddCommand(addProjectionCmd)
	addCmd.AddCommand(addHandlerCmd)
	addCmd.AddCommand(addQueryCmd)
}
