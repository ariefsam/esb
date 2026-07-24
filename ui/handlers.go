package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ariefsam/esb/inspector"
)

// handleHealth is a tiny smoke endpoint. It returns 200 even when the
// project root is invalid so local monitoring scripts can distinguish
// "process up" from "project invalid".
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// routeRoot dispatches the bare "/" path. Anything else falls through
// to the 404 handler — paths like "/foo" are not valid routes.
func (s *Server) routeRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, r, "Halaman tidak ditemukan", fmt.Sprintf("Route %s tidak terdaftar.", r.URL.Path))
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.overview(w, r)
}

// overview renders the dashboard. A bad project root surfaces as a
// 404 error page so the user can fix their cwd.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	model, err := inspector.Scan(s.projectRoot)
	if err != nil {
		s.renderScanError(w, r, err)
		return
	}

	rows := make([]AggregateRow, 0, len(model.Aggregate))
	var eventCount int
	for _, a := range model.Aggregate {
		eventCount += len(a.Events)
		text := fmt.Sprintf("%d events", len(a.Events))
		if len(a.Events) > 0 {
			text = fmt.Sprintf("%d events: %s", len(a.Events), strings.Join(a.Events, ", "))
		}
		rows = append(rows, AggregateRow{
			Name:      a.Name,
			Events:    a.Events,
			EventText: text,
			Href:      "/aggregates/" + a.Name,
		})
	}

	page := OverviewPage{
		Kind:          PageOverview,
		Project:       model,
		EventCount:    eventCount,
		HandlerCount:  len(model.Handler),
		QueryCount:    len(model.Query),
		AggregateRows: rows,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Project Overview",
		Subtitle: fmt.Sprintf("%s — %d aggregates", model.ModuleName, len(model.Aggregate)),
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("overview"),
	})
}

// handleAggregate serves /aggregates/{name}. An empty name segment
// produces a 404, the same way an unknown name does.
func (s *Server) handleAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/aggregates/")
	name = strings.Trim(name, "/")
	if name == "" {
		s.notFound(w, r, "Aggregate tidak ditentukan", "Pilih aggregate dari overview.")
		return
	}

	model, err := inspector.Scan(s.projectRoot)
	if err != nil {
		s.renderScanError(w, r, err)
		return
	}

	var selected *inspector.Aggregate
	for i := range model.Aggregate {
		if model.Aggregate[i].Name == name || model.Aggregate[i].FileName == name {
			selected = &model.Aggregate[i]
			break
		}
	}
	if selected == nil {
		s.notFound(w, r, "Aggregate tidak ditemukan",
			fmt.Sprintf("Aggregate %q tidak ada pada proyek ini.", name))
		return
	}

	resolved := selected.Name

	var matchingHandlers []inspector.Handler
	for _, h := range model.Handler {
		if h.Aggregate == resolved {
			matchingHandlers = append(matchingHandlers, h)
		}
	}

	var matchingQueries []inspector.Query
	for _, q := range model.Query {
		if q.Aggregate == resolved {
			matchingQueries = append(matchingQueries, q)
		}
	}

	var matchingWorkers []inspector.Projection
	for _, p := range model.Projection {
		for _, a := range p.Aggregates {
			if a == resolved {
				matchingWorkers = append(matchingWorkers, p)
				break
			}
		}
	}

	var chains []inspector.WireNode
	for _, f := range model.Wire.Fields {
		base := strings.TrimSuffix(f.Field, "Handler")
		base = strings.TrimSuffix(base, "ProjectionWorker")
		baseSnake := snakeFromPascal(base)
		if baseSnake == "" {
			continue
		}
		if strings.HasSuffix(f.Field, "Handler") {
			for _, h := range matchingHandlers {
				if h.Name == baseSnake {
					chains = append(chains, f)
					break
				}
			}
		}
		if strings.HasSuffix(f.Field, "ProjectionWorker") {
			for _, p := range matchingWorkers {
				if p.Name == baseSnake {
					chains = append(chains, f)
					break
				}
			}
		}
	}

	var other []string
	for _, a := range model.Aggregate {
		if a.Name != resolved {
			other = append(other, a.Name)
		}
	}

	page := AggregateDetailPage{
		Kind:     PageAggregateDetail,
		Project:  model,
		Name:     resolved,
		Events:   selected.Events,
		Handlers: matchingHandlers,
		Queries:  matchingQueries,
		Workers:  matchingWorkers,
		Chains:   chains,
		Other:    other,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Aggregate " + resolved,
		Subtitle: fmt.Sprintf("%s — %d events", resolved, len(selected.Events)),
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("overview"),
	})
}

