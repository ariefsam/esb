package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var addEventCmd = &cobra.Command{
	Use:   "event <aggregate> <EventName> [field:type ...]",
	Short: "Add an event to an existing aggregate",
	Long: `Injects an event struct, Apply() case, constructor, and
projection worker case into the aggregate's domain and projection files.

EventName must be PascalCase. Fields use field_name:go_type syntax.
Supported types: string, int64, int32, int, float64, float32, bool.

Examples:
  esb add event user UserRegistered email:string display_name:string
  esb add event order OrderPlaced amount:int64 currency:string buyer_id:string
  esb add event order OrderCancelled reason:string`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		aggregateName := args[0]
		eventName := args[1]
		fieldArgs := args[2:]

		fields, err := generator.ParseFields(fieldArgs)
		if err != nil {
			return err
		}

		fmt.Printf("Adding event %s to aggregate %s\n\n", eventName, aggregateName)
		return generator.AddEvent(aggregateName, eventName, fields)
	},
}
