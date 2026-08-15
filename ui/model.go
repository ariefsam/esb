// Package ui implements the local-only admin web UI exposed by `esb ui`.
//
// The UI parses the generated project files via inspector.Scan, renders
// dashboards with html/template + Alpine.js, and lets the user execute a
// strictly allow-listed set of esb commands against the current project.
//
// The package is intentionally scoped to localhost usage: no database,
// no remote event store, no account system. All run state lives in memory
// and disappears when the server stops.
package ui

import (
	"time"

	"github.com/ariefsam/esb/inspector"
)

// PageKind identifies the high-level template to render so handlers
// can share a single dispatch helper while still picking the right body.
type PageKind string

const (
	PageOverview        PageKind = "overview"
	PageAggregateDetail PageKind = "aggregate_detail"
	PageCommands        PageKind = "commands"
	PageRunDetail       PageKind = "run_detail"
	PageStorage         PageKind = "storage"
	PageMigrate         PageKind = "migrate"
	PageDeleteEvent     PageKind = "delete_event"
	PageError           PageKind = "error"
)

// Layout is the top-level view-model passed to layout.html. The body
// field is rendered through the {{block}} helper so the same shell can
// wrap overview, aggregate, commands, run, and error pages.
type Layout struct {
	Title    string
	Subtitle string
	Root     string // project root, shown in the header
	CSSPath  string // /static/app.css
	JSPath   string // /static/app.js
	Body     string // rendered body HTML, inlined by the handler
	Nav      Nav
	Flash    string // optional banner text (e.g. "Project refreshed")
	Run      *Run   // active run pointer if the user just executed a command
}

// Nav holds the navigation links rendered into the layout header.
type Nav struct {
	Active string
	Links  []NavLink
}

// NavLink is one entry in the top navigation bar.
type NavLink struct {
	Href string
	Text string
	Key  string
}

// OverviewPage is the rendered dashboard for `GET /`. Title and
// Subtitle are duplicated from the layout fields so the body template
// can render them through its own dot without relying on the layout
// shell — keeping the body self-contained also keeps the render path
// (overview.html) free of silent template errors when the title is
// missing.
type OverviewPage struct {
	Kind          PageKind
	Project       inspector.ProjectModel
	Title         string
	Subtitle      string
	EventCount    int
	HandlerCount  int
	QueryCount    int
	AggregateRows []AggregateRow
}

// AggregateRow is one entry on the overview page's aggregate table.
type AggregateRow struct {
	Name      string
	Events    []string
	EventText string
	Href      string
}

// AggregateDetailPage is the rendered `GET /aggregates/{name}` page.
// Wire-graph plumbing (chains) used to live here but the matching was
// unreliable for single-aggregate projections, so the field has been
// removed; the page focuses on handlers, queries, and projection
// workers for now.
type AggregateDetailPage struct {
	Kind         PageKind
	Project      inspector.ProjectModel
	Name         string // store name (kebab), shown in the UI + URL
	FileName     string // snake_case file name, passed to `esb add/delete event`
	Events       []string
	EventDetails []inspector.EventDetail
	Handlers     []inspector.Handler
	Queries      []inspector.Query
	Workers      []inspector.Projection
	Other        []string
}

// CommandsPage renders the command catalog and the per-command forms.
type CommandsPage struct {
	Kind     PageKind
	Project  inspector.ProjectModel
	Groups   []CommandGroup
	Commands []CommandView // flat list, kept for callers that don't group
	Error    string        // pre-fill error banner when a previous run was rejected
	Flash    string
}

// DeleteEventPage is the confirm screen before removing an event definition.
// It reports whether the event's rows already exist in the store so the user
// can be warned before deleting a definition whose events have happened.
type DeleteEventPage struct {
	Kind          PageKind
	AggregateName string // store name (kebab), for display + the count lookup
	AggregateFile string // file name (snake), passed to `esb delete event`
	EventName     string
	Mode          string // storage mode: embedded / esb-server / unknown
	CanVerify     bool   // true in embedded mode where EventCount is authoritative
	EventCount    int    // stored rows for this event (embedded only)
	RequireAck    bool   // count > 0, or cannot verify (esb-server) — force a confirm checkbox
	Error         string // shown when a POST is rejected (missing ack)
}

