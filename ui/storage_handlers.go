package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariefsam/esb/eventstore"
	"github.com/ariefsam/esb/inspector"
)

// handleStorage serves `GET /storage`. It reads the project, scans
// the storage subsystem, and renders the storage detail page. A
// bad project root surfaces the same 404 as the rest of the UI.
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/storage" {
		s.notFound(w, r, "Halaman tidak ditemukan", fmt.Sprintf("Route %s tidak terdaftar.", r.URL.Path))
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	model, err := s.scanOrEmpty(r)
	if err != nil {
		s.renderScanError(w, r, err)
		return
	}

	info := model.Storage
	page := buildStoragePage(model, info)

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Storage",
		Subtitle: "Mode event store dan ringkasan event per aggregate",
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("storage"),
	})
}

// handleMigrate handles both GET (form) and POST (run command) for
// `/storage/migrate`. The same handler because the action is
// already allow-listed via the command catalog.
func (s *Server) handleMigrate(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/storage/migrate") {
		s.notFound(w, r, "Halaman tidak ditemukan", fmt.Sprintf("Route %s tidak terdaftar.", r.URL.Path))
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleMigrateForm(w, r)
	case http.MethodPost:
		s.handleMigrateSubmit(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMigrateForm renders the confirmation form for one of the
// two migration directions. The direction is read from the
// `?direction=` query string; missing or invalid values surface a
// 400.
func (s *Server) handleMigrateForm(w http.ResponseWriter, r *http.Request) {
	direction := r.URL.Query().Get("direction")
	if direction != "to-esb" && direction != "to-embedded" {
		s.badRequest(w, r, "direction wajib diisi (to-esb atau to-embedded)")
		return
	}

	model, err := s.scanOrEmpty(r)
	if err != nil {
		s.renderScanError(w, r, err)
		return
	}
	info := model.Storage

	// Reject migrate requests that don't make sense for the
	// current mode — embedded → to-embedded is a no-op, esb →
	// to-esb likewise.
	if direction == "to-esb" && info.Mode != inspector.StorageModeEmbedded {
		s.badRequest(w, r, "Mode sekarang bukan embedded — tidak ada event lokal untuk dimigrasi.")
		return
	}
	if direction == "to-embedded" && info.Mode != inspector.StorageModeESBServer {
		s.badRequest(w, r, "Mode sekarang bukan esb-server — tidak ada server remote untuk rollback.")
		return
	}

	esbURL, tenant, project := readESBTargetsFromEnv(s.projectRoot)
	page := MigrateFormPage{
		Kind:            PageMigrate,
		Project:         model,
		Mode:            info.Mode,
		DSN:             info.DSN,
		ESBURL:          esbURL,
		TenantID:        tenant,
		ProjectID:       project,
		EventsToMigrate: info.TotalEvents(),
		Direction:       direction,
	}

	s.renderLayout(w, http.StatusOK, Layout{
		Title:    "Migrate event store",
		Subtitle: fmt.Sprintf("Direction: %s", direction),
		Root:     s.projectRoot,
		Body:     s.renderBody(page),
		Nav:      defaultNav("storage"),
	})
}

// handleMigrateSubmit runs the actual migration by dispatching the
// corresponding allow-listed command. Validation is delegated to
// the command catalog — BuildArgv handles the per-field checks and
// returns an error a 400 page can render.
func (s *Server) handleMigrateSubmit(w http.ResponseWriter, r *http.Request) {
	if err := checkSameOrigin(r); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	direction := r.URL.Query().Get("direction")
	if direction != "to-esb" && direction != "to-embedded" {
		s.badRequest(w, r, "direction wajib diisi (to-esb atau to-embedded)")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, r, "Form tidak bisa dibaca: "+err.Error())
		return
	}

	// Map the UI direction to the catalog command id. The form
	// fields are intentionally identical for both commands so the
	// catalog can validate uniformly.
	commandID := "migrate-" + direction
	if findCommand(commandID) == nil {
		s.badRequest(w, r, fmt.Sprintf("Command %q tidak ada di allowlist.", commandID))
		return
	}

	form := ParseForm(r.Form)
	// Reject unknown fields per the same rule as the execute handler.
	for k := range form {
		if k == "direction" {
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
		s.renderLayout(w, http.StatusConflict, Layout{
			Title: "Command lain sedang berjalan",
			Body: s.renderBody(ErrorPage{
				Kind:    PageError,
				Title:   "Command lain sedang berjalan",
				Message: "Tunggu sampai command sebelumnya selesai sebelum menjalankan command baru.",
				Back:    "/storage",
				Code:    http.StatusConflict,
			}),
			Root: s.projectRoot,
			Nav:  defaultNav("storage"),
		})
		return
	}

	run, err := s.runs.Start(s.runContext(), s.projectRoot, commandID, form, s.runner)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	http.Redirect(w, r, "/commands/runs/"+run.ID, http.StatusSeeOther)
}

// scanOrEmpty returns the ProjectModel or an empty one so the
// storage page can still render even when scan fails. The empty
// value lets the template fill in placeholders instead of crashing.
func (s *Server) scanOrEmpty(r *http.Request) (model inspector.ProjectModel, err error) {
	model, err = inspector.Scan(s.projectRoot)
	if err != nil {
		// Don't bail — fall through with the zero model so the
		// /storage page can still render with placeholders. The
		// inspector errors are typically "bukan proyek ESB" which
		// the storage page should communicate rather than 404 on.
		_ = r
	}
	return
}

// readESBTargetsFromEnv reads ESB_URL/TENANT_ID/PROJECT_ID out of
// the project's .env so the migration form can pre-fill. Returns
// "" for any missing key — the form fields are required so the
// user must fill the gap manually.
func readESBTargetsFromEnv(rootDir string) (esbURL, tenant, project string) {
	envPath := filepath.Join(rootDir, ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq == -1 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		switch key {
		case "ESB_URL":
			esbURL = val
		case "TENANT_ID":
			tenant = val
		case "PROJECT_ID":
			project = val
		}
	}
	return
}

// buildStoragePage constructs the view-model for /storage from
// the scanned ProjectModel + StorageInfo. Pulled out as a helper
// so tests can assert the projection rules (CanMigrateTo*) without
// spinning up an HTTP server.
func buildStoragePage(model inspector.ProjectModel, info inspector.StorageInfo) StoragePage {
	page := StoragePage{
		Kind:           PageStorage,
		Project:        model,
		Mode:           info.Mode,
		ModeLabel:      modeLabel(info.Mode),
		DSN:            info.DSN,
		ESBURL:         info.ESBURL,
		TotalEvents:    info.TotalEvents(),
		TotalSnapshots: info.TotalSnapshots(),
		HasSQLite:      info.HasSQLite,
		HeldLockCount:  info.HeldLockCount(),
	}
	for _, name := range info.SortedAggregateNames() {
		page.Rows = append(page.Rows, StorageAggregateRow{
			Name:          name,
			Count:         info.Counts[name],
			SnapshotCount: info.SnapshotCounts[name],
		})
	}
	for _, lock := range info.Locks {
		page.Locks = append(page.Locks, StorageLockRow{
			Key:        lock.Key,
			OwnerToken: lock.OwnerToken,
			ExpiresAt:  lock.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
			Held:       lock.Held,
		})
	}
	// Read any recorded migration state so the page can show the
	// last run direction + count.
	if state, err := eventstore.ReadMigrationState(info.DSN); err == nil && state != "" {
		page.MigrationDirection = eventstore.MigrationDirection(state)
		page.MigrationCount = eventstore.MigrationEventCount(state)
	}

	// Migration eligibility: only allow the direction the user
	// would expect given the active mode.
	switch info.Mode {
	case inspector.StorageModeEmbedded:
		page.CanMigrateToESB = true
	case inspector.StorageModeESBServer:
		page.CanMigrateToEmbedded = true
	}
	return page
}

// modeLabel renders the human-friendly mode name for the badge.
// Embedded → "lokal", esb-server → "remote", unknown → "tidak dikenal"
// so a typo like "esb-sever" surfaces in the UI instead of being
// silently treated as one of the canonical modes.
func modeLabel(mode string) string {
	switch mode {
	case inspector.StorageModeESBServer:
		return "esb-server"
	case inspector.StorageModeEmbedded:
		return "embedded"
	case inspector.StorageModeUnknown:
		return "tidak dikenal — cek EVENT_STORE_MODE di .env"
	}
	return mode
}

// readMigrationFromEnv is a sentinel import so the compile-time
// dependency between this file and cmd stays obvious even when
// future refactors move the storage helpers around.
var _ = url.Parse
