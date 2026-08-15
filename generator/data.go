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

// CRUDField extends a plain field with a Go literal sample value used by the
// generated Given-When-Then scenario tests.
type CRUDField struct {
	NamePascal string // Go struct field name: "Price"
	JSONTag    string // json tag / gorm column: "price"
	Type       string // Go type: "string", "int64", ...
	Sample     string // Go literal for tests: `"sample"`, `1`, `true`
}

// CRUDData is passed to the CRUD recipe templates. It describes one entity
// aggregate (product, customer, …) plus the fields carried by its
// Created/Updated events and mirrored in the read-model row.
type CRUDData struct {
	ModuleName  string
	PackageName string
	Name        string // snake_case: "product"
	NamePascal  string // PascalCase: "Product"
	NameKebab   string // kebab-case aggregate-store name: "product"
	Receiver    string // Go receiver variable: "p"
	TableName   string // snake_case plural: "products"
	Fields      []CRUDField
}

// LedgerData is passed to the ledger recipe templates. A ledger has a fixed
// event shape (Opened/Deposited/Withdrawn/Frozen/Closed), so unlike CRUD it
// takes no user-defined fields.
type LedgerData struct {
	ModuleName     string
	PackageName    string
	Name           string // snake_case: "account"
	NamePascal     string // PascalCase: "Account"
	NameKebab      string // kebab-case aggregate-store name: "account"
	Receiver       string // Go receiver variable: "a"
	TableName      string // balance table: "accounts"
	EntryTableName string // statement table: "account_entries"
}

// SMState is one state of a state-machine aggregate.
type SMState struct {
	Raw    string // "placed"
	Pascal string // "Placed"
	Event  string // "<NamePascal>Placed" — the event emitted when entering it
}

// SMFromTransitions lists the states reachable from one state.
type SMFromTransitions struct {
	From string
	Tos  []string
}

// StateMachineData is passed to the state-machine recipe templates.
type StateMachineData struct {
	ModuleName       string
	PackageName      string
	Name             string // snake_case: "order"
	NamePascal       string // PascalCase: "Order"
	NameKebab        string // kebab-case aggregate-store name: "order"
	Receiver         string // Go receiver variable: "o"
	TableName        string // snake_case plural: "orders"
	States           []SMState
	InitialState     string // raw name of the entry state
	TransitionGroups []SMFromTransitions

	// Precomputed values for the generated scenario tests.
	InitialEvent string // event of the initial state
	HasSecond    bool
	SecondState  string // a non-initial state (for "invalid from nothing")
	HasValidTo   bool
	ValidTo      string // a state reachable from the initial state
	ValidEvent   string // its event
	HasInvalidTo bool
	InvalidTo    string // a state NOT reachable from the initial state
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
