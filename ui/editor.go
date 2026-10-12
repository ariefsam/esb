package ui

// The aggregate editor: /editor/{aggregate} shows one aggregate's flow
// diagram next to an editable Go source pane, with every file that makes up
// the aggregate (domain, service, projection, handler, query) one pick away.
//
// It is the only part of the UI that writes files directly, so every write
// path is narrow on purpose:
//   - a file is editable only if it belongs to the aggregate (editorFiles),
//     resolves inside the project root, and is a regular .go file ≤ 1 MiB;
//   - a save must be valid Go (it is gofmt'ed before it is written) and must
//     carry the hash of the content it was based on, so a change made on disk
//     meanwhile is a 409 instead of being silently overwritten;
//   - saves and commits run under RunStore.Exclusive, so they never overlap a
//     generator command that is writing files;
//   - every POST passes checkSameOrigin, like /commands/execute.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"go/scanner"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ariefsam/esb/inspector"
)

// EditorFile is one file the editor offers for an aggregate.
type EditorFile struct {
	Path  string // project-relative, slash-separated
	Group string // folder it lives in: domain, service, projection, handler, …
}

// editorFiles lists the files that make up aggregate agg: its domain file,
// command service and scenario tests, the projections and handlers bound to
// it, its queries, and every file its flow diagram points at. Only existing
// regular .go files inside the project root are returned.
func editorFiles(m inspector.ProjectModel, root string, agg inspector.Aggregate) []EditorFile {
	var candidates []string
	add := func(rel string) { candidates = append(candidates, rel) }
	glob := func(pattern string) {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		sort.Strings(matches)
		for _, mm := range matches {
			if rel, err := filepath.Rel(root, mm); err == nil {
				add(filepath.ToSlash(rel))
			}
		}
	}

	add("domain/" + agg.FileName + ".go")
	for _, s := range m.Service {
		if s.Aggregate == agg.Name {
			add(s.File)
			glob(strings.TrimSuffix(s.File, ".go") + "_*test.go")
		}
	}
	for _, p := range m.Projection {
		if contains(p.Aggregates, agg.Name) {
			add("projection/" + p.Name + ".go")
			glob("projection/" + p.Name + "_*.go")
		}
	}
	glob("projection/" + agg.FileName + "_*.go")
	for _, h := range m.Handler {
		if h.Aggregate == agg.Name {
			add("server/handler/" + h.Name + ".go")
		}
	}
	for _, q := range m.Query {
		if q.Aggregate == agg.Name && q.File != "" {
			add(q.File)
		}
	}
	g := inspector.BuildFlowWith(m, inspector.FlowOptions{Aggregates: []string{agg.Name}})
	inspector.AttachSources(m, root, &g)
	for _, c := range g.Columns {
		for _, n := range c.Nodes {
			if n.Source.File != "" {
				add(n.Source.File)
			}
		}
	}

	seen := map[string]bool{}
	var files []EditorFile
	for _, rel := range candidates {
		rel = path.Clean(rel)
		if seen[rel] || !strings.HasSuffix(rel, ".go") {
			continue
		}
		seen[rel] = true
		if _, err := resolveProjectFile(root, rel); err != nil {
			continue
		}
		group := path.Dir(rel)
		if group == "server/handler" {
			group = "handler"
		}
		files = append(files, EditorFile{Path: rel, Group: group})
	}
	return files
}

