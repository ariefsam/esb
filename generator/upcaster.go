package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddUpcaster registers an event upcaster: a function that migrates a stored
// event's JSON payload from an older shape to the current one on read.
// Aggregate Replay routes every stored event through the registered chain
// before Apply, so Apply only ever sees the latest shape. The generated
// function is an identity stub for you to fill in.
func AddUpcaster(aggregateName, eventName string) error {
	if err := validateSnakeName("aggregate name", aggregateName); err != nil {
		return err
	}
	if err := validatePascalName("event name", eventName); err != nil {
		return err
	}
	if _, err := ReadModuleName(); err != nil {
		return err
	}

	tx := injector.NewTx()
	var actions []string

	// Ensure the upcaster registry exists (projects generated before upcaster
	// support won't have it).
	if _, statErr := os.Stat(filepath.FromSlash("domain/upcast.go")); os.IsNotExist(statErr) {
		content, err := renderTemplate("domain_upcast.go.tmpl", nil)
		if err != nil {
			return fmt.Errorf("generate domain/upcast.go: %w", err)
		}
		tx.Create("domain/upcast.go", content)
		actions = append(actions, "  create  domain/upcast.go")
	}

	dest := "domain/upcast_" + aggregateName + "_" + naming.ToSnakeCase(eventName) + ".go"
	if _, statErr := os.Stat(filepath.FromSlash(dest)); statErr == nil {
		return fmt.Errorf("upcaster for %s already exists (%s)", eventName, dest)
	}
	content, err := renderTemplate("domain_upcaster_fn.go.tmpl", struct{ EventName string }{eventName})
	if err != nil {
		return fmt.Errorf("generate %s: %w", dest, err)
	}
	tx.Create(dest, content)
	actions = append(actions, "  create  "+dest)

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nUpcaster for %q registered. Edit %s to transform old payloads; it runs on every Replay/load.\n", eventName, dest)
	return nil
}
