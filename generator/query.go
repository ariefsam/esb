package generator

import (
	"fmt"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddQuery injects a new query function into projection/query.go.
func AddQuery(queryName, aggregateName string) error {
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

	if ok, _ := injector.AlreadyContains("projection/query.go", data.QueryNamePascal); ok {
		return fmt.Errorf("query %s already exists in projection/query.go", data.QueryNamePascal)
	}

	if err := appendToFile("projection/query.go", queryCode); err != nil {
		return fmt.Errorf("append to projection/query.go: %w", err)
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