// snakeFromPascal is a tiny inline converter used to map PascalCase
// App field names ("OrderProjectionWorker") back to the snake_case
// names the inspector uses ("order").
func snakeFromPascal(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// handleCommands serves the GET /commands form page.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/commands" {
		s.notFound(w, r, "Halaman tidak ditemukan", fmt.Sprintf("Route %s tidak terdaftar.", r.URL.Path))
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	model, err := inspector.Scan(s.projectRoot)
	if err != nil {
		s.renderScanError(w, r, err)
		return
	}

	page := CommandsPage{
		Kind:     PageCommands,
		Project:  model,
		Commands: PublicCommands(),
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Commands",
		Subtitle: "Form untuk menjalankan command esb yang diizinkan",
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("commands"),
	})
}

// handleExecute accepts POST /commands/execute. Anything else returns
// 405; an unknown command, missing fields, or a conflict with an
// active run surfaces as a 400 page.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, r, "Form tidak bisa dibaca: "+err.Error())
		return
	}

	commandID := r.Form.Get("command")
	if findCommand(commandID) == nil {
		s.badRequest(w, r, fmt.Sprintf("Command %q tidak ada di allowlist.", commandID))
		return
	}

	form := ParseForm(r.Form)
	// Reject submissions that include fields outside the catalog so
	// the request can never smuggle extra argv positions through
	// the form parser. The synthetic "command" hidden field is the
	// route key, not a per-command field, so it is allowed.
	for k := range form {
		if k == "command" {
			continue
		}
		if !catalogHasField(commandID, k) {
			s.badRequest(w, r, fmt.Sprintf("Field %q tidak dikenal untuk command %s.", k, commandID))
			return
		}
	}

	if _, err := BuildArgv(commandID, form); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	if s.runs.Busy() {
		page := ErrorPage{
			Kind:    PageError,
			Title:   "Command lain sedang berjalan",
			Message: "Tunggu sampai command sebelumnya selesai sebelum menjalankan command baru.",
			Back:    "/commands",
			Code:    http.StatusConflict,
		}
		s.renderLayout(w, http.StatusConflict, Layout{
			Title: "Command lain sedang berjalan",
			Body:  s.renderBody(page),
			Root:  s.projectRoot,
			Nav:   defaultNav("commands"),
		})
		return
	}

	run, err := s.runs.Start(s.runContext(), s.projectRoot, commandID, form, s.runner)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			page := ErrorPage{
				Kind:    PageError,
				Title:   "Command lain sedang berjalan",
				Message: "Tunggu sampai command sebelumnya selesai sebelum menjalankan command baru.",
				Back:    "/commands",
				Code:    http.StatusConflict,
			}
			s.renderLayout(w, http.StatusConflict, Layout{
				Title: "Command lain sedang berjalan",
				Body:  s.renderBody(page),
				Root:  s.projectRoot,
				Nav:   defaultNav("commands"),
			})
			return
		}
		s.badRequest(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/commands/runs/"+run.ID, http.StatusSeeOther)
}

// catalogHasField reports whether the catalog entry for id declares
// a field with the given name.
func catalogHasField(id, name string) bool {
	cmd := findCommand(id)
	if cmd == nil {
		return false
	}
	for _, f := range cmd.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// handleRun serves /commands/runs/{id}.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/commands/runs/")
	id = strings.Trim(id, "/")
	if id == "" {
		s.notFound(w, r, "Run tidak ditemukan", "ID run kosong.")
		return
	}

	run := s.runs.Get(id)
	if run == nil {
		s.notFound(w, r, "Run tidak ditemukan", fmt.Sprintf("Run %s tidak ada di memory store.", id))
		return
	}
	// Read the running flag under the lock so the polling widget in
	// the template does not race with the executor goroutine.
	status, _, _ := s.runs.StatusSnapshot(id)
	running := status == RunRunning

	model, err := inspector.Scan(s.projectRoot)
	if err != nil {
		// Run detail is more important than the parsed overview; if
		// the scan now fails we still want to show stdout/stderr. The
		// project model stays zero-valued, which the template renders
		// as empty sections.
		model = inspector.ProjectModel{}
	}

	page := RunPage{
		Kind:    PageRunDetail,
		Project: model,
		Run:     run,
		Found:   true,
		Running: running,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Run " + run.ID,
		Subtitle: fmt.Sprintf("%s — %s", run.CommandID, run.Status),
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("commands"),
	})
}

// renderScanError turns an inspector error into a 404 page that names
// the exact failure. We use 404 (rather than 500) because the most
// common cause is the user launching the UI from outside an ESB
// project — a fixable condition, not a server bug.
func (s *Server) renderScanError(w http.ResponseWriter, r *http.Request, err error) {
	title := "Bukan proyek ESB"
	msg := err.Error()
	var nf *inspector.NotFound
	if errors.As(err, &nf) {
		title = "Bukan proyek ESB"
	}
	s.renderLayout(w, http.StatusNotFound, Layout{
		Title: title,
		Body: s.renderBody(ErrorPage{
			Kind:    PageError,
			Title:   title,
			Message: msg,
			Back:    "/",
			Code:    http.StatusNotFound,
		}),
		Root: s.projectRoot,
		Nav:  defaultNav(""),
	})
}