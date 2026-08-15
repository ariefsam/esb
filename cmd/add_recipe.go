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
  crud <name> [field:type ...]   entity with Create/Update/Archive (soft delete)

Examples:
  esb add recipe crud product name:string price:int64 sku:string
  esb add recipe crud customer email:string display_name:string`,
	Args: cobra.MinimumNArgs(1),
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
}
