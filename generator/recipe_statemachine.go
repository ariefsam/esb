package generator

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddStateMachine scaffolds a state-machine aggregate: one event per state, a
// transition table, a generic guarded Transition command, a read model +
// worker, queries, an HTTP handler, and Given-When-Then scenario tests.
// statesCSV is a comma-separated list of snake_case states (the first is the
// initial state); transitionsCSV is a comma-separated list of "from->to"
// pairs. Everything is staged in one injector.Tx.
func AddStateMachine(name, statesCSV, transitionsCSV string) error {
	if err := validateSnakeName("statemachine name", name); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	data, err := buildStateMachineData(moduleName, name, statesCSV, transitionsCSV)
	if err != nil {
		return err
	}

	tx := injector.NewTx()
	var actions []string

	files := []struct {
		tmpl string
		dest string
	}{
		{"recipe/sm_domain.go.tmpl", "domain/" + name + ".go"},
		{"recipe/sm_service.go.tmpl", "service/" + name + ".go"},
		{"recipe/sm_scenario_test.go.tmpl", "service/" + name + "_scenario_test.go"},
		{"recipe/sm_row.go.tmpl", "projection/" + name + "_row.go"},
		{"recipe/sm_worker.go.tmpl", "projection/" + name + "_worker.go"},
		{"recipe/sm_query.go.tmpl", "projection/" + name + "_query.go"},
		{"recipe/sm_handler.go.tmpl", "server/handler/" + name + ".go"},
	}
	for _, f := range files {
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		actions = append(actions, "  create  "+f.dest)
	}

	if err := ensureResponseHelper(tx, &actions); err != nil {
		return err
	}
	if err := injectAutoMigrateModel(tx, data.NamePascal+"Row", &actions); err != nil {
		return err
	}

	if err := wireAggregateSlice(tx, moduleName, data.NamePascal, &actions); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nState machine %q ready: %d states, guarded Transition command, read model, and scenario tests.\n",
		name, len(data.States))
	return nil
}

// buildStateMachineData parses and validates the states/transitions and
// precomputes everything the templates need.
func buildStateMachineData(moduleName, name, statesCSV, transitionsCSV string) (StateMachineData, error) {
	var data StateMachineData
	pascal := naming.ToPascalCase(name)

	rawStates := splitCSV(statesCSV)
	if len(rawStates) == 0 {
		return data, fmt.Errorf("--states must list at least one state")
	}
	stateSet := map[string]bool{}
	for _, s := range rawStates {
		if err := validateSnakeName("state", s); err != nil {
			return data, err
		}
		if stateSet[s] {
			return data, fmt.Errorf("duplicate state %q", s)
		}
		stateSet[s] = true
		data.States = append(data.States, SMState{
			Raw:    s,
			Pascal: naming.ToPascalCase(s),
			Event:  pascal + naming.ToPascalCase(s),
		})
	}

	// Parse transitions into an ordered map keyed by the "from" state.
	tos := map[string][]string{}
	for _, tr := range splitCSV(transitionsCSV) {
		parts := strings.SplitN(tr, "->", 2)
		if len(parts) != 2 {
			return data, fmt.Errorf("invalid transition %q — expected from->to", tr)
		}
		from, to := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !stateSet[from] {
			return data, fmt.Errorf("transition %q references unknown state %q", tr, from)
		}
		if !stateSet[to] {
			return data, fmt.Errorf("transition %q references unknown state %q", tr, to)
		}
		tos[from] = appendUnique(tos[from], to)
	}

	constName := func(raw string) string { return pascal + "State" + naming.ToPascalCase(raw) }
	for _, s := range rawStates {
		if len(tos[s]) == 0 {
			continue // terminal state — no outgoing transitions
		}
		grp := SMFromTransitions{From: constName(s)}
		for _, to := range tos[s] {
			grp.Tos = append(grp.Tos, constName(to))
		}
		data.TransitionGroups = append(data.TransitionGroups, grp)
	}

	data.ModuleName = moduleName
	data.PackageName = naming.PackageName(moduleName)
	data.Name = name
	data.NamePascal = pascal
	data.NameKebab = naming.ToKebabCase(name)
	data.Receiver = strings.ToLower(pascal[:1])
	data.TableName = naming.ToPlural(name)
	data.InitialState = rawStates[0]
	data.InitialEvent = pascal + naming.ToPascalCase(rawStates[0])

	if len(rawStates) > 1 {
		data.HasSecond = true
		data.SecondState = rawStates[1]
	}
	initialTos := tos[rawStates[0]]
	if len(initialTos) > 0 {
		data.HasValidTo = true
		data.ValidTo = initialTos[0]
		data.ValidEvent = pascal + naming.ToPascalCase(initialTos[0])
	}
	initialToSet := map[string]bool{}
	for _, to := range initialTos {
		initialToSet[to] = true
	}
	for _, s := range rawStates[1:] {
		if !initialToSet[s] {
			data.HasInvalidTo = true
			data.InvalidTo = s
			break
		}
	}

	return data, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
