package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ariefsam/esb/naming"
)

// maxOutputBytes caps each stream so an accidentally verbose child
// process cannot exhaust memory. 1 MiB matches the value used by the
// approved plan and the same value is enforced for both stdout and
// stderr.
const maxOutputBytes = 1 << 20

// runTimeout bounds a single command execution. The runner kills the
// child and marks the run as timed_out once the deadline is exceeded.
// Declared as a var (not const) so tests can shrink it to keep the
// timeout suite fast.
var runTimeout = 5 * time.Minute
var runTimeoutMu sync.RWMutex

// CatalogEntry is one allow-listed command. It owns the metadata used
// to render the form and the BuildArgv function that turns validated
// form input into a single argv slice.
type CatalogEntry struct {
	ID          string
	Label       string
	Description string
	Fields      []CommandField
	Preview     []string
	Build       func(form FormInput) ([]string, error)
}

// FormInput is the parsed set of field values for one command. List
// fields are pre-split; the Build function never receives raw user
// strings longer than the per-field max.
type FormInput map[string][]string

// catalog is the closed allow-list of commands the UI can run. Adding
// a command here is the only way to make a new esb subcommand
// executable through the UI.
var catalog = []CatalogEntry{
	{
		ID:          "add-aggregate",
		Label:       "Add aggregate",
		Description: "Generate domain/service/projection files for a new aggregate. snake_case.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "order", Required: true, Type: "text", Help: "snake_case aggregate name"},
		},
		Preview: []string{"esb", "add", "aggregate", "order"},
		Build:   buildAddAggregate,
	},
	{
		ID:          "add-event",
		Label:       "Add event",
		Description: "Add an event to an existing aggregate. PascalCase event name, field:type pairs.",
		Fields: []CommandField{
			{Name: "aggregate", Label: "Aggregate", Placeholder: "order", Required: true, Type: "text"},
			{Name: "event", Label: "Event name", Placeholder: "OrderPlaced", Required: true, Type: "text", Help: "PascalCase"},
			{Name: "fields", Label: "Fields", Placeholder: "amount:int64 currency:string", Type: "list", Help: "one field:type per line"},
		},
		Preview: []string{"esb", "add", "event", "order", "OrderPlaced", "amount:int64", "currency:string"},
		Build:   buildAddEvent,
	},
	{
		ID:          "add-projection",
		Label:       "Add projection",
		Description: "Generate a multi-aggregate projection worker.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "sales_report", Required: true, Type: "text", Help: "snake_case projection name"},
			{Name: "aggregates", Label: "Aggregates", Placeholder: "order", Required: true, Type: "list", Help: "one aggregate name per line"},
		},
		Preview: []string{"esb", "add", "projection", "sales_report", "--aggregates", "order"},
		Build:   buildAddProjection,
	},
	{
		ID:          "add-handler",
		Label:       "Add handler",
		Description: "Generate an HTTP handler skeleton.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "place_order", Required: true, Type: "text", Help: "snake_case handler name"},
			{Name: "aggregate", Label: "Aggregate", Placeholder: "order", Required: true, Type: "text"},
		},
		Preview: []string{"esb", "add", "handler", "place_order", "--aggregate", "order"},
		Build:   buildAddHandler,
	},
	{
		ID:          "add-query",
		Label:       "Add query",
		Description: "Append a query function to projection/query.go.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "orders_by_buyer", Required: true, Type: "text"},
			{Name: "aggregate", Label: "Aggregate", Placeholder: "order", Required: true, Type: "text"},
		},
		Preview: []string{"esb", "add", "query", "orders_by_buyer", "--aggregate", "order"},
		Build:   buildAddQuery,
	},
	{
		ID:          "add-recipe-crud",
		Label:       "Add CRUD recipe",
		Description: "Scaffold a whole CRUD slice: Created/Updated/Archived events, commands, projection, queries, handlers, and tests. snake_case name, field:type pairs.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "product", Required: true, Type: "text", Help: "snake_case entity name"},
			{Name: "fields", Label: "Fields", Placeholder: "name:string price:int64 sku:string", Type: "list", Help: "one field:type per line"},
		},
		Preview: []string{"esb", "add", "recipe", "crud", "product", "name:string", "price:int64", "sku:string"},
		Build:   buildAddRecipeCRUD,
	},
	{
		ID:          "add-recipe-ledger",
		Label:       "Add ledger recipe",
		Description: "Scaffold an append-only ledger account: Open/Deposit/Withdraw/Freeze/Close with a non-negative-balance invariant, statement journal, and tests. snake_case name.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "account", Required: true, Type: "text", Help: "snake_case account name"},
		},
		Preview: []string{"esb", "add", "recipe", "ledger", "account"},
		Build:   buildAddRecipeLedger,
	},
	{
		ID:          "add-recipe-statemachine",
		Label:       "Add state machine recipe",
		Description: "Scaffold a lifecycle aggregate with guarded transitions. snake_case name + states; transitions as from->to (one per line).",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "order", Required: true, Type: "text", Help: "snake_case aggregate name"},
			{Name: "states", Label: "States", Placeholder: "placed\npaid\nshipped\ndelivered\ncancelled", Required: true, Type: "list", Help: "one snake_case state per line; the first is the initial state"},
			{Name: "transitions", Label: "Transitions", Placeholder: "placed->paid\npaid->shipped", Type: "list", Help: "one from->to per line"},
		},
		Preview: []string{"esb", "add", "recipe", "statemachine", "order", "--states", "placed,paid,shipped", "--transitions", "placed->paid,paid->shipped"},
		Build:   buildAddRecipeStateMachine,
	},
	{
		ID:          "add-recipe-saga",
		Label:       "Add saga recipe",
		Description: "Scaffold an orchestration saga: a two-step transfer with compensation (Debit then Credit, refund on failure). snake_case name.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "money_transfer", Required: true, Type: "text", Help: "snake_case saga name"},
		},
		Preview: []string{"esb", "add", "recipe", "saga", "money_transfer"},
		Build:   buildAddRecipeSaga,
	},
	{
		ID:          "add-recipe-outbox",
		Label:       "Add outbox recipe",
		Description: "Scaffold a transactional outbox: ingest an aggregate's events + a publisher worker for integration events. snake_case name.",
		Fields: []CommandField{
			{Name: "name", Label: "Name", Placeholder: "order", Required: true, Type: "text", Help: "snake_case aggregate name whose events to relay"},
		},
		Preview: []string{"esb", "add", "recipe", "outbox", "order"},
		Build:   buildAddRecipeOutbox,
	},
	{
		ID:          "add-upcaster",
		Label:       "Add upcaster",
		Description: "Register an event upcaster so old stored payloads are migrated to the current shape on replay. PascalCase event name.",
		Fields: []CommandField{
			{Name: "aggregate", Label: "Aggregate", Placeholder: "order", Required: true, Type: "text", Help: "snake_case aggregate name"},
			{Name: "event", Label: "Event name", Placeholder: "OrderPlaced", Required: true, Type: "text", Help: "PascalCase"},
		},
		Preview: []string{"esb", "add", "upcaster", "order", "OrderPlaced"},
		Build:   buildAddUpcaster,
	},
	{
		ID:          "show",
		Label:       "Show project",
		Description: "Print esb show output for the current project (optional aggregate focus).",
		Fields: []CommandField{
			{Name: "aggregate", Label: "Aggregate (optional)", Placeholder: "order", Type: "text"},
		},
		Preview: []string{"esb", "show"},
		Build:   buildShow,
	},
	{
		ID:          "migrate-to-esb",
		Label:       "Migrate to ESB server",
		Description: "Pindahkan event dari SQLite lokal ke server Event Sourcing Builder remote.",
		Fields: []CommandField{
			{Name: "source", Label: "SQLite path", Placeholder: "app.db", Type: "text", Help: "Defaults to app.db relative to the selected project root"},
			{Name: "esb_url", Label: "ESB URL", Placeholder: "http://esb.internal:8080", Required: true, Type: "text", Help: "http(s)://host:port"},
			{Name: "tenant_id", Label: "Tenant ID", Placeholder: "demo", Required: true, Type: "text"},
			{Name: "project_id", Label: "Project ID", Placeholder: "toko", Required: true, Type: "text"},
		},
		Preview: []string{"esb", "migrate", "to-esb", "--esb-url", "http://esb.internal:8080", "--tenant", "demo", "--project", "toko"},
		Build:   buildMigrateToESB,
	},
	{
		ID:          "migrate-to-embedded",
		Label:       "Migrate to embedded",
		Description: "Rollback: pindahkan event dari server ESB remote ke SQLite lokal.",
		Fields: []CommandField{
			// Same fields as the to-esb form so the same-origin
			// validator can reject unknown keys uniformly; the
			// operator pre-fills from .env and the handler
			// passes them through to `esb migrate to-embedded`.
			{Name: "source", Label: "SQLite path", Placeholder: "app.db", Type: "text", Help: "Defaults to app.db relative to the selected project root"},
			{Name: "esb_url", Label: "ESB URL", Placeholder: "http://esb.internal:8080", Required: true, Type: "text", Help: "http(s)://host:port"},
			{Name: "tenant_id", Label: "Tenant ID", Placeholder: "demo", Required: true, Type: "text"},
			{Name: "project_id", Label: "Project ID", Placeholder: "toko", Required: true, Type: "text"},
			{Name: "force", Label: "Force overwrite", Type: "checkbox", Help: "Wajib dicentang bila SQLite sudah berisi event."},
		},
		Preview: []string{"esb", "migrate", "to-embedded", "--esb-url", "http://esb.internal:8080", "--tenant", "demo", "--project", "toko", "--force"},
		Build:   buildMigrateToEmbedded,
	},
}

