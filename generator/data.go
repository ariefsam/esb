package generator

// ProjectData is passed to init-time templates.
type ProjectData struct {
	ModuleName  string // e.g. "github.com/myorg/toko"
	PackageName string // last path segment, valid identifier: "toko"
}

// AggregateData is passed to add-aggregate templates.
type AggregateData struct {
	ModuleName          string
	PackageName         string
	AggregateName       string // snake_case: "bank_account"
	AggregateNamePascal string // PascalCase: "BankAccount"
	AggregateNameKebab  string // kebab-case: "bank-account" (ESB aggregate_name value)
	ReceiverName        string // Go receiver variable: "b" for "BankAccount"
	TableName           string // snake_case plural: "bank_accounts"
}

// FieldDef describes one field of a domain event.
type FieldDef struct {
	NamePascal string // Go struct field name: "BuyerID"
	JSONTag    string // json tag value: "buyer_id"
	Type       string // Go type: "string", "int64", "float64", "bool"
}

// EventData is passed to add-event templates and injectors.
type EventData struct {
	ModuleName          string
	PackageName         string
	AggregateName       string // snake_case
	AggregateNamePascal string // PascalCase
	EventName           string // PascalCase: "OrderPlaced"
	Fields              []FieldDef
}

// ProjectionData is passed to add-projection templates.
type ProjectionData struct {
	ModuleName           string
	PackageName          string
	ProjectionName       string   // snake_case: "sales_report"
	ProjectionNamePascal string   // PascalCase: "SalesReport"
	AggregateNames       []string // ["order", "payment"]
	TableName            string   // snake_case plural: "sales_reports"
}

// HandlerData is passed to add-handler templates.
type HandlerData struct {
	ModuleName          string
	PackageName         string
	HandlerName         string // snake_case: "place_order"
	HandlerNamePascal   string // PascalCase: "PlaceOrder"
	AggregateName       string // snake_case
	AggregateNamePascal string // PascalCase
}

// QueryData is passed to add-query templates.
type QueryData struct {
	ModuleName          string
	PackageName         string
	QueryName           string // snake_case: "order_by_buyer"
	QueryNamePascal     string // PascalCase: "OrderByBuyer"
	AggregateName       string // snake_case
	AggregateNamePascal string // PascalCase
}
