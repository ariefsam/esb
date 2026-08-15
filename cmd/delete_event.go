package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var deleteEventCmd = &cobra.Command{
	Use:   "event <aggregate> <EventName>",
	Short: "Remove an event from an aggregate (code only; stored data untouched)",
	Long: `Removes an event's generated code from an aggregate — its struct, constructor,
Apply() case, and projection worker case — as the inverse of 'esb add event'.
It is AST-based and atomic (all-or-nothing).

This does NOT delete any stored event data. If events of this type were ever
stored, deleting the definition means future replays silently ignore them, so
check your event store first (the UI does this before offering a Delete button).

An upcaster that targets the event blocks removal; remove it first.

EventName must be PascalCase.

Examples:
  esb delete event order OrderPlaced`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		aggregate, event := args[0], args[1]
		fmt.Printf("Removing event %s from aggregate %s\n\n", event, aggregate)
		return generator.RemoveEvent(aggregate, event)
	},
}

func init() {
	deleteCmd.AddCommand(deleteEventCmd)
}