// findCommand returns the catalog entry for id, or nil if unknown.
func findCommand(id string) *CatalogEntry {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}

// PublicCommands exposes catalog entries in the view-model shape used
// by the templates and the handlers.
func PublicCommands() []CommandView {
	out := make([]CommandView, 0, len(catalog))
	for _, c := range catalog {
		out = append(out, CommandView{
			ID:          c.ID,
			Label:       c.Label,
			Description: c.Description,
			Fields:      c.Fields,
			Preview:     c.Preview,
		})
	}
	return out
}

// validateFieldName enforces the conservative input rules the UI uses
// before passing a value to a Build function. The list matches the
// characters every Cobra subcommand already accepts via flag/argument
// parsing, and rejects shell metacharacters defensively even though we
// never invoke a shell.
func validateFieldName(name string) error {
	if name == "" {
		return fmt.Errorf("empty value")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("invalid character %q in %q", string(r), name)
		}
	}
	return nil
}

// validateFieldType extends the alphanumeric+_- set with ':' so the
// event field list ("amount:int64") can pass through validation.
func validateFieldType(s string) error {
	if s == "" {
		return fmt.Errorf("empty value")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == ':' || r == '.' || r == ',':
		default:
			return fmt.Errorf("invalid character %q in %q", string(r), s)
		}
	}
	return nil
}

