package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var addUpcasterCmd = &cobra.Command{
	Use:   "upcaster <aggregate> <EventName>",
	Short: "Register an event upcaster (migrate old event payloads on read)",
	Long: `Generates an upcaster function for an event so stored payloads in an older
shape are migrated to the current shape when replayed. Aggregate Replay routes
every stored event through the registered upcaster chain before Apply, so your
Apply only ever deals with the latest shape.

The generated function is an identity stub — edit it to rename/split/compute
fields. Register several (v1->v2->v3) by running the command again as the
schema evolves.

EventName must be PascalCase.

Examples:
  esb add upcaster order OrderPlaced
  esb add upcaster account AccountOpened`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		aggregate, event := args[0], args[1]
		fmt.Printf("Adding upcaster for %s (aggregate %s)\n\n", event, aggregate)
		return generator.AddUpcaster(aggregate, event)
	},
}

func init() {
	addCmd.AddCommand(addUpcasterCmd)
}
