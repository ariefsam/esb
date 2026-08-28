package ui

// Flow layout turns an inspector.FlowGraph into absolute SVG geometry.
//
// The layout is computed here rather than in the browser because the graph is
// a DAG with fixed layers — handler, command, event, projection, query — so
// column x is known up front and row y is just the node's index. That removes
// any need for a client-side layout library, which matters: the UI binary must
// run offline (see embed.go), so there is no CDN to pull d3 or mermaid from.
//
// Everything is deterministic: same model in, same pixels out. That keeps the
// page diffable in tests without rendering it.

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/inspector"
)

// Flow layout constants, in SVG user units (== CSS px at scale 1).
const (
	flowNodeW   = 196
	flowNodeH   = 48
	flowGapY    = 12
	flowColGap  = 88
	flowPadX    = 10
	flowPadTop  = 36
	flowPadBot  = 12
	flowMaxChar = 26 // label chars that fit flowNodeW at the node font size
)

// FlowSVGColumn is one column header.
type FlowSVGColumn struct {
	Title string
	X     int
	Count int
}

// FlowSVGNode is one positioned box. Label/Sub are already truncated for
// display; Full carries the untruncated text for the hover <title>.
type FlowSVGNode struct {
	ID    string
	Kind  string
	X, Y  int
	W, H  int
	Label string
	Sub   string
	Warn  string
	Full  string
	// Pre-computed baselines so the template does no arithmetic.
	LabelY int
	SubY   int
}

// FlowSVGEdge is one connector, as a cubic bezier path.
type FlowSVGEdge struct {
	Path     string
	Inferred bool
}

// FlowSVG is the whole drawing: canvas size plus positioned parts.
type FlowSVG struct {
	Width   int
	Height  int
	Columns []FlowSVGColumn
	Nodes   []FlowSVGNode
	Edges   []FlowSVGEdge
	Empty   bool
}

// layoutFlow positions every node and routes every edge. Nodes keep the order
// the inspector produced (aggregate, then label), so the drawing is stable
// across refreshes and groups an aggregate's boxes together.
func layoutFlow(g inspector.FlowGraph) FlowSVG {
	svg := FlowSVG{Empty: true}
	anchors := map[string]struct{ x, y int }{}

	rows := 0
	for col, column := range g.Columns {
		x := flowPadX + col*(flowNodeW+flowColGap)
		svg.Columns = append(svg.Columns, FlowSVGColumn{
			Title: column.Title,
			X:     x,
			Count: len(column.Nodes),
		})
		if len(column.Nodes) > rows {
			rows = len(column.Nodes)
		}
		for i, n := range column.Nodes {
			y := flowPadTop + i*(flowNodeH+flowGapY)
			svg.Nodes = append(svg.Nodes, FlowSVGNode{
				ID:     n.ID,
				Kind:   string(n.Kind),
				X:      x,
				Y:      y,
				W:      flowNodeW,
				H:      flowNodeH,
				Label:  truncateLabel(n.Label, flowMaxChar),
				Sub:    truncateLabel(flowSubtitle(n), flowMaxChar+4),
				Warn:   n.Warn,
				Full:   flowTooltip(n),
				LabelY: y + 20,
				SubY:   y + 36,
			})
			anchors[n.ID] = struct{ x, y int }{x, y + flowNodeH/2}
			svg.Empty = false
		}
	}

	for _, e := range g.Edges {
		from, okFrom := anchors[e.From]
		to, okTo := anchors[e.To]
		if !okFrom || !okTo {
			continue
		}
		x1 := from.x + flowNodeW
		x2 := to.x
		ctrl := flowColGap / 2
		svg.Edges = append(svg.Edges, FlowSVGEdge{
			Path: fmt.Sprintf("M%d,%d C%d,%d %d,%d %d,%d",
				x1, from.y, x1+ctrl, from.y, x2-ctrl, to.y, x2, to.y),
			Inferred: e.Inferred,
		})
	}

	svg.Width = flowPadX*2 + len(g.Columns)*flowNodeW + (len(g.Columns)-1)*flowColGap
	svg.Height = flowPadTop + rows*(flowNodeH+flowGapY) + flowPadBot
	if svg.Empty {
		svg.Height = flowPadTop + flowPadBot
	}
	return svg
}

// flowSubtitle picks the node's second line: the warning when there is one,
// since a dead end is the more useful thing to read at a glance.
func flowSubtitle(n inspector.FlowNode) string {
	if n.Warn != "" {
		return n.Warn
	}
	return n.Sub
}

// flowTooltip is the untruncated hover text, so a name clipped in the box is
// still recoverable without leaving the page.
func flowTooltip(n inspector.FlowNode) string {
	parts := []string{n.Label}
	if n.Sub != "" {
		parts = append(parts, n.Sub)
	}
	if n.Warn != "" {
		parts = append(parts, "⚠ "+n.Warn)
	}
	return strings.Join(parts, " — ")
}

// truncateLabel clips s to max runes, ending in an ellipsis. It counts runes
// rather than bytes so a multi-byte name is not cut mid-character.
func truncateLabel(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}
