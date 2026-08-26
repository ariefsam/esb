package ui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

	subtitle := fmt.Sprintf("%s — %d aggregates", model.ModuleName, len(model.Aggregate))
	page := OverviewPage{
		Kind:          PageOverview,
		Project:       model,
		Title:         "Project Overview",
		Subtitle:      subtitle,
		EventCount:    eventCount,
		HandlerCount:  len(model.Handler),
		QueryCount:    len(model.Query),
		AggregateRows: rows,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Project Overview",
		Subtitle: subtitle,
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("overview"),
	})
}

// handleAggregate serves /aggregates/{name}. An empty name segment
// produces a 404, the same way an unknown name does.
func (s *Server) handleAggregate(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/aggregates/"), "/")

	// Delete-event sub-route: <name>/events/<event>/delete. Dispatched here
	// because the flat mux routes everything under /aggregates/ to this handler.
	if parts := strings.Split(path, "/"); len(parts) == 4 && parts[1] == "events" && parts[3] == "delete" {
		s.handleDeleteEvent(w, r, parts[0], parts[2])
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path
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

	var other []string
	for _, a := range model.Aggregate {
		if a.Name != resolved {
			other = append(other, a.Name)
		}
	}

	page := AggregateDetailPage{
		Kind:         PageAggregateDetail,
		Project:      model,
		Name:         resolved,
		FileName:     selected.FileName,
		Events:       selected.Events,
		EventDetails: selected.EventDetails,
		Handlers:     matchingHandlers,
		Queries:      matchingQueries,
		Workers:      matchingWorkers,
		Other:        other,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Aggregate " + resolved,
		Subtitle: fmt.Sprintf("%s — %d events", resolved, len(selected.Events)),
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("overview"),
	})
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

	groups := PublicCommandGroups()
	commands := PublicCommands()
	aggregateNames := make([]string, 0, len(model.Aggregate))
	for _, aggregate := range model.Aggregate {
		aggregateNames = append(aggregateNames, aggregate.Name)
	}
	addAggregateSuggestions(commands, aggregateNames)
	for i := range groups {
		addAggregateSuggestions(groups[i].Commands, aggregateNames)
	}

	page := CommandsPage{
		Kind:     PageCommands,
		Project:  model,
		Groups:   groups,
		Commands: commands,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Commands",
		Subtitle: "Form untuk menjalankan command esb yang diizinkan",
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("commands"),
	})
}

func addAggregateSuggestions(commands []CommandView, aggregates []string) {
	for i := range commands {
		if commands[i].ID != "add-projection" {
			continue
		}
		for j := range commands[i].Fields {
			if commands[i].Fields[j].Name == "aggregates" {
				commands[i].Fields[j].Suggestions = aggregates
			}
		}
	}
}

// handleExecute accepts POST /commands/execute. Anything else returns
// 405; an unknown command, missing fields, or a conflict with an
// active run surfaces as a 400 page.
// handleDeleteEvent serves the delete-event confirm page (GET) and, after the
// stored-data check + confirmation, starts the delete-event run (POST). It is
// the only entry point for the hidden delete-event command, so the data check
// cannot be bypassed.
func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request, name, event string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		s.notFound(w, r, "Aggregate tidak ditemukan", fmt.Sprintf("Aggregate %q tidak ada.", name))
		return
	}
	known := false
	for _, e := range selected.Events {
		if e == event {
			known = true
			break
		}
	}
	if !known {
		s.notFound(w, r, "Event tidak ditemukan", fmt.Sprintf("Event %q tidak ada di aggregate %q.", event, selected.Name))
		return
	}

	storage := inspector.ScanStorage(s.projectRoot)
	// We can only claim a verified count when we actually opened the embedded
	// events table. No readable DB (esb-server, no .env/DSN, or the store never
	// ran) means "cannot verify" — never a false "safe".
	canVerify := storage.Mode == inspector.StorageModeEmbedded && storage.HasSQLite
	count := storage.EventCount(selected.Name, event)
	page := DeleteEventPage{
		Kind:          PageDeleteEvent,
		AggregateName: selected.Name,
		AggregateFile: selected.FileName,
		EventName:     event,
		Mode:          storage.Mode,
		CanVerify:     canVerify,
		EventCount:    count,
		RequireAck:    (canVerify && count > 0) || !canVerify,
	}

	if r.Method == http.MethodGet {
		s.renderDeleteEvent(w, http.StatusOK, page)
		return
	}

	// POST: same-origin, confirmation gate, then run the hidden command.
	if err := checkSameOrigin(r); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, r, "Form tidak bisa dibaca: "+err.Error())
		return
	}
	if page.RequireAck && r.Form.Get("ack") != "on" {
		page.Error = "Centang konfirmasi dulu sebelum menghapus."
		s.renderDeleteEvent(w, http.StatusBadRequest, page)
		return
	}

	form := FormInput{"aggregate": {selected.FileName}, "event": {event}}
	run, err := s.runs.Start(s.runContext(), s.projectRoot, "delete-event", form, s.runner)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			s.badRequest(w, r, "Command lain sedang berjalan; tunggu selesai lalu coba lagi.")
			return
		}
		s.badRequest(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/commands/runs/"+run.ID, http.StatusSeeOther)
}

func (s *Server) renderDeleteEvent(w http.ResponseWriter, status int, page DeleteEventPage) {
	s.renderLayout(w, status, Layout{
		Title: "Hapus event " + page.EventName,
		Root:  s.projectRoot,
		Body:  s.renderBody(page),
		Nav:   defaultNav("overview"),
	})
}

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, r)
		return
	}
	// Same-origin check: requests to a localhost tool shouldn't accept
	// cross-site form posts. The browser sends `Origin` on every CORS
	// request; if present, it must match the request's Host header.
	if err := checkSameOrigin(r); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, r, "Form tidak bisa dibaca: "+err.Error())
		return
	}

	commandID := r.Form.Get("command")
	if c := findCommand(commandID); c == nil || c.Hidden {
		s.badRequest(w, r, fmt.Sprintf("Command %q tidak bisa dijalankan dari sini.", commandID))
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
		if errors.Is(err, ErrStoreFull) {
			s.renderLayout(w, http.StatusServiceUnavailable, Layout{
				Title: "Run store penuh",
				Body: s.renderBody(ErrorPage{
					Kind:    PageError,
					Title:   "Run store penuh",
					Message: fmt.Sprintf("Run store sudah mencapai batas %d. Restart esb ui untuk membersihkan.", s.runs.Cap()),
					Back:    "/commands",
					Code:    http.StatusServiceUnavailable,
				}),
				Root: s.projectRoot,
				Nav:  defaultNav("commands"),
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

// checkSameOrigin enforces same-origin on POST /commands/execute. The
// UI is localhost-only, but a cross-site form post from another
// origin could still trigger a command — Origin / Referer matching
// closes that gap. A missing Origin header on a POST is treated as a
// non-browser client (curl, etc.) and is rejected so the rule is
// consistent; legitimate browser requests always send Origin on POST.
func checkSameOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	host := r.Host
	if origin == "" {
		return fmt.Errorf("origin header wajib ada untuk POST")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("origin %q tidak valid: %w", origin, err)
	}
	if u.Host == "" {
		return fmt.Errorf("origin %q tidak punya host", origin)
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}
	if !strings.EqualFold(u.Scheme, requestScheme) || !strings.EqualFold(u.Host, host) {
		return fmt.Errorf("origin %q tidak sama dengan request origin %s://%s", origin, requestScheme, host)
	}
	return nil
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

	// Get returns a copy, so the handler can render it after releasing
	// the store lock without racing the executor.
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
