package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var addAggregateCmd = &cobra.Command{
	Use:   "aggregate <name>",
	Short: "Add a new aggregate to the project",
	Long: `Generates domain/<name>.go, service/<name>.go,
projection/<name>_row.go, and projection/<name>_worker.go.
Also updates projection/db.go, wire/wire.go, and main.go.

Name must be snake_case. Run from the project root.

Examples:
  esb add aggregate user
  esb add aggregate bank_account`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Adding aggregate: %s\n\n", name)
		return generator.AddAggregate(name)
	},
}
