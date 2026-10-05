package injector

import (
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Tx stages edits to multiple files in memory so a multi-file generator step
// can be all-or-nothing. Reads are cached; mutations update the cached copy.
// Nothing touches disk until Commit, and Commit validates every staged Go
// file before writing any of them — so a missing marker, a missing file, or
// output that does not gofmt aborts the whole step with the project left
// exactly as it was, instead of a half-wired result.
type Tx struct {
	files map[string]*txFile
	order []string // paths in the order first touched, for deterministic writes
}

type txFile struct {
	content string
	dirty   bool // staged for writing on Commit
}

// NewTx returns an empty transaction.
func NewTx() *Tx {
	return &Tx{files: map[string]*txFile{}}
}

// get returns the cached file, loading it from disk on first access. Files
// staged via Create are returned without a disk read.
func (t *Tx) get(path string) (*txFile, error) {
	if f, ok := t.files[path]; ok {
		return f, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	f := &txFile{content: string(src)}
	t.track(path, f)
	return f, nil
}

func (t *Tx) track(path string, f *txFile) {
	if _, ok := t.files[path]; !ok {
		t.order = append(t.order, path)
	}
	t.files[path] = f
}

// Create stages a brand-new file's full content, replacing any staged copy.
// Used for freshly rendered template output.
func (t *Tx) Create(path, content string) {
	t.track(path, &txFile{content: content, dirty: true})
}

// Append adds content to the end of a file (loaded from disk if not staged).
func (t *Tx) Append(path, content string) error {
	f, err := t.get(path)
	if err != nil {
		return err
	}
	f.content += content
	f.dirty = true
	return nil
}

// Contains reports whether the staged (or on-disk) content of path contains
// needle as a whole token run, never as part of a longer identifier (see
// containsIdent). Used to guard against double-injection within the
// transaction.
func (t *Tx) Contains(path, needle string) (bool, error) {
	f, err := t.get(path)
	if err != nil {
		return false, err
	}
	return containsIdent(f.content, needle), nil
}

// InjectAfterMarker stages an insertion after marker in path.
func (t *Tx) InjectAfterMarker(path, marker, code string) error {
	f, err := t.get(path)
	if err != nil {
		return err
	}
	result, err := injectAfterMarker(f.content, marker, code)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	f.content = result
	f.dirty = true
	return nil
}

// EnsureImport stages the addition of importPath to path's import block if it
// is not already present.
func (t *Tx) EnsureImport(path, importPath string) error {
	f, err := t.get(path)
	if err != nil {
		return err
	}
	result, err := ensureImport(f.content, importPath)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	f.content = result
	f.dirty = true
	return nil
}

// Commit writes every staged change to disk. It first validates that all
// staged Go files gofmt cleanly; if any does not, nothing is written and the
// offending file's parse error is returned. Individual writes use a
// write-to-temp-then-rename so a file is never observed half-written, and if
// a write fails midway the files already written are restored (or removed,
// if they were new) so the step stays all-or-nothing on disk too.
func (t *Tx) Commit() error {
	type pending struct {
		path string
		out  []byte
		orig original
	}
	var writes []pending

	for _, path := range t.order {
		f := t.files[path]
		if !f.dirty {
			continue
		}
		out := []byte(f.content)
		if strings.HasSuffix(path, ".go") {
			formatted, err := format.Source(out)
			if err != nil {
				return fmt.Errorf("refusing to write invalid Go to %s: %w", path, err)
			}
			out = formatted
		}
		orig, err := snapshot(path)
		if err != nil {
			return err
		}
		writes = append(writes, pending{path: path, out: out, orig: orig})
	}

	for i, w := range writes {
		if err := atomicWrite(w.path, w.out, w.orig.mode()); err != nil {
			var rollbackErrs []error
			for _, done := range writes[:i] {
				if rbErr := done.orig.restore(done.path); rbErr != nil {
					rollbackErrs = append(rollbackErrs, rbErr)
				}
			}
			if len(rollbackErrs) > 0 {
				return fmt.Errorf("%w (rollback failed, project may be partially written: %v)", err, errors.Join(rollbackErrs...))
			}
			return err
		}
	}
	return nil
}

// original is a file's on-disk state before Commit touched it.
type original struct {
	existed bool
	data    []byte
	perm    fs.FileMode
}

func snapshot(path string) (original, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return original{}, nil
	}
	if err != nil {
		return original{}, fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return original{}, fmt.Errorf("read %s: %w", path, err)
	}
	return original{existed: true, data: data, perm: info.Mode().Perm()}, nil
}

// mode is the permission the rewritten file should keep: its existing mode,
// or 0644 for a new file.
func (o original) mode() fs.FileMode {
	if o.existed {
		return o.perm
	}
	return 0644
}

// restore puts path back the way snapshot found it.
func (o original) restore(path string) error {
	if !o.existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	return atomicWrite(path, o.data, o.perm)
}

// atomicWrite writes data to a sibling temp file then renames it over path so
// readers never see a partial file. Directories are created as needed. The
// temp file is created 0600, so perm is applied before the rename.
func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".esb-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	return nil
}
