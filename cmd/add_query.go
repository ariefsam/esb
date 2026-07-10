package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var queryAggregate string

var addQueryCmd = &cobra.Command{
	Use:   "query <name>",
	Short: "Add a query function to projection/query.go",
	Long: `Appends a typed query function stub to projection/query.go.

Examples:
  esb add query orders_by_buyer --aggregate order
  esb add query user_by_email --aggregate user`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if queryAggregate == "" {
			return fmt.Errorf("--aggregate is required")
		}
		name := args[0]
		fmt.Printf("Adding query: %s (aggregate: %s)\n\n", name, queryAggregate)
		return generator.AddQuery(name, queryAggregate)
	},
}

func init() {
	addQueryCmd.Flags().StringVar(&queryAggregate, "aggregate", "", "aggregate this query reads from (required)")
}
