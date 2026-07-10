package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var handlerAggregate string

var addHandlerCmd = &cobra.Command{
	Use:   "handler <name>",
	Short: "Add an HTTP handler skeleton",
	Long: `Generates server/handler/<name>.go and updates server/routes.go and wire/wire.go.

Examples:
  esb add handler place_order --aggregate order
  esb add handler user_auth --aggregate user`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if handlerAggregate == "" {
			return fmt.Errorf("--aggregate is required")
		}
		name := args[0]
		fmt.Printf("Adding handler: %s (aggregate: %s)\n\n", name, handlerAggregate)
		return generator.AddHandler(name, handlerAggregate)
	},
}

func init() {
	addHandlerCmd.Flags().StringVar(&handlerAggregate, "aggregate", "", "aggregate this handler belongs to (required)")
}
