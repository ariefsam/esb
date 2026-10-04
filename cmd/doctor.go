package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/inspector"
)

var (
	doctorOutput string
	doctorStrict bool
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Report what esb could not understand in the project, with fixes",
	Long: `Scan the project the way 'esb show flow' and 'esb ui' do, and report two
things separately:

  Tidak dipahami scanner  code esb could not read: a file that does not
                          parse, an event name computed at runtime, a handler
                          whose calls lead nowhere esb knows, a query without
                          a recognisable table. Each comes with file:line and
                          the fix, usually an annotation.
  Celah alur              what the project does not have yet: an event no
                          command emits, an event no projection handles.

Annotations (line comments in your code):

  // esb:emits OrderPlaced, cart/CartClosed  service method: events it stores
  // esb:reads budget-cycle                  service method: aggregates it loads
  // esb:writes envelope                     service method: aggregates it stores to
  // esb:no-projection                       domain file (or an event type):
                                             no projection needed, on purpose
  // esb:ignore                              handler/service/projection function:
                                             leave it out of the flow

Exit status is 1 when there is a "warn" finding the scanner could not
understand; with --strict, also when the flow has a "warn" gap.

Examples:
  esb doctor
  esb doctor --strict -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if doctorOutput != "text" && doctorOutput != "json" {
			return fmt.Errorf("unknown output format %q (want text or json)", doctorOutput)
		}
		m, err := inspector.Scan(".")
		if err != nil {
			return err
		}
		root, err := filepath.Abs(".")
		if err != nil {
			return err
		}
		gaps := inspector.BuildStats(m).Gaps
		// Warnings first: they are what to look at.
		sort.SliceStable(gaps, func(i, j int) bool {
			return gaps[i].Severity == "warn" && gaps[j].Severity != "warn"
		})
		if doctorOutput == "json" {
			if err := writeDoctorJSON(os.Stdout, root, m.Diagnostics, gaps); err != nil {
				return err
			}
		} else {
			writeDoctorText(os.Stdout, root, m.Diagnostics, gaps)
		}
		if n := countLevel(m.Diagnostics, gaps, doctorStrict); n > 0 {
			return fmt.Errorf("doctor: %d temuan berlevel warn", n)
		}
		return nil
	},
}

func init() {
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "text", "output format: text or json")
	doctorCmd.Flags().BoolVar(&doctorStrict, "strict", false, "also fail on warn-level flow gaps")
	rootCmd.AddCommand(doctorCmd)
}

func countLevel(diags []inspector.Diagnostic, gaps []inspector.Gap, strict bool) int {
	n := 0
	for _, d := range diags {
		if d.Level == "warn" {
			n++
		}
	}
	if strict {
		for _, g := range gaps {
			if g.Severity == "warn" {
				n++
			}
		}
	}
	return n
}

func writeDoctorText(w io.Writer, root string, diags []inspector.Diagnostic, gaps []inspector.Gap) {
	fmt.Fprintf(w, "esb doctor — %s\n\n", root)
	fmt.Fprintf(w, "Tidak dipahami scanner (%d)\n", len(diags))
	if len(diags) == 0 {
		fmt.Fprintln(w, "  (tidak ada)")
	}
	for _, d := range diags {
		loc := d.File
		if d.Line > 0 {
			loc = fmt.Sprintf("%s:%d", d.File, d.Line)
		}
		fmt.Fprintf(w, "  %-4s  %s  [%s]\n        %s\n        → %s\n", d.Level, loc, d.Code, d.Message, d.Hint)
	}
	fmt.Fprintf(w, "\nCelah alur (%d)\n", len(gaps))
	if len(gaps) == 0 {
		fmt.Fprintln(w, "  (tidak ada)")
	}
	for _, g := range gaps {
		fmt.Fprintf(w, "  %-4s  %s — %s\n", g.Severity, g.Subject, g.Message)
	}
}

func writeDoctorJSON(w io.Writer, root string, diags []inspector.Diagnostic, gaps []inspector.Gap) error {
	type diag struct {
		Level   string `json:"level"`
		Code    string `json:"code"`
		File    string `json:"file"`
		Line    int    `json:"line"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	}
	type gap struct {
		Severity string `json:"severity"`
		Subject  string `json:"subject"`
		Message  string `json:"message"`
	}
	out := struct {
		Project     string `json:"project"`
		Diagnostics []diag `json:"diagnostics"`
		Gaps        []gap  `json:"gaps"`
	}{Project: root, Diagnostics: []diag{}, Gaps: []gap{}}
	for _, d := range diags {
		out.Diagnostics = append(out.Diagnostics, diag{d.Level, d.Code, d.File, d.Line, d.Message, d.Hint})
	}
	for _, g := range gaps {
		out.Gaps = append(out.Gaps, gap{g.Severity, g.Subject, g.Message})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