// resolveProjectFile returns the real path of project-relative file rel, or
// an error when it is absolute, escapes the root (also through a symlink), is
// not a regular file, or is larger than maxSourceBytes.
func resolveProjectFile(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(path.Clean(rel), "..") {
		return "", errors.New("invalid path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
		return "", errors.New("outside project root")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSourceBytes {
		return "", errors.New("not an editable file")
	}
	return resolved, nil
}

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// editorContext is what every /editor/{aggregate}/… request resolves first.
type editorContext struct {
	model inspector.ProjectModel
	agg   inspector.Aggregate
	files []EditorFile
}

func (c editorContext) has(rel string) bool {
	for _, f := range c.files {
		if f.Path == rel {
			return true
		}
	}
	return false
}

func (c editorContext) paths() []string {
	out := make([]string, len(c.files))
	for i, f := range c.files {
		out[i] = f.Path
	}
	return out
}

// handleEditor dispatches /editor/{aggregate}[/{action}].
func (s *Server) handleEditor(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/editor/"), "/")
	name, action, _ := strings.Cut(rest, "/")
	if name == "" {
		s.notFound(w, r, "Aggregate tidak ditentukan", "Pilih aggregate dari sidebar.")
		return
	}

	model, err := inspector.Scan(s.projectRoot)
	if err != nil {
		if action == "" {
			s.renderScanError(w, r, err)
		} else {
			writeEditorError(w, http.StatusInternalServerError, "scan gagal: "+err.Error())
		}
		return
	}
	var ctx editorContext
	found := false
	for _, a := range model.Aggregate {
		if a.Name == name {
			ctx = editorContext{model: model, agg: a, files: editorFiles(model, s.projectRoot, a)}
			found = true
			break
		}
	}
	if !found {
		if action == "" {
			s.notFound(w, r, "Aggregate tidak ditemukan", fmt.Sprintf("Aggregate %q tidak ada pada proyek ini.", name))
		} else {
			writeEditorError(w, http.StatusNotFound, fmt.Sprintf("aggregate %q tidak ditemukan", name))
		}
		return
	}

	routes := map[string]struct {
		method string
		fn     func(http.ResponseWriter, *http.Request, editorContext)
	}{
		"":        {http.MethodGet, s.editorPage},
		"file":    {http.MethodGet, s.editorFile},
		"diagram": {http.MethodGet, s.editorDiagram},
		"history": {http.MethodGet, s.editorHistory},
		"version": {http.MethodGet, s.editorVersion},
		"save":    {http.MethodPost, s.editorSave},
		"check":   {http.MethodPost, s.editorCheck},
		"commit":  {http.MethodPost, s.editorCommit},
	}
	route, ok := routes[action]
	if !ok {
		s.notFound(w, r, "Halaman tidak ditemukan", fmt.Sprintf("Route %s tidak terdaftar.", r.URL.Path))
		return
	}
	if r.Method != route.method {
		w.Header().Set("Allow", route.method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if route.method == http.MethodPost {
		if err := checkSameOrigin(r); err != nil {
			writeEditorError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	route.fn(w, r, ctx)
}

// editorDiagramSVG lays out the aggregate's own slice of the flow graph.
func (s *Server) editorDiagramSVG(ctx editorContext) FlowSVG {
	g := inspector.BuildFlowWith(ctx.model, inspector.FlowOptions{Aggregates: []string{ctx.agg.Name}})
	inspector.AttachSources(ctx.model, s.projectRoot, &g)
	return layoutFlow(g)
}

func (s *Server) editorPage(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	initial := r.URL.Query().Get("file")
	if !ctx.has(initial) {
		initial = ""
		if len(ctx.files) > 0 {
			initial = ctx.files[0].Path
		}
	}
	page := EditorPage{
		Kind:      PageEditor,
		Module:    ctx.model.ModuleName,
		Aggregate: ctx.agg.Name,
		Files:     ctx.files,
		Initial:   initial,
		Line:      atoiDefault(r.URL.Query().Get("line"), 1),
		SVG:       s.editorDiagramSVG(ctx),
	}
	s.renderLayout(w, http.StatusOK, Layout{
		Title:     ctx.agg.Name,
		Subtitle:  "Editor aggregate",
		Root:      s.projectRoot,
		Body:      s.renderBody(page),
		Nav:       defaultNav("aggregate:" + ctx.agg.Name),
		Wide:      true,
		Aggregate: ctx.agg.Name,
	})
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

func (s *Server) editorFile(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	rel := r.URL.Query().Get("path")
	if !ctx.has(rel) {
		writeEditorError(w, http.StatusNotFound, "file bukan bagian dari aggregate ini")
		return
	}
	full, err := resolveProjectFile(s.projectRoot, rel)
	if err != nil {
		writeEditorError(w, http.StatusNotFound, "file tidak bisa dibaca")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeEditorError(w, http.StatusNotFound, "file tidak bisa dibaca")
		return
	}
	writeEditorJSON(w, http.StatusOK, map[string]string{"path": rel, "code": string(data), "hash": contentHash(data)})
}

func (s *Server) editorDiagram(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	var b strings.Builder
	if err := s.templates.ExecuteTemplate(&b, "flow_svg", s.editorDiagramSVG(ctx)); err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// Problem is one compile, vet or parse error, positioned in a file.
type Problem struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

// maxEditorBody bounds a save request: the source plus JSON overhead.
const maxEditorBody = maxSourceBytes + 64<<10

func (s *Server) editorSave(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	var req struct {
		Path string `json:"path"`
		Code string `json:"code"`
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEditorBody)).Decode(&req); err != nil {
		writeEditorError(w, http.StatusBadRequest, "request tidak valid: "+err.Error())
		return
	}
	if !ctx.has(req.Path) {
		writeEditorError(w, http.StatusNotFound, "file bukan bagian dari aggregate ini")
		return
	}
	if len(req.Code) > maxSourceBytes {
		writeEditorError(w, http.StatusRequestEntityTooLarge, "file lebih dari 1 MiB")
		return
	}

	formatted, err := format.Source([]byte(req.Code))
	if err != nil {
		writeEditorJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "kode belum valid Go, belum disimpan",
			"problems": parseProblems(req.Path, err),
		})
		return
	}

	var current []byte
	err = s.runs.Exclusive(func() error {
		full, err := resolveProjectFile(s.projectRoot, req.Path)
		if err != nil {
			return err
		}
		current, err = os.ReadFile(full)
		if err != nil {
			return err
		}
		if contentHash(current) != req.Hash {
			return errStale
		}
		return writeFileAtomic(full, formatted)
	})
	switch {
	case errors.Is(err, ErrConflict):
		writeEditorError(w, http.StatusConflict, "command esb sedang berjalan, coba simpan lagi setelah selesai")
	case errors.Is(err, errStale):
		writeEditorJSON(w, http.StatusConflict, map[string]any{
			"error": "file berubah di disk sejak dibuka",
			"stale": true,
			"code":  string(current),
			"hash":  contentHash(current),
		})
	case err != nil:
		writeEditorError(w, http.StatusInternalServerError, "gagal menyimpan: "+err.Error())
	default:
		writeEditorJSON(w, http.StatusOK, map[string]string{"code": string(formatted), "hash": contentHash(formatted)})
	}
}

var errStale = errors.New("file changed on disk")

// parseProblems turns a go/scanner error list into Problems for file rel.
func parseProblems(rel string, err error) []Problem {
	var list scanner.ErrorList
	if !errors.As(err, &list) {
		return []Problem{{File: rel, Line: 1, Column: 1, Message: err.Error()}}
	}
	out := make([]Problem, 0, len(list))
	for _, e := range list {
		out = append(out, Problem{File: rel, Line: e.Pos.Line, Column: e.Pos.Column, Message: e.Msg})
	}
	return out
}

// writeFileAtomic replaces path with data through a temp file in the same
// directory, keeping the file's permissions, so a crash never leaves a
// half-written source file.
func writeFileAtomic(full string, data []byte) error {
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), "."+filepath.Base(full)+".esb-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), full)
}

// checkMu keeps one `go vet` at a time; a second click gets 409.
var checkMu sync.Mutex

// checkTimeout bounds `go vet ./...`, which compiles the whole project.
const checkTimeout = 2 * time.Minute

// vetLine matches "path/file.go:12:5: message" in go vet / build output.
var vetLine = regexp.MustCompile(`^(?:vet: )?(?:\./)?([^\s:]+\.go):(\d+):(?:(\d+):)? (.*)$`)

func (s *Server) editorCheck(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	if !checkMu.TryLock() {
		writeEditorError(w, http.StatusConflict, "check sedang berjalan")
		return
	}
	defer checkMu.Unlock()

	runCtx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "go", "vet", "./...")
	cmd.Dir = s.projectRoot
	out, err := cmd.CombinedOutput()
	if runCtx.Err() != nil {
		writeEditorError(w, http.StatusGatewayTimeout, "go vet melebihi batas waktu")
		return
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		writeEditorError(w, http.StatusInternalServerError, "go tidak ditemukan di PATH")
		return
	}

	problems := []Problem{}
	var other []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := vetLine.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			problems = append(problems, Problem{File: m[1], Line: ln, Column: max(col, 1), Message: m[4]})
			continue
		}
		other = append(other, line)
	}
	writeEditorJSON(w, http.StatusOK, map[string]any{
		"ok":       err == nil,
		"problems": problems,
		"output":   strings.Join(other, "\n"),
	})
}

func writeEditorJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeEditorError(w http.ResponseWriter, status int, msg string) {
	writeEditorJSON(w, status, map[string]string{"error": msg})
}

// ---- git: history and commit ----------------------------------------------

// gitTimeout bounds every git call; commit hooks get the same budget.
const gitTimeout = time.Minute

// commitRef is what a version request may name: an abbreviated or full SHA.
var commitRef = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// git runs git in the project root without a shell and without ever
// prompting for credentials, returning stdout, or stderr as the error.
func (s *Server) git(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.projectRoot
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 2000 {
			msg = msg[:2000] + "…"
		}
		return stdout.Bytes(), errors.New(msg)
	}
	return stdout.Bytes(), nil
}

