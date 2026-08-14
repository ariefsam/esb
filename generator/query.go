package generator

import (
	"fmt"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddQuery injects a new query function into projection/query.go.
func AddQuery(queryName, aggregateName string) error {
	if err := validateSnakeName("query name", queryName); err != nil {
		return err
	}
	if err := validateSnakeName("aggregate name", aggregateName); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	data := QueryData{
		ModuleName:          moduleName,
		PackageName:         naming.PackageName(moduleName),
		QueryName:           queryName,
		QueryNamePascal:     naming.ToPascalCase(queryName),
		AggregateName:       aggregateName,
		AggregateNamePascal: naming.ToPascalCase(aggregateName),
	}

	queryCode, err := renderWithFuncs(queryFuncTmpl, data)
	if err != nil {
		return fmt.Errorf("render query: %w", err)
	}

	tx := injector.NewTx()

	// Guard against re-adding the same query. A bare substring check would
	// false-positive ("Order" matching an existing "OrderItems" func), so we
	// match the full function declaration on a word boundary instead.
	existing, err := tx.Contains("projection/query.go", "func "+data.QueryNamePascal+"(")
	if err != nil {
		return err
	}
	if existing {
		return fmt.Errorf("query %s already exists in projection/query.go", data.QueryNamePascal)
	}

	if err := tx.Append("projection/query.go", queryCode); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Println("  update  projection/query.go")
	return nil
}

const queryFuncTmpl = `
// {{.QueryNamePascal}} queries {{.AggregateNamePascal}} rows.
// TODO: add filter parameters as needed.
func {{.QueryNamePascal}}(ctx context.Context, db *gorm.DB) ([]{{.AggregateNamePascal}}Row, error) {
	var rows []{{.AggregateNamePascal}}Row
	err := db.WithContext(ctx).Find(&rows).Error
	return rows, err
}
`