func buildAddAggregate(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if name != naming.ToSnakeCase(name) {
		return nil, fmt.Errorf("name must be snake_case")
	}
	return []string{"esb", "add", "aggregate", name}, nil
}

func buildAddEvent(form FormInput) ([]string, error) {
	agg := onlyValue(form, "aggregate")
	if err := validateFieldName(agg); err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	event := onlyValue(form, "event")
	if err := validateFieldName(event); err != nil {
		return nil, fmt.Errorf("event: %w", err)
	}
	if naming.ToPascalCase(event) != event {
		return nil, fmt.Errorf("event name must be PascalCase")
	}
	fields := form["fields"]
	for _, f := range fields {
		if err := validateFieldType(f); err != nil {
			return nil, fmt.Errorf("field %q: %w", f, err)
		}
	}
	argv := []string{"esb", "add", "event", agg, event}
	argv = append(argv, fields...)
	return argv, nil
}

func buildAddProjection(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	aggs := form["aggregates"]
	if len(aggs) == 0 {
		return nil, fmt.Errorf("aggregates: at least one aggregate required")
	}
	for _, a := range aggs {
		if err := validateFieldName(a); err != nil {
			return nil, fmt.Errorf("aggregate %q: %w", a, err)
		}
	}
	argv := []string{"esb", "add", "projection", name, "--aggregates", strings.Join(aggs, ",")}
	return argv, nil
}

