package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ariefsam/esb/injector"
)

// injectAutoMigrateModel registers a GORM model (e.g. "OrderRow") in
// projection/db.go's AutoMigrate block if it is not already present.
func injectAutoMigrateModel(tx *injector.Tx, rowType string, actions *[]string) error {
	if ok, err := tx.Contains("projection/db.go", rowType+"{}"); err != nil {
		return err
	} else if ok {
		return nil
	}
	if err := tx.Inject("projection/db.go", injector.AutomigrateModels, "\t\t&"+rowType+"{},"); err != nil {
		return err
	}
	*actions = append(*actions, "  update  projection/db.go")
	return nil
}

// ensureResponseHelper stages the shared server/handler/response.go helpers
// (writeJSON/writeError/decodeJSON) exactly once, so multiple recipes with
// HTTP handlers don't redefine them.
func ensureResponseHelper(tx *injector.Tx, actions *[]string) error {
	const path = "server/handler/response.go"
	decode, err := renderTemplate("recipe/response_decode.go.tmpl", nil)
	if err != nil {
		return fmt.Errorf("generate %s: %w", path, err)
	}
	if _, statErr := os.Stat(filepath.FromSlash(path)); os.IsNotExist(statErr) {
		content, err := renderTemplate("recipe/crud_response.go.tmpl", nil)
		if err != nil {
			return fmt.Errorf("generate %s: %w", path, err)
		}
		tx.Create(path, content+decode)
		*actions = append(*actions, "  create  "+path)
		return nil
	}

	// A response.go generated before decodeJSON existed lacks it, but the
	// handlers generated now call it — add it instead of breaking the build.
	if ok, err := tx.Contains(path, "func decodeJSON("); err != nil {
		return err
	} else if ok {
		return nil
	}
	if err := tx.EnsureImport(path, "errors"); err != nil {
		return err
	}
	if err := tx.Append(path, decode); err != nil {
		return err
	}
	*actions = append(*actions, "  update  "+path)
	return nil
}
