package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
	"github.com/ariefsam/esb/inspector"
)

var deleteEventForce bool

var deleteEventCmd = &cobra.Command{
	Use:   "event <aggregate> <EventName>",
	Short: "Remove an event from an aggregate (code only; stored data untouched)",
	Long: `Removes an event's generated code from an aggregate — its struct, constructor,
Apply() case, and projection worker case — as the inverse of 'esb add event'.
It is AST-based and atomic (all-or-nothing).

This does NOT delete any stored event data. Before touching any files it checks
the embedded event store for rows of this type — same check the UI runs before
offering its Delete button — and refuses to proceed without --force when:
  - stored rows exist (deleting the definition means future replays silently
    ignore them), or
  - the count cannot be verified (esb-server mode, or no local SQLite file).

An upcaster that targets the event blocks removal; remove it first.

EventName must be PascalCase.

Examples:
  esb delete event order OrderPlaced
  esb delete event order OrderPlaced --force`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		aggregate, event := args[0], args[1]

		storage := inspector.ScanStorage(".")
		if warning, requireAck := deleteEventWarning(storage, aggregate, event); requireAck {
			if !deleteEventForce {
				return fmt.Errorf("%s\nrerun with --force to proceed anyway", warning)
			}
			fmt.Println(warning)
		}

		fmt.Printf("Removing event %s from aggregate %s\n\n", event, aggregate)
		return generator.RemoveEvent(aggregate, event)
	},
}

func init() {
	deleteEventCmd.Flags().BoolVar(&deleteEventForce, "force", false, "proceed even when stored events exist or cannot be verified")
	deleteCmd.AddCommand(deleteEventCmd)
}

// deleteEventWarning mirrors the UI's pre-delete verification
// (ui/handlers.go handleDeleteEvent): warn when stored events of this type
// exist, or when we cannot verify one way or the other. requireAck is true
// in both cases — only a verified zero count is safe to proceed on silently.
func deleteEventWarning(storage inspector.StorageInfo, aggregate, event string) (warning string, requireAck bool) {
	canVerify := storage.Mode == inspector.StorageModeEmbedded && storage.HasSQLite
	if !canVerify {
		return fmt.Sprintf("cannot verify stored event count for %s.%s (esb-server mode or no local SQLite file) — "+
			"if events of this type were ever stored, deleting the definition means future replays will silently ignore them", aggregate, event), true
	}
	if count := storage.EventCount(aggregate, event); count > 0 {
		return fmt.Sprintf("%d stored %s event(s) exist for aggregate %q — "+
			"deleting the code definition does NOT remove stored data; future replays will silently ignore them", count, event, aggregate), true
	}
	return "", false
}