func buildAddHandler(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	agg := onlyValue(form, "aggregate")
	if err := validateFieldName(agg); err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	return []string{"esb", "add", "handler", name, "--aggregate", agg}, nil
}

func buildAddQuery(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	agg := onlyValue(form, "aggregate")
	if err := validateFieldName(agg); err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	return []string{"esb", "add", "query", name, "--aggregate", agg}, nil
}

func buildAddRecipeCRUD(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if name != naming.ToSnakeCase(name) {
		return nil, fmt.Errorf("name must be snake_case")
	}
	fields := form["fields"]
	for _, f := range fields {
		if err := validateFieldType(f); err != nil {
			return nil, fmt.Errorf("field %q: %w", f, err)
		}
	}
	argv := []string{"esb", "add", "recipe", "crud", name}
	argv = append(argv, fields...)
	return argv, nil
}

func buildAddRecipeLedger(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if name != naming.ToSnakeCase(name) {
		return nil, fmt.Errorf("name must be snake_case")
	}
	return []string{"esb", "add", "recipe", "ledger", name}, nil
}

func buildAddUpcaster(form FormInput) ([]string, error) {
	agg := onlyValue(form, "aggregate")
	if err := validateFieldName(agg); err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	event := onlyValue(form, "event")
	if err := validateFieldName(event); err != nil {
		return nil, fmt.Errorf("event: %w", err)
	}
	if naming.ToPascalCase(event) != event {
		return nil, fmt.Errorf("event name must be PascalCase")
	}
	return []string{"esb", "add", "upcaster", agg, event}, nil
}

func buildAddRecipeOutbox(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if name != naming.ToSnakeCase(name) {
		return nil, fmt.Errorf("name must be snake_case")
	}
	return []string{"esb", "add", "recipe", "outbox", name}, nil
}

func buildAddRecipeSaga(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if name != naming.ToSnakeCase(name) {
		return nil, fmt.Errorf("name must be snake_case")
	}
	return []string{"esb", "add", "recipe", "saga", name}, nil
}

func buildAddRecipeStateMachine(form FormInput) ([]string, error) {
	name := onlyValue(form, "name")
	if err := validateFieldName(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if name != naming.ToSnakeCase(name) {
		return nil, fmt.Errorf("name must be snake_case")
	}
	states := form["states"]
	if len(states) == 0 {
		return nil, fmt.Errorf("states: at least one state required")
	}
	for _, s := range states {
		if err := validateFieldName(s); err != nil {
			return nil, fmt.Errorf("state %q: %w", s, err)
		}
		if s != naming.ToSnakeCase(s) {
			return nil, fmt.Errorf("state %q must be snake_case", s)
		}
	}
	// Transitions are "from->to"; validate each side as a plain name so the
	// only special character that reaches argv is the "->" separator.
	transitions := form["transitions"]
	for _, tr := range transitions {
		parts := strings.SplitN(tr, "->", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("transition %q: expected from->to", tr)
		}
		for _, side := range parts {
			if err := validateFieldName(strings.TrimSpace(side)); err != nil {
				return nil, fmt.Errorf("transition %q: %w", tr, err)
			}
		}
	}
	argv := []string{"esb", "add", "recipe", "statemachine", name, "--states", strings.Join(states, ",")}
	if len(transitions) > 0 {
		argv = append(argv, "--transitions", strings.Join(transitions, ","))
	}
	return argv, nil
}

func buildShow(form FormInput) ([]string, error) {
	agg := onlyValue(form, "aggregate")
	if agg != "" {
		if err := validateFieldName(agg); err != nil {
			return nil, fmt.Errorf("aggregate: %w", err)
		}
	}
	if agg == "" {
		return []string{"esb", "show"}, nil
	}
	return []string{"esb", "show", agg}, nil
}

// validateFieldURL accepts a parsed http(s) URL with a valid hostname
// and optional port/path. Query strings, fragments, and userinfo are
// rejected because migration endpoints are configured as base URLs.
func validateFieldURL(s string) error {
	if s == "" {
		return fmt.Errorf("empty value")
	}
	u, err := url.ParseRequestURI(s)
	if err != nil {
		return fmt.Errorf("URL tidak valid: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL harus mulai dengan http:// atau https://")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("URL tidak boleh punya userinfo, query, atau fragment")
	}
	host := u.Hostname()
	if host == "" || host == "." || strings.Trim(host, ".") == "" {
		return fmt.Errorf("URL tidak punya host yang valid")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("URL tidak punya host yang valid")
		}
	}
	return nil
}