// gitAvailable reports whether the project is inside a git work tree.
func (s *Server) gitAvailable(ctx context.Context) error {
	out, err := s.git(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return fmt.Errorf("proyek ini bukan repo git: %v", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return errors.New("proyek ini bukan repo git")
	}
	return nil
}

// HistoryEntry is one commit that touched a file.
type HistoryEntry struct {
	Commit  string `json:"commit"`
	Short   string `json:"short"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

func (s *Server) editorHistory(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	rel := r.URL.Query().Get("path")
	if !ctx.has(rel) {
		writeEditorError(w, http.StatusNotFound, "file bukan bagian dari aggregate ini")
		return
	}
	if err := s.gitAvailable(r.Context()); err != nil {
		writeEditorJSON(w, http.StatusOK, map[string]any{"available": false, "reason": err.Error(), "entries": []HistoryEntry{}})
		return
	}
	out, err := s.git(r.Context(), "log", "-n", "100", "--format=%H%x1f%h%x1f%an%x1f%aI%x1f%s", "--", rel)
	if err != nil {
		// A repo without commits yet has no history, which is not an error.
		writeEditorJSON(w, http.StatusOK, map[string]any{"available": true, "entries": []HistoryEntry{}, "reason": err.Error()})
		return
	}
	entries := []HistoryEntry{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 5 {
			continue
		}
		entries = append(entries, HistoryEntry{Commit: f[0], Short: f[1], Author: f[2], Date: f[3], Subject: f[4]})
	}
	writeEditorJSON(w, http.StatusOK, map[string]any{"available": true, "entries": entries})
}

func (s *Server) editorVersion(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	rel := r.URL.Query().Get("path")
	commit := r.URL.Query().Get("commit")
	if !ctx.has(rel) {
		writeEditorError(w, http.StatusNotFound, "file bukan bagian dari aggregate ini")
		return
	}
	if !commitRef.MatchString(commit) {
		writeEditorError(w, http.StatusBadRequest, "commit tidak valid")
		return
	}
	// "./" makes the path relative to the project root even when the git
	// repository root is a parent directory.
	out, err := s.git(r.Context(), "show", commit+":./"+rel)
	if err != nil {
		writeEditorError(w, http.StatusNotFound, err.Error())
		return
	}
	writeEditorJSON(w, http.StatusOK, map[string]string{"code": string(out)})
}

// maxCommitMessage bounds the commit message a request may carry.
const maxCommitMessage = 4000

// editorCommit commits the aggregate's files, and only those: other changes
// in the work tree or already staged elsewhere are left as they are.
func (s *Server) editorCommit(w http.ResponseWriter, r *http.Request, ctx editorContext) {
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommitMessage+1024)).Decode(&req); err != nil {
		writeEditorError(w, http.StatusBadRequest, "request tidak valid: "+err.Error())
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeEditorError(w, http.StatusBadRequest, "pesan commit wajib diisi")
		return
	}
	if len(msg) > maxCommitMessage || strings.ContainsRune(msg, 0) {
		writeEditorError(w, http.StatusBadRequest, "pesan commit tidak valid")
		return
	}
	if err := s.gitAvailable(r.Context()); err != nil {
		writeEditorError(w, http.StatusConflict, err.Error())
		return
	}

	files := ctx.paths()
	var commit string
	committed := false
	err := s.runs.Exclusive(func() error {
		if _, err := s.git(r.Context(), append([]string{"add", "--"}, files...)...); err != nil {
			return fmt.Errorf("git add: %w", err)
		}
		// diff --cached --quiet exits 1 when the files have staged changes.
		if _, err := s.git(r.Context(), append([]string{"diff", "--cached", "--quiet", "--"}, files...)...); err == nil {
			return nil
		}
		if _, err := s.git(r.Context(), append([]string{"commit", "-m", msg, "--"}, files...)...); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
		out, err := s.git(r.Context(), "rev-parse", "--short", "HEAD")
		if err != nil {
			return err
		}
		commit, committed = strings.TrimSpace(string(out)), true
		return nil
	})
	switch {
	case errors.Is(err, ErrConflict):
		writeEditorError(w, http.StatusConflict, "command esb sedang berjalan, coba commit lagi setelah selesai")
	case err != nil:
		writeEditorError(w, http.StatusInternalServerError, err.Error())
	default:
		writeEditorJSON(w, http.StatusOK, map[string]any{"committed": committed, "commit": commit, "files": files})
	}
}
