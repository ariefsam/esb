package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var addIdempotencyCmd = &cobra.Command{
	Use:   "idempotency",
	Short: "Generate a reusable idempotency guard for command handling",
	Long: `Generates service/idempotency.go — AlreadyProcessed and Once helpers backed
by the event stream's IdempotencyKey — so commands can be made safe to retry.

Wrap a command in service.Once(ctx, s.eventRepo, <AggregateName>, id, commandID,
func() error { ... }) and store the event with IdempotencyKey = commandID; a
duplicate submission is then recognized and skipped. Generated once per project.

Examples:
  esb add idempotency`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Print("Adding idempotency guard\n\n")
		return generator.AddIdempotency()
	},
}

func init() {
	addCmd.AddCommand(addIdempotencyCmd)
}
