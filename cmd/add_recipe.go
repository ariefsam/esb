package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/generator"
)

var addRecipeCmd = &cobra.Command{
	Use:   "recipe <kind> ...",
	Short: "Scaffold a whole event-sourcing pattern (CRUD, …)",
	Long: `Recipes generate a complete vertical slice for a common event-sourcing
pattern instead of a single component. Everything is written in one atomic
step — if any part fails, nothing is written.

Available recipes:
  crud   <name> [field:type ...]   entity with Create/Update/Archive (soft delete)
  ledger <name>                    append-only account: Open/Deposit/Withdraw/Freeze/Close

Examples:
  esb add recipe crud product name:string price:int64 sku:string
  esb add recipe ledger account`,
	Args: cobra.MinimumNArgs(1),
}

var addRecipeLedgerCmd = &cobra.Command{
	Use:   "ledger <name>",
	Short: "Ledger account: Open/Deposit/Withdraw/Freeze/Close with a non-negative-balance invariant",
	Long: `Generates a double-entry-style ledger account aggregate:

  - domain aggregate + Opened/Deposited/Withdrawn/Frozen/Closed events
  - service with Open/Deposit/Withdraw/Freeze/Close commands, enforcing a
    non-negative balance (money is int64 minor units, never float)
  - balance read model + append-only statement journal (idempotent projection)
  - Get<Name>Balance / List<Name>Entries queries
  - write-side HTTP handlers
  - Given-When-Then scenario tests, including a concurrent no-double-spend test

Name must be snake_case.

Examples:
  esb add recipe ledger account
  esb add recipe ledger wallet`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Scaffolding ledger recipe: %s\n\n", name)
		return generator.AddLedger(name)
	},
}

var addRecipeCRUDCmd = &cobra.Command{
	Use:   "crud <name> [field:type ...]",
	Short: "Entity CRUD: Create/Update/Archive events, commands, projection, queries, handlers, tests",
	Long: `Generates a full CRUD slice for an entity aggregate:

  - domain aggregate + Created/Updated/Archived events (Archived = soft delete)
  - service with Create/Update/Archive commands (invariants on the aggregate)
  - read-model row + projection worker
  - List<Name>s / Get<Name> queries
  - write-side HTTP handlers (Create/Update/Archive)
  - Given-When-Then scenario tests (happy path + failure paths)

Name must be snake_case. Fields use field_name:go_type syntax.
Supported types: string, int64, int32, int, float64, float32, bool.

Examples:
  esb add recipe crud product name:string price:int64 sku:string
  esb add recipe crud customer email:string display_name:string`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fields, err := generator.ParseFields(args[1:])
		if err != nil {
			return err
		}
		fmt.Printf("Scaffolding CRUD recipe: %s\n\n", name)
		return generator.AddCRUD(name, fields)
	},
}

func init() {
	addRecipeCmd.AddCommand(addRecipeCRUDCmd)
	addRecipeCmd.AddCommand(addRecipeLedgerCmd)
}
