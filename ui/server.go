package ui

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Server is the HTTP front-end exposed by `esb ui`. It owns the run
// store, the embedded asset FS, and a tiny template set. Construction
// parses every template up-front so malformed HTML fails the server
// immediately rather than on the first request.
type Server struct {
	projectRoot string
	runs        *RunStore
	runner      ProcessRunner
	templates   *template.Template
	staticFS    fs.FS
}

// Options configure a Server. ProjectRoot is required and is the
// directory whose generated files the UI will parse.
type Options struct {
	ProjectRoot string
	Runner      ProcessRunner
}

// NewServer builds a Server with the embedded templates parsed. A nil
// Runner is replaced with the production ExecRunner so callers can
// keep the constructor signature simple.
func NewServer(opts Options) (*Server, error) {
	if opts.ProjectRoot == "" {
		return nil, fmt.Errorf("project root is required")
	}
	if opts.Runner == nil {
		opts.Runner = ExecRunner{}
	}

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("sub static: %w", err)
	}

	return &Server{
		projectRoot: opts.ProjectRoot,
		runs:        NewRunStore(),
		runner:      opts.Runner,
		templates:   tmpl,
		staticFS:    sub,
	}, nil
}

// ProjectRoot returns the directory the UI is serving.
func (s *Server) ProjectRoot() string { return s.projectRoot }

// RunStore exposes the in-memory store so tests can inspect runs.
func (s *Server) RunStore() *RunStore { return s.runs }

// Handler returns an http.Handler that serves every route described
// in the plan. It is the only exported HTTP entry point — the cmd
// layer hands the listener straight to net/http.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/", s.routeRoot)
	mux.HandleFunc("/aggregates/", s.handleAggregate)
	mux.HandleFunc("/commands", s.handleCommands)
	mux.HandleFunc("/commands/execute", s.handleExecute)
	mux.HandleFunc("/commands/runs/", s.handleRun)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))))

	return securityHeadersMiddleware(logMiddleware(mux))
}

// parseTemplates loads every template file and wires up the funcMap.
// Layout, overview, aggregate_detail, commands, run_detail, and error
// each contribute one {{define}} block; the body is picked per
// request through RenderBody.
func parseTemplates() (*template.Template, error) {
	tmpl := template.New("ui").Funcs(template.FuncMap{
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"join":     strings.Join,
	})
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		data, err := fs.ReadFile(templatesFS, path.Join("templates", e.Name()))
		if err != nil {
			return nil, err
		}
		if _, err := tmpl.New(e.Name()).Parse(string(data)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
	}
	return tmpl, nil
}

// logMiddleware writes a single-line log for every request. It is the
// only middleware in v1 and is intentionally small so the loop stays
// observable without adding a logger dependency.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		fmt.Printf("esb ui %s %s %s\n", r.Method, r.URL.Path, time.Since(start))
	})
}

// securityHeadersMiddleware applies the minimum response headers
// every page in the UI must carry. It is the regression net for the
// X-Content-Type-Options header the original renderLayout set only on
// the HTML render path; without this middleware /healthz and the
// /static/ file server would silently omit it. Cache-Control:
// no-store keeps intermediate caches from holding the /commands/runs
// stream — a shared proxy caching a run-id response would otherwise
// replay a finished run as if it were still in progress.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// runContext is used as the parent for every background command run.
// It is independent of the HTTP request that started the run so the
// child process survives the request lifecycle — only server shutdown
// or the run timeout cancels it.
func (s *Server) runContext() context.Context {
	return context.Background()
}

// notFound writes the shared 404 page so handlers don't repeat the
// same boilerplate.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, title, message string) {
	page := ErrorPage{
		Kind:    PageError,
		Title:   title,
		Message: message,
		Back:    "/",
		Code:    http.StatusNotFound,
	}
	s.renderLayout(w, http.StatusNotFound, Layout{
		Title: title,
		Body:  s.renderBody(page),
		Root:  s.projectRoot,
		Nav:   defaultNav(""),
	})
}

// badRequest writes a 400 with a localized error page. Used by the
// execute handler for invalid forms.
func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, message string) {
	page := ErrorPage{
		Kind:    PageError,
		Title:   "Permintaan tidak valid",
		Message: message,
		Back:    "/commands",
		Code:    http.StatusBadRequest,
	}
	s.renderLayout(w, http.StatusBadRequest, Layout{
		Title: "Permintaan tidak valid",
		Body:  s.renderBody(page),
		Root:  s.projectRoot,
		Nav:   defaultNav("commands"),
	})
}

// methodNotAllowed writes a 405 page. The execute handler relies on
// this to reject GET hits on the mutating route.
func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	page := ErrorPage{
		Kind:    PageError,
		Title:   "Method tidak diizinkan",
		Message: fmt.Sprintf("Route %s hanya menerima POST.", r.URL.Path),
		Back:    "/commands",
		Code:    http.StatusMethodNotAllowed,
	}
	s.renderLayout(w, http.StatusMethodNotAllowed, Layout{
		Title: "Method tidak diizinkan",
		Body:  s.renderBody(page),
		Root:  s.projectRoot,
		Nav:   defaultNav("commands"),
	})
}

// defaultNav returns the top-nav links shared by every page. The
// active key drives the highlight class.
func defaultNav(active string) Nav {
	return Nav{
		Active: active,
		Links: []NavLink{
			{Href: "/", Text: "Overview", Key: "overview"},
			{Href: "/commands", Text: "Commands", Key: "commands"},
		},
	}
}

// renderLayout executes the layout template with the given body HTML
// already rendered. This split lets handlers pick the body template
// without changing the layout.
func (s *Server) renderLayout(w http.ResponseWriter, status int, layout Layout) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if layout.CSSPath == "" {
		layout.CSSPath = "/static/app.css"
	}
	if layout.JSPath == "" {
		layout.JSPath = "/static/app.js"
	}
	if err := s.templates.ExecuteTemplate(w, "layout", layout); err != nil {
		// We've already written headers so we can only log. A bad
		// template is a startup error and should not happen in
		// production; if it does, the body may be partial.
		fmt.Printf("esb ui render error: %v\n", err)
	}
}

// renderBody executes one of the body templates by name. The name is
// derived from the page kind so this helper stays the single render
// path for non-layout content.
func (s *Server) renderBody(page interface{}) string {
	var tmplName string
	switch p := page.(type) {
	case OverviewPage:
		tmplName = "overview"
	case AggregateDetailPage:
		tmplName = "aggregate_detail"
	case CommandsPage:
		tmplName = "commands"
	case RunPage:
		tmplName = "run_detail"
	case ErrorPage:
		tmplName = "error"
	default:
		_ = p
		tmplName = "error"
	}
	var b strings.Builder
	if err := s.templates.ExecuteTemplate(&b, tmplName, page); err != nil {
		return fmt.Sprintf("<!-- render error: %v -->", err)
	}
	return b.String()
}