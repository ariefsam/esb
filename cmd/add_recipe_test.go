package cmd

import "testing"

// TestAddRecipe_AllSubcommandsRegistered guards that every recipe stays wired
// under `esb add recipe` — so `esb add recipe --list` (which derives the list
// from the registered subcommands) never silently drops one.
func TestAddRecipe_AllSubcommandsRegistered(t *testing.T) {
	want := []string{"crud", "ledger", "statemachine", "saga", "outbox"}
	got := map[string]bool{}
	for _, c := range addRecipeCmd.Commands() {
		got[c.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("recipe subcommand %q not registered under `add recipe`", name)
		}
	}
}
