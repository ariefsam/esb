package generator

import (
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"

	"github.com/ariefsam/esb/injector"
)

// Client modes accepted by UpdateClient.
const (
	ClientModeAll      = "all"
	ClientModeEmbedded = "embedded"
	ClientModeESB      = "esb"
)

// clientFile is one generated file `esb update-client` keeps current.
// mode is the store it belongs to; "" means every mode needs it.
type clientFile struct {
	tmpl string
	dest string
	mode string
}

var clientFiles = []clientFile{
	{"eventstore_client.go.tmpl", "eventstore/client.go", ClientModeESB},
	{"repository_adapter.go.tmpl", "repository/eventstore_adapter.go", ClientModeESB},
	{"eventstore_local_store.go.tmpl", "eventstore/local_store.go", ClientModeEmbedded},
	{"repository_local_adapter.go.tmpl", "repository/local_adapter.go", ClientModeEmbedded},
	{"eventstore_fake_store.go.tmpl", "eventstore/fake_store.go", ""},
	{"domain_projection_wait.go.tmpl", "domain/projection_wait.go", ""},
	{"projection_wait.go.tmpl", "projection/wait.go", ""},
}

// waitSupportFiles are what StoreAndWaitProjectionWorker and the
// generated WaitPast methods need.
var waitSupportFiles = []clientFile{
	{"domain_projection_wait.go.tmpl", "domain/projection_wait.go", ""},
	{"projection_wait.go.tmpl", "projection/wait.go", ""},
}

// UpdateClientOptions configures UpdateClient.
type UpdateClientOptions struct {
	// Mode picks which store's client to update: ClientModeAll (the
	// default), ClientModeEmbedded or ClientModeESB. Files every mode
	// needs are always updated.
	Mode string
	// DryRun computes and reports the changes without writing.
	DryRun bool
}

// ClientFileChange is what UpdateClient did, or would do, to one file.
type ClientFileChange struct {
	Path    string
	Created bool     // the file did not exist and was written whole
	Added   []string // declarations added to an existing file
	Imports []string // imports added for them
	Differs []string // declarations the project changed; left alone
	Skipped []string // declarations that could not be added on their own
}

// Changed reports whether the file is written.
func (c ClientFileChange) Changed() bool { return c.Created || len(c.Added) > 0 }

// UpdateClient brings the event store client of the project in the
// current directory up to the templates of this esb version.
//
// A missing file is created. An existing one only gains the top-level
// declarations it lacks (and their imports), found by name through the
// Go AST: nothing the project already declares is changed, moved or
// removed, so hand edits survive. Declarations the project changed are
// reported in Differs, for a human to compare.
//
// Every file is computed before any is written; a file that fails to
// parse or merge aborts the whole update with nothing written.
func UpdateClient(opts UpdateClientOptions) ([]ClientFileChange, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ClientModeAll
	}
	if mode != ClientModeAll && mode != ClientModeEmbedded && mode != ClientModeESB {
		return nil, fmt.Errorf("unknown mode %q: use %s, %s or %s", opts.Mode, ClientModeAll, ClientModeEmbedded, ClientModeESB)
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return nil, err
	}
	data := ProjectData{ModuleName: moduleName}

	var changes []ClientFileChange
	outputs := map[string][]byte{}
	for _, f := range clientFiles {
		if f.mode != "" && mode != ClientModeAll && f.mode != mode {
			continue
		}
		rendered, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", f.dest, err)
		}
		change := ClientFileChange{Path: f.dest}
		existing, err := os.ReadFile(f.dest)
		switch {
		case errors.Is(err, os.ErrNotExist):
			out, err := formatGo(rendered)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.dest, err)
			}
			change.Created = true
			outputs[f.dest] = out
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", f.dest, err)
		default:
			res, err := injector.MergeDecls(existing, []byte(rendered))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.dest, err)
			}
			change.Added, change.Imports = res.Added, res.Imports
			change.Differs, change.Skipped = res.Differs, res.Skipped
			if change.Changed() {
				outputs[f.dest] = res.Source
			}
		}
		changes = append(changes, change)
	}

	if opts.DryRun {
		return changes, nil
	}
	for _, c := range changes {
		out, ok := outputs[c.Path]
		if !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
			return changes, err
		}
		if err := os.WriteFile(c.Path, out, 0o644); err != nil {
			return changes, fmt.Errorf("write %s: %w", c.Path, err)
		}
	}
	return changes, nil
}

// stageWaitSupport stages the files a generated WaitPast or storeAndWait
// needs, when a project from before them does not have them yet — so
// `esb add aggregate` in an old project still compiles without running
// `esb update-client` first.
func stageWaitSupport(tx *injector.Tx, moduleName string, actions *[]string) error {
	for _, f := range waitSupportFiles {
		if _, err := os.Stat(f.dest); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", f.dest, err)
		}
		content, err := renderTemplate(f.tmpl, ProjectData{ModuleName: moduleName})
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		*actions = append(*actions, "  create  "+f.dest)
	}
	return nil
}

func formatGo(src string) ([]byte, error) {
	out, err := format.Source([]byte(src))
	if err != nil {
		return nil, fmt.Errorf("rendered template does not gofmt: %w", err)
	}
	return out, nil
}