// CommandGroup is a titled section of related commands on the Commands page
// (e.g. "Recipes"), with a short intro explaining the section.
type CommandGroup struct {
	Title    string
	Intro    string
	Commands []CommandView
}

// CommandView is one allow-listed command in the catalog. The handler
// uses it to render both the form inputs and the live Alpine preview.
type CommandView struct {
	ID          string
	Label       string
	Description string
	Details     []string // optional bullet points ("what it generates")
	Fields      []CommandField
	Preview     []string // example argv shown when the form is empty
}

// CommandField is one input field on a command form. Type is "text" or
// "list" — list fields let the user enter one item per line and are
// translated into multiple argv positions.
type CommandField struct {
	Name        string
	Label       string
	Placeholder string
	Required    bool
	Type        string // "text" or "list"
	Help        string
}

// RunStatus is the lifecycle of a single command execution.
type RunStatus string

const (
	RunRunning  RunStatus = "running"
	RunSucceed  RunStatus = "succeeded"
	RunFailed   RunStatus = "failed"
	RunTimedOut RunStatus = "timed_out"
)

// Run captures one command execution. Mutated by the runner goroutine
// and read by the polling handler — access is guarded by RunStore.mu.
type Run struct {
	ID         string
	CommandID  string
	Argv       []string
	Dir        string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     RunStatus
	ExitCode   int
	Stdout     string
	Stderr     string
	Err        string // populated when Status != succeeded
}

// RunPage renders `GET /commands/runs/{id}`.
type RunPage struct {
	Kind    PageKind
	Project inspector.ProjectModel
	Run     *Run
	Found   bool
	// Running is computed under the run store lock so the template
	// can render the polling widget without racing the executor.
	Running bool
}

// ErrorPage renders the shared 404 / parse-failure / validation error
// states so handlers don't duplicate the same shell.
type ErrorPage struct {
	Kind    PageKind
	Project inspector.ProjectModel
	Title   string
	Message string
	Back    string // href of the "back to overview" link
	Code    int    // HTTP status to send alongside the page
}

// StorageAggregateRow is one row on the /storage aggregate table.
type StorageAggregateRow struct {
	Name          string
	Count         int
	SnapshotCount int
}

// StorageLockRow is one row on the /storage locks table. ExpiresAt
// is pre-formatted so the template does no time math.
type StorageLockRow struct {
	Key        string
	OwnerToken string
	ExpiresAt  string
	Held       bool
}

// StoragePage is the view-model for `GET /storage`. It surfaces
// the active event-store mode, the resolved DSN/URL, and the
// per-aggregate event counts so the user can decide whether to
// trigger a migration.
type StoragePage struct {
	Kind                 PageKind
	Project              inspector.ProjectModel
	Mode                 string // "embedded" or "esb-server"
	ModeLabel            string // human label for the badge
	DSN                  string // embedded SQLite path, absolute
	ESBURL               string // remote URL when mode == esb-server
	TotalEvents          int
	TotalSnapshots       int
	HasSQLite            bool // false when DSN is unset or file unreadable
	Rows                 []StorageAggregateRow
	Locks                []StorageLockRow
	HeldLockCount        int
	MigrationDirection   string // "to-esb", "to-embedded", or ""
	MigrationCount       int
	CanMigrateToESB      bool
	CanMigrateToEmbedded bool
}

// MigrateFormPage renders the confirmation form for /storage/migrate.
// Pre-fill ESB_URL/TENANT_ID/PROJECT_ID from the project's .env so the
// user only needs to confirm — manual override is allowed.
type MigrateFormPage struct {
	Kind            PageKind
	Project         inspector.ProjectModel
	Mode            string
	DSN             string
	ESBURL          string
	TenantID        string
	ProjectID       string
	EventsToMigrate int    // events visible to be migrated
	Direction       string // "to-esb" or "to-embedded" (query param)
	Error           string // pre-fill error banner when a previous run was rejected
}
