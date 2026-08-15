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
	if err := tx.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", "\t\t&"+rowType+"{},"); err != nil {
		return err
	}
	*actions = append(*actions, "  update  projection/db.go")
	return nil
}

// ensureResponseHelper stages the shared server/handler/response.go helpers
// (writeJSON/writeError) exactly once, so multiple recipes with HTTP handlers
// don't redefine them.
func ensureResponseHelper(tx *injector.Tx, actions *[]string) error {
	if _, statErr := os.Stat(filepath.FromSlash("server/handler/response.go")); !os.IsNotExist(statErr) {
		return nil
	}
	content, err := renderTemplate("recipe/crud_response.go.tmpl", nil)
	if err != nil {
		return fmt.Errorf("generate server/handler/response.go: %w", err)
	}
	tx.Create("server/handler/response.go", content)
	*actions = append(*actions, "  create  server/handler/response.go")
	return nil
}
