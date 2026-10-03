package inspector

import (
	"encoding/json"
	"fmt"
	"io"
	"math"

	"gopkg.in/yaml.v3"
)

// FlowExportVersion is bumped on any breaking change to FlowExport so
// scripts that consume `esb show flow` and /flow.json can detect it.
const FlowExportVersion = 1

// FlowExport is the machine-readable form of the Flow page. It is a flat
// projection of BuildFlow and BuildStats: nodes carry their layer in Kind
// (column order is derivable), and every edge states whether it is Inferred
// so a consumer never mistakes a naming-convention guess for a call.
type FlowExport struct {
	Version int              `json:"version" yaml:"version"`
	Project string           `json:"project" yaml:"project"`
	Filter  string           `json:"filter" yaml:"filter"`
	Stats   FlowExportStats  `json:"stats" yaml:"stats"`
	Nodes   []FlowExportNode `json:"nodes" yaml:"nodes"`
	Edges   []FlowExportEdge `json:"edges" yaml:"edges"`
	Gaps    []FlowExportGap  `json:"gaps" yaml:"gaps"`
}

// FlowExportStats always covers the whole project, never just the filter.
type FlowExportStats struct {
	Aggregates        int     `json:"aggregates" yaml:"aggregates"`
	Events            int     `json:"events" yaml:"events"`
	Commands          int     `json:"commands" yaml:"commands"`
	HandlerMethods    int     `json:"handler_methods" yaml:"handler_methods"`
	Projections       int     `json:"projections" yaml:"projections"`
	Queries           int     `json:"queries" yaml:"queries"`
	AvgFieldsPerEvent float64 `json:"avg_fields_per_event" yaml:"avg_fields_per_event"`
	UnproducedEvents  int     `json:"unproduced_events" yaml:"unproduced_events"`
	UnconsumedEvents  int     `json:"unconsumed_events" yaml:"unconsumed_events"`
	DynamicCommands   int     `json:"dynamic_commands" yaml:"dynamic_commands"`
}

type FlowExportNode struct {
	ID        string `json:"id" yaml:"id"`
	Kind      string `json:"kind" yaml:"kind"`
	Label     string `json:"label" yaml:"label"`
	Sub       string `json:"sub" yaml:"sub"`
	Aggregate string `json:"aggregate" yaml:"aggregate"`
	Warn      string `json:"warn" yaml:"warn"`
	File      string `json:"file" yaml:"file"` // relative to the project, "" when not found
	Line      int    `json:"line" yaml:"line"`
}

type FlowExportEdge struct {
	From     string `json:"from" yaml:"from"`
	To       string `json:"to" yaml:"to"`
	Inferred bool   `json:"inferred" yaml:"inferred"`
	Op       string `json:"op,omitempty" yaml:"op,omitempty"` // into the event store: read or write
}

type FlowExportGap struct {
	Severity string `json:"severity" yaml:"severity"`
	Subject  string `json:"subject" yaml:"subject"`
	Message  string `json:"message" yaml:"message"`
}

// BuildFlowExport assembles the export for m. aggregate narrows nodes and
// edges exactly as BuildFlow does; the caller validates that it exists.
func BuildFlowExport(m ProjectModel, root, aggregate string) FlowExport {
	g := BuildFlow(m, aggregate)
	AttachSources(m, root, &g)
	s := BuildStats(m)

	out := FlowExport{
		Version: FlowExportVersion,
		Project: root,
		Filter:  aggregate,
		Stats: FlowExportStats{
			Aggregates:        s.Aggregates,
			Events:            s.Events,
			Commands:          s.Commands,
			HandlerMethods:    s.HandlerMethods,
			Projections:       s.Projections,
			Queries:           s.Queries,
			AvgFieldsPerEvent: math.Round(s.AvgFieldsPerEvent*100) / 100,
			UnproducedEvents:  s.UnproducedEvents,
			UnconsumedEvents:  s.UnconsumedEvents,
			DynamicCommands:   s.DynamicCommands,
		},
		// Non-nil so empty lists serialise as [] rather than null.
		Nodes: []FlowExportNode{},
		Edges: []FlowExportEdge{},
		Gaps:  []FlowExportGap{},
	}
	for _, c := range g.Columns {
		for _, n := range c.Nodes {
			out.Nodes = append(out.Nodes, FlowExportNode{
				ID: n.ID, Kind: string(n.Kind), Label: n.Label,
				Sub: n.Sub, Aggregate: n.Aggregate, Warn: n.Warn,
				File: n.Source.File, Line: n.Source.Line,
			})
		}
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, FlowExportEdge{From: e.From, To: e.To, Inferred: e.Inferred, Op: e.Op})
	}
	for _, gap := range s.Gaps {
		out.Gaps = append(out.Gaps, FlowExportGap{Severity: gap.Severity, Subject: gap.Subject, Message: gap.Message})
	}
	return out
}

// WriteFlowExport encodes f as "yaml" or "json".
func WriteFlowExport(w io.Writer, f FlowExport, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(f)
	case "yaml":
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(f); err != nil {
			return err
		}
		return enc.Close()
	default:
		return fmt.Errorf("unknown output format %q (want yaml or json)", format)
	}
}
