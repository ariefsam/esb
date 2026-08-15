package generator

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// ParseFields parses "field:type ..." arguments into FieldDef slices.
func ParseFields(args []string) ([]FieldDef, error) {
	var fields []FieldDef
	for _, arg := range args {
		parts := strings.SplitN(arg, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid field %q — expected name:type", arg)
		}
		fieldName, typ := parts[0], parts[1]
		if err := validateSnakeName("field name", fieldName); err != nil {
			return nil, err
		}
		if !validType(typ) {
			return nil, fmt.Errorf("unsupported type %q for field %q — use string, int64, float64, or bool", typ, fieldName)
		}
		fields = append(fields, FieldDef{
			NamePascal: naming.ToPascalCase(fieldName),
			JSONTag:    fieldName,
			Type:       typ,
		})
	}
	return fields, nil
}

func validType(t string) bool {
	switch t {
	case "string", "int", "int64", "int32", "float64", "float32", "bool":
		return true
	}
	return false
}

// eventRenderData extends EventData with precomputed strings for templates.
type eventRenderData struct {
	EventData
	AggVarName string // lower-camel of AggregateNamePascal: "bankAccount"
	FieldArgs  string // constructor arg list: "amount int64, currency string"
	FieldInits string // struct init lines: "Amount: amount,\n\t\tCurrency: currency,"
}

func buildEventRenderData(data EventData) eventRenderData {
	// Receiver name is just the first letter lowercase, matching the template receiver.
	aggVar := strings.ToLower(data.AggregateNamePascal[:1])

	var argParts, initParts []string
	for _, f := range data.Fields {
		argVar := lcFirst(f.NamePascal)
		argParts = append(argParts, argVar+" "+f.Type)
		initParts = append(initParts, f.NamePascal+": "+argVar+",")
	}

	return eventRenderData{
		EventData:  data,
		AggVarName: aggVar,
		FieldArgs:  strings.Join(argParts, ", "),
		FieldInits: strings.Join(initParts, "\n\t\t"),
	}
}

func lcFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// AddEvent injects a new event struct, Apply() case, constructor, and
// projection worker case into the existing aggregate files.
func AddEvent(aggregateName, eventName string, fields []FieldDef) error {
	if err := validateSnakeName("aggregate name", aggregateName); err != nil {
		return err
	}
	if err := validatePascalName("event name", eventName); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	data := buildEventRenderData(EventData{
		ModuleName:          moduleName,
		PackageName:         naming.PackageName(moduleName),
		AggregateName:       aggregateName,
		AggregateNamePascal: naming.ToPascalCase(aggregateName),
		EventName:           eventName,
		Fields:              fields,
	})

	domainFile := "domain/" + aggregateName + ".go"
	workerFile := "projection/" + aggregateName + "_worker.go"

	tx := injector.NewTx()

	if ok, err := tx.Contains(domainFile, "type "+eventName+" struct"); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("event %s already exists in %s", eventName, domainFile)
	}

	// 1. Ensure "time" is imported (needed by constructors).
	if err := tx.EnsureImport(domainFile, "time"); err != nil {
		return err
	}

	// 2. Append event struct + constructor to domain file.
	structCode, err := renderWithFuncs(eventStructTmpl, data)
	if err != nil {
		return fmt.Errorf("render event struct: %w", err)
	}
	if err := tx.Append(domainFile, structCode); err != nil {
		return err
	}

	// 3. Inject Apply() case.
	applyCase, err := renderWithFuncs(applyCaseTmpl, data)
	if err != nil {
		return fmt.Errorf("render apply case: %w", err)
	}
	if err := tx.InjectAfterMarker(domainFile, "// esb:inject:apply-cases", applyCase); err != nil {
		return err
	}

	// 4. Inject projection worker case.
	workerCase, err := renderWithFuncs(workerCaseTmpl, data)
	if err != nil {
		return fmt.Errorf("render worker case: %w", err)
	}
	if err := tx.InjectAfterMarker(workerFile, "// esb:inject:applyevent-cases", workerCase); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("  update  %s (struct + constructor)\n", domainFile)
	fmt.Printf("  update  %s (Apply case)\n", domainFile)
	fmt.Printf("  update  %s\n", workerFile)
	return nil
}

func renderWithFuncs(tmplStr string, data any) (string, error) {
	t, err := template.New("").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const eventStructTmpl = `
// {{.EventName}} event.
type {{.EventName}} struct {
{{- range .Fields}}
	{{.NamePascal}} {{.Type}} ` + "`" + `json:"{{.JSONTag}}"` + "`" + `
{{- end}}
	OccurredAt int64 ` + "`" + `json:"occurred_at"` + "`" + `
}

func New{{.EventName}}Event({{.FieldArgs}}) {{.EventName}} {
	return {{.EventName}}{
		{{.FieldInits}}
		OccurredAt: time.Now().UnixMilli(),
	}
}
`

const applyCaseTmpl = `	case "{{.EventName}}":
		var evt {{.EventName}}
		if err := json.Unmarshal(data, &evt); err != nil {
			return err
		}
		// TODO: apply evt to {{.AggVarName}} fields
		{{.AggVarName}}.Version++
		return nil
`

const workerCaseTmpl = `		case "{{.EventName}}":
			var evtData domain.{{.EventName}}
			if err := json.Unmarshal(e.Data, &evtData); err != nil {
				return err
			}
			// TODO: update projection rows from evtData
			_ = evtData
`
