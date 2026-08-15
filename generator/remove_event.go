package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// RemoveEvent deletes an event's generated code from an aggregate: its struct,
// its New<Event>Event constructor (when present), its Apply() case, and its
// projection worker case. It is the inverse of AddEvent, done AST-based and
// staged in one injector.Tx so the whole removal is atomic and gofmt-valid.
//
// It refuses to run when an upcaster targets the event (that file would no
// longer compile, and removing it automatically risks silent data-migration
// loss) — remove the upcaster first. It does NOT touch any stored event data;
// callers that care about existing history must check before calling.
func RemoveEvent(aggregateName, eventName string) error {
	if err := validateSnakeName("aggregate name", aggregateName); err != nil {
		return err
	}
	if err := validatePascalName("event name", eventName); err != nil {
		return err
	}
	if _, err := ReadModuleName(); err != nil {
		return err
	}

	domainFile := "domain/" + aggregateName + ".go"
	workerFile := "projection/" + aggregateName + "_worker.go"

	// Guard: an upcaster for this event references the struct we're about to
	// remove; block until it's removed manually.
	upcasterFile := "domain/upcast_" + aggregateName + "_" + naming.ToSnakeCase(eventName) + ".go"
	if _, err := os.Stat(filepath.FromSlash(upcasterFile)); err == nil {
		return fmt.Errorf("an upcaster references %s (%s); remove it first with care, then retry", eventName, upcasterFile)
	}

	tx := injector.NewTx()

	hasStruct, err := tx.HasTypeDecl(domainFile, eventName)
	if err != nil {
		return err
	}
	hasApply, err := tx.HasSwitchCase(domainFile, "Apply", eventName)
	if err != nil {
		return err
	}
	if !hasStruct && !hasApply {
		return fmt.Errorf("event %s not found in aggregate %s", eventName, aggregateName)
	}

	if hasStruct {
		if err := tx.RemoveTypeDecl(domainFile, eventName); err != nil {
			return err
		}
	}
	if has, err := tx.HasFuncDecl(domainFile, "New"+eventName+"Event"); err != nil {
		return err
	} else if has {
		if err := tx.RemoveFuncDecl(domainFile, "New"+eventName+"Event"); err != nil {
			return err
		}
	}
	if hasApply {
		if err := tx.RemoveSwitchCase(domainFile, "Apply", eventName); err != nil {
			return err
		}
	}

	// The worker file and its case are optional (multi-aggregate projections
	// or hand-edited workers may differ).
	if _, statErr := os.Stat(filepath.FromSlash(workerFile)); statErr == nil {
		if has, err := tx.HasSwitchCase(workerFile, "applyEvent", eventName); err != nil {
			return err
		} else if has {
			if err := tx.RemoveSwitchCase(workerFile, "applyEvent", eventName); err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("  update  %s (removed %s struct + Apply case)\n", domainFile, eventName)
	fmt.Printf("\nEvent %q removed from aggregate %q. Stored event data (if any) is untouched.\n", eventName, aggregateName)
	return nil
}
