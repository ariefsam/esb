package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ariefsam/esb/inspector"
)

var showFlowOutput string

var showFlowCmd = &cobra.Command{
	Use:   "flow [aggregate-name]",
	Short: "Print the code flow graph (handler → method → event → event store → projection → query) as YAML or JSON",
	Long: `Machine-readable form of the 'esb ui' /flow page. The graph is derived
from the project's source, not from stored events. Edges marked inferred:
true come from a naming convention (projection → query), not a call.
Stats always cover the whole project; the optional aggregate only narrows
nodes and edges.

Layers: handler → service method → event → event store → projection →
query. Edges into the event store carry op: read or write.

Examples:
  esb show flow
  esb show flow user-settings -o json | jq '.gaps'`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if showFlowOutput != "yaml" && showFlowOutput != "json" {
			return fmt.Errorf("unknown output format %q (want yaml or json)", showFlowOutput)
		}

		m, err := inspector.Scan(".")
		if err != nil {
			return err
		}
		focus := ""
		if len(args) == 1 {
			focus = args[0]
			found := false
			for _, a := range m.Aggregate {
				if a.Name == focus {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("aggregate %q not found in this project", focus)
			}
		}
		root, err := filepath.Abs(".")
		if err != nil {
			return err
		}
		out := inspector.BuildFlowExport(m, root, focus)
		return inspector.WriteFlowExport(os.Stdout, out, showFlowOutput)
	},
}

func init() {
	showFlowCmd.Flags().StringVarP(&showFlowOutput, "output", "o", "yaml", "output format: yaml or json")
	showCmd.AddCommand(showFlowCmd)
}