func validateSourcePath(s string) error {
	if s == "" {
		return nil
	}
	if filepath.IsAbs(s) || filepath.Clean(s) == ".." || strings.HasPrefix(filepath.Clean(s), ".."+string(filepath.Separator)) {
		return fmt.Errorf("source must stay inside the selected project")
	}
	return nil
}

// validateFieldTenantProject enforces snake_case-or-hyphen on the
// tenant + project IDs so they round-trip cleanly to ESB without
// being parsed as flags or paths.
func validateFieldTenantProject(s string) error {
	if s == "" {
		return fmt.Errorf("empty value")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("invalid character %q in %q", string(r), s)
		}
	}
	return nil
}

// buildMigrateToESB constructs the argv for `esb migrate to-esb`.
// All three required fields are validated: URL via validateFieldURL,
// tenant/project via validateFieldTenantProject. Empty field
// rejection is handled by the catalog (Required: true) before this
// function runs.
func buildMigrateToESB(form FormInput) ([]string, error) {
	source := onlyValue(form, "source")
	if err := validateSourcePath(source); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if source == "" {
		source = "app.db"
	}
	esbURL := onlyValue(form, "esb_url")
	if err := validateFieldURL(esbURL); err != nil {
		return nil, fmt.Errorf("esb_url: %w", err)
	}
	tenant := onlyValue(form, "tenant_id")
	if err := validateFieldTenantProject(tenant); err != nil {
		return nil, fmt.Errorf("tenant_id: %w", err)
	}
	project := onlyValue(form, "project_id")
	if err := validateFieldTenantProject(project); err != nil {
		return nil, fmt.Errorf("project_id: %w", err)
	}
	return []string{
		"esb", "migrate", "to-esb",
		"--source", source,
		"--esb-url", esbURL,
		"--tenant", tenant,
		"--project", project,
	}, nil
}

// buildMigrateToEmbedded constructs the argv for `esb migrate
// to-embedded`. The form requires esb_url / tenant_id / project_id
// (same as the to-esb direction) so the operator can confirm the
// remote target without editing .env. The optional force checkbox
// maps to the --force CLI flag.
func buildMigrateToEmbedded(form FormInput) ([]string, error) {
	source := onlyValue(form, "source")
	if err := validateSourcePath(source); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if source == "" {
		source = "app.db"
	}
	esbURL := onlyValue(form, "esb_url")
	if err := validateFieldURL(esbURL); err != nil {
		return nil, fmt.Errorf("esb_url: %w", err)
	}
	tenant := onlyValue(form, "tenant_id")
	if err := validateFieldTenantProject(tenant); err != nil {
		return nil, fmt.Errorf("tenant_id: %w", err)
	}
	project := onlyValue(form, "project_id")
	if err := validateFieldTenantProject(project); err != nil {
		return nil, fmt.Errorf("project_id: %w", err)
	}
	argv := []string{
		"esb", "migrate", "to-embedded",
		"--source", source,
		"--esb-url", esbURL,
		"--tenant", tenant,
		"--project", project,
	}
	if onlyValue(form, "force") == "true" {
		argv = append(argv, "--force")
	}
	return argv, nil
}

