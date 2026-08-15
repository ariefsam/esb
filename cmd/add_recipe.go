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
  crud         <name> [field:type ...]   entity with Create/Update/Archive (soft delete)
  ledger       <name>                     append-only account: Open/Deposit/Withdraw/Freeze/Close
  statemachine <name> --states --transitions   guarded lifecycle transitions
  saga         <name>                     orchestration saga: two-step transfer + compensation

Examples:
  esb add recipe crud product name:string price:int64 sku:string
  esb add recipe ledger account
  esb add recipe saga money_transfer
  esb add recipe statemachine order --states placed,paid,shipped,delivered,cancelled \
    --transitions "placed->paid,paid->shipped,shipped->delivered,placed->cancelled,paid->cancelled"`,
	Args: cobra.MinimumNArgs(1),
}

var addRecipeSagaCmd = &cobra.Command{
	Use:   "saga <name>",
	Short: "Orchestration saga: a two-step transfer with compensation",
	Long: `Generates an orchestration saga (process manager) that coordinates a
two-step transfer through a Port interface, compensating on failure:

  - domain aggregate + Requested/Debited/Credited/Completed/Failed/Compensated
  - service with a Transfer(ctx, id, from, to, amount) command; a failed leg is
    a recorded outcome (Failed / Compensated with a source refund), not a Go
    error, so no money is lost
  - a <Name>Port interface (Debit/Credit) plus a log-only stub, wired so the
    project compiles — replace the stub with a real adapter
  - read model + projection worker, Get<Name> / List<Name>sByState queries,
    an HTTP handler, and Given-When-Then scenario tests (happy / debit-fails /
    credit-fails-compensates)

Name must be snake_case.

Examples:
  esb add recipe saga money_transfer`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Scaffolding saga recipe: %s\n\n", name)
		return generator.AddSaga(name)
	},
}

var (
	smStates      string
	smTransitions string
)

var addRecipeStateMachineCmd = &cobra.Command{
	Use:   "statemachine <name> --states <s1,s2,...> --transitions <a->b,b->c,...>",
	Short: "State-machine aggregate with guarded lifecycle transitions",
	Long: `Generates a state-machine aggregate whose state is derived purely from
events, with a transition table that rejects illegal moves:

  - one event per state (<Name><State>) + a transition table
  - service with a guarded Transition(ctx, id, to) command
  - read model + projection worker (current state), queries, HTTP handler
  - Given-When-Then scenario tests (valid + illegal transitions)

Name and states must be snake_case. The first --states entry is the initial
state (the only one a new aggregate may enter).

Example:
  esb add recipe statemachine order \
    --states placed,paid,shipped,delivered,cancelled \
    --transitions "placed->paid,paid->shipped,shipped->delivered,placed->cancelled,paid->cancelled"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if smStates == "" {
			return fmt.Errorf("--states is required")
		}
		name := args[0]
		fmt.Printf("Scaffolding state-machine recipe: %s\n\n", name)
		return generator.AddStateMachine(name, smStates, smTransitions)
	},
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
	addRecipeStateMachineCmd.Flags().StringVar(&smStates, "states", "", "comma-separated snake_case states; first is the initial state (required)")
	addRecipeStateMachineCmd.Flags().StringVar(&smTransitions, "transitions", "", "comma-separated from->to pairs")

	addRecipeCmd.AddCommand(addRecipeCRUDCmd)
	addRecipeCmd.AddCommand(addRecipeLedgerCmd)
	addRecipeCmd.AddCommand(addRecipeStateMachineCmd)
	addRecipeCmd.AddCommand(addRecipeSagaCmd)
}
