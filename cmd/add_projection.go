package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var projectionAggregates string

var addProjectionCmd = &cobra.Command{
	Use:   "projection <name>",
	Short: "Add a multi-aggregate projection worker",
	Long: `Generates a projection worker that listens to multiple aggregates.
Use this when one read model is built from events of several aggregate types.

Name must be snake_case. Pass aggregate names as a comma-separated list.

Examples:
  esb add projection sales_report --aggregates order,product
  esb add projection balance_summary --aggregates order,payment`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if projectionAggregates == "" {
			return fmt.Errorf("--aggregates is required")
		}
		name := args[0]
		aggs := strings.Split(projectionAggregates, ",")
		for i, a := range aggs {
			aggs[i] = strings.TrimSpace(a)
		}

		fmt.Printf("Adding projection: %s (aggregates: %s)\n\n", name, strings.Join(aggs, ", "))
		return generator.AddProjection(name, aggs)
	},
}

func init() {
	addProjectionCmd.Flags().StringVar(&projectionAggregates, "aggregates", "", "comma-separated aggregate names (required)")
}