// onlyValue returns the first non-empty value for a key, or "" if none.
func onlyValue(form FormInput, key string) string {
	vs := form[key]
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ParseForm converts a url.Values map into the FormInput shape used by
// Build functions. Each submitted value is split on newlines so textarea
// list fields become individual argv positions; blank lines are ignored.
func ParseForm(values map[string][]string) FormInput {
	out := FormInput{}
	for k, vs := range values {
		clean := make([]string, 0, len(vs))
		for _, raw := range vs {
			for _, v := range strings.Split(raw, "\n") {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				clean = append(clean, v)
			}
		}
		out[k] = clean
	}
	return out
}

// BuildArgv resolves a form submission into an argv slice. The slice
// is constructed exactly as it will be passed to exec.Command — no
// shell, no concatenation, no env passing.
func BuildArgv(id string, form FormInput) ([]string, error) {
	cmd := findCommand(id)
	if cmd == nil {
		return nil, fmt.Errorf("unknown command %q", id)
	}
	argv, err := cmd.Build(form)
	if err != nil {
		return nil, err
	}
	return argv, nil
}

// ProcessRunner executes an argv against projectRoot without invoking
// a shell. Concrete implementations live alongside the UI so tests
// can swap them out for fakes.
type ProcessRunner interface {
	Run(ctx context.Context, projectRoot string, argv []string, stdout, stderr io.Writer) (exitCode int, err error)
}

// ExecRunner is the production ProcessRunner. It uses os.Executable()
// when present (so `esb ui` re-enters itself as a child for the show
// shortcut) and otherwise falls back to the first token of argv.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, projectRoot string, argv []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 1, fmt.Errorf("empty argv")
	}
	bin, lookupErr := exec.LookPath(argv[0])
	if lookupErr != nil {
		self, err := os.Executable()
		if err != nil || filepath.Base(self) != filepath.Base(argv[0]) {
			return 1, fmt.Errorf("locate %s: %w", argv[0], lookupErr)
		}
		bin = self
	}
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = projectRoot
	cmd.Env = os.Environ()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return 0, ErrTimeout
	}
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 1, err
}

// ErrTimeout signals that the run exceeded runTimeout. The handler
// surfaces this as RunTimedOut rather than a generic failure.
var ErrTimeout = errors.New("command exceeded timeout")

// runStoreCap bounds the number of runs kept in memory. When the store
// reaches this size Start evicts the oldest *completed* run to make room
// so a long-lived `esb ui` session isn't permanently locked out after
// runStoreCap executions (the assessment's RunStore-never-evicts finding).
// Eviction never touches the single in-flight run, so history is bounded
// without ever discarding a run the UI is actively streaming.
const runStoreCap = 1000

// RunStore keeps the in-memory map of run id -> *Run. Only one run may
// be active at a time so the UI never races with itself on generated
// file writes.
type RunStore struct {
	mu     sync.Mutex
	runs   map[string]*Run
	nextID uint64
	active bool
}

// NewRunStore returns an empty RunStore.
func NewRunStore() *RunStore {
	return &RunStore{runs: map[string]*Run{}}
}

// Cap returns the maximum number of runs the store will keep in
// memory. Exposed so tests and templates can reference the same number
// the store enforces.
func (s *RunStore) Cap() int { return runStoreCap }

// Busy reports whether another run is in progress.
func (s *RunStore) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Get returns an independent snapshot of the Run for id, or nil if
// unknown. The returned pointer and its slices may be safely read or
// modified without racing the executor goroutine.
func (s *RunStore) Get(id string) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return nil
	}
	copy := *r
	copy.Argv = append([]string(nil), r.Argv...)
	return &copy
}

// StatusSnapshot returns the run's current status and exit code under
// the store's lock. Use this from polling code instead of dereferencing
// the pointer returned by Get, which races with the executor goroutine.
func (s *RunStore) StatusSnapshot(id string) (status RunStatus, exitCode int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return "", 0, false
	}
	return r.Status, r.ExitCode, true
}

// Start launches the command behind catalog[id] and records a *Run.
// The returned error is ErrConflict when another run is still active,
// or ErrStoreFull when the in-memory run store has reached runStoreCap.
// The parent context should outlive the HTTP request that triggered
// the run so the child process isn't cancelled the moment the handler
// redirects.
func (s *RunStore) Start(parent context.Context, projectRoot, commandID string, form FormInput, runner ProcessRunner) (*Run, error) {
	argv, err := BuildArgv(commandID, form)
	if err != nil {
		return nil, err
	}
	argv = append([]string(nil), argv...)

	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return nil, ErrConflict
	}
	if len(s.runs) >= runStoreCap {
		if !s.evictOldestCompletedLocked() {
			// Every slot is an in-flight run — impossible under the
			// single-active invariant, but fail safe rather than grow
			// unbounded.
			s.mu.Unlock()
			return nil, ErrStoreFull
		}
	}
	s.active = true
	run := &Run{
		ID:        newRunID(s.nextID),
		CommandID: commandID,
		Argv:      argv,
		Dir:       projectRoot,
		StartedAt: time.Now(),
		Status:    RunRunning,
	}
	s.nextID++
	s.runs[run.ID] = run
	s.mu.Unlock()

	go s.execute(parent, run, runner)
	return run, nil
}

// evictOldestCompletedLocked removes the oldest run whose status is no
// longer RunRunning and reports whether one was evicted. The caller must
// hold s.mu. It is O(n) but only runs when the store is at capacity.
func (s *RunStore) evictOldestCompletedLocked() bool {
	var oldestID string
	var oldestAt time.Time
	for id, r := range s.runs {
		if r.Status == RunRunning {
			continue
		}
		if oldestID == "" || r.StartedAt.Before(oldestAt) {
			oldestID, oldestAt = id, r.StartedAt
		}
	}
	if oldestID == "" {
		return false
	}
	delete(s.runs, oldestID)
	return true
}

// ErrConflict signals that the UI rejected a second run while one is
// already in progress.
var ErrConflict = errors.New("another command is already running")

// ErrStoreFull signals that the in-memory run store is full. This is
// the safety net introduced in Phase 1 of the approved plan: a
// runaway tab cannot exhaust process memory by hammering the execute
// endpoint. The store keeps at most runStoreCap runs.
var ErrStoreFull = errors.New("run store is full")

func (s *RunStore) execute(parent context.Context, run *Run, runner ProcessRunner) {
	runTimeoutMu.RLock()
	timeout := runTimeout
	runTimeoutMu.RUnlock()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	out := &capWriter{limit: maxOutputBytes}
	errOut := &capWriter{limit: maxOutputBytes}

	exitCode, err := runner.Run(ctx, run.Dir, run.Argv, out, errOut)

	s.mu.Lock()
	run.FinishedAt = time.Now()
	run.Stdout = out.String()
	run.Stderr = errOut.String()
	run.ExitCode = exitCode
	switch {
	case errors.Is(err, ErrTimeout), errors.Is(ctx.Err(), context.DeadlineExceeded):
		run.Status = RunTimedOut
		run.Err = "command exceeded timeout"
	case err != nil:
		run.Status = RunFailed
		run.Err = err.Error()
	case exitCode != 0:
		run.Status = RunFailed
		run.Err = fmt.Sprintf("exit code %d", exitCode)
	default:
		run.Status = RunSucceed
	}
	s.active = false
	s.mu.Unlock()
}

// newRunID generates a unique run identifier from a monotonic counter
// and a small random suffix. crypto/rand is preferred but we tolerate
// failure by falling back to a time-based suffix; the id only needs
// to be hard to guess between concurrent tabs.
func newRunID(counter uint64) string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("run-%d-%d", counter, time.Now().UnixNano())
	}
	return fmt.Sprintf("run-%d-%s", counter, hex.EncodeToString(buf[:]))
}

// capWriter is an io.Writer that stops accepting bytes once limit is
// reached. It appends a truncation marker so the UI can show the user
// that more output existed.
type capWriter struct {
	limit     int
	written   int
	truncated bool
	buf       strings.Builder
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.truncated {
		return len(p), nil
	}
	remaining := c.limit - c.written
	if remaining <= 0 {
		c.markTruncated()
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.written = c.limit
		c.markTruncated()
		return len(p), nil
	}
	c.buf.Write(p)
	c.written += len(p)
	return len(p), nil
}

func (c *capWriter) markTruncated() {
	if c.truncated {
		return
	}
	c.truncated = true
	c.buf.WriteString("\n[output truncated at 1 MiB]\n")
}

func (c *capWriter) String() string {
	return c.buf.String()
}

// ProjectRoot returns the absolute path to projectRoot, expanding any
// ".." segments and resolving relative paths against the process's
// current working directory.
func ProjectRoot(raw string) (string, error) {
	if raw == "" {
		raw = "."
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("project root %q is not absolute", raw)
	}
	return clean, nil
}

// IsValidProjectRoot checks whether path looks like an ESB project —
// a go.mod must exist. Used by the cmd layer before starting the UI.
func IsValidProjectRoot(path string) bool {
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}
