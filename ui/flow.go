package ui

// Flow layout turns an inspector.FlowGraph into absolute SVG geometry.
//
// The layout is computed here rather than in the browser because the graph is
// a DAG with fixed layers — handler, method, event, store, projection, query —
// so column x is known up front and row y comes from the node's swimlane and
// its rank in it. That removes
// any need for a client-side layout library, which matters: the UI binary must
// run offline (see embed.go), so there is no CDN to pull d3 or mermaid from.
//
// Everything is deterministic: same model in, same pixels out. That keeps the
// page diffable in tests without rendering it.

import (
	"fmt"
	"math"
	"sort"
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
	flowPadTop  = 36 // room for the column titles
	flowPadBot  = 12
	flowMaxChar = 26 // label chars that fit flowNodeW at the node font size

	// Swimlanes: every aggregate gets a horizontal band across all columns.
	flowLaneHead = 22 // room for the lane title above its first row
	flowLaneGap  = 10 // space between two bands
)

// crossLane is the lane of nodes that belong to no single aggregate (a
// resolver, a multi-aggregate projection).
const crossLane = "lintas aggregate"

// FlowSVGLane is one horizontal band: all nodes of one aggregate.
type FlowSVGLane struct {
	Y, H   int
	Title  string
	TitleY int
	Alt    bool // every other band is shaded differently
}

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
	File  string // project-relative source file; "" means no code to open
	Line  int
	// Pre-computed baselines so the template does no arithmetic.
	LabelY int
	SubY   int
}

// FlowSVGEdge is one connector, as a cubic bezier path. From/To are node IDs,
// used by flow.js to trace a path through the graph.
type FlowSVGEdge struct {
	From, To string
	Path     string
	Inferred bool
	Op       string // into the event store: read or write
}

// FlowSVG is the whole drawing: canvas size plus positioned parts.
type FlowSVG struct {
	Width   int
	Height  int
	Columns []FlowSVGColumn
	Nodes   []FlowSVGNode
	Edges   []FlowSVGEdge
	Lanes   []FlowSVGLane
	LaneW   int // band width: the canvas minus a small inset on each side
	Empty   bool
}

// layoutFlow positions every node and routes every edge.
//
// Rows are swimlanes: each aggregate is one horizontal band across every
// column, so a slice (handler → method → event → store) mostly reads straight
// across and only cross-aggregate edges leave their band. Nodes without a
// single aggregate share a "lintas aggregate" band on top.
//
// Inside a band the event column keeps label order, and the other columns are
// ordered by the barycenter of their already-placed neighbours (methods and
// handlers by what they lead to, projections and queries by what feeds them),
// which removes most edge crossings without a layout library.
func layoutFlow(g inspector.FlowGraph) FlowSVG {
	svg := FlowSVG{Empty: true}

	// Lanes in order: cross-aggregate first, then aggregates alphabetically.
	laneOf := func(n inspector.FlowNode) string {
		if n.Aggregate == "" {
			return crossLane
		}
		return n.Aggregate
	}
	seen := map[string]bool{}
	var lanes []string
	for _, c := range g.Columns {
		for _, n := range c.Nodes {
			l := laneOf(n)
			if !seen[l] {
				seen[l] = true
				lanes = append(lanes, l)
			}
		}
	}
	sortLanes(lanes)

	// cells[lane][col] are the nodes of one band in one column.
	cells := map[string][][]inspector.FlowNode{}
	for _, l := range lanes {
		cells[l] = make([][]inspector.FlowNode, len(g.Columns))
	}
	for ci, c := range g.Columns {
		for _, n := range c.Nodes {
			l := laneOf(n)
			cells[l][ci] = append(cells[l][ci], n)
		}
	}

	// Band geometry depends only on counts, so it is fixed before ordering.
	laneTop := map[string]int{}
	y := flowPadTop
	for i, l := range lanes {
		rows := 0
		for _, cell := range cells[l] {
			if len(cell) > rows {
				rows = len(cell)
			}
		}
		h := flowLaneHead + rows*(flowNodeH+flowGapY)
		laneTop[l] = y
		svg.Lanes = append(svg.Lanes, FlowSVGLane{Y: y, H: h, Title: l, TitleY: y + 15, Alt: i%2 == 1})
		y += h + flowLaneGap
	}
	bottom := y

	neighbours := map[string][]string{}
	for _, e := range g.Edges {
		neighbours[e.From] = append(neighbours[e.From], e.To)
		neighbours[e.To] = append(neighbours[e.To], e.From)
	}

	// Place columns starting from the event column (or the first one), then
	// outwards, so every column is ordered against one already placed.
	anchor := 0
	for ci, c := range g.Columns {
		if c.Kind == inspector.FlowEvent {
			anchor = ci
		}
	}
	order := []int{anchor}
	for ci := anchor - 1; ci >= 0; ci-- {
		order = append(order, ci)
	}
	for ci := anchor + 1; ci < len(g.Columns); ci++ {
		order = append(order, ci)
	}

	centre := map[string]int{} // node ID → y of its centre, once placed
	for _, ci := range order {
		x := flowPadX + ci*(flowNodeW+flowColGap)
		for _, l := range lanes {
			cell := cells[l][ci]
			if ci != anchor {
				orderByBarycenter(cell, neighbours, centre)
			}
			for i, n := range cell {
				ny := laneTop[l] + flowLaneHead + i*(flowNodeH+flowGapY)
				sub := flowSubtitle(n)
				if n.Warn == "" && l != crossLane {
					// The band already names the aggregate.
					sub = strings.TrimPrefix(sub, n.Aggregate+" · ")
				}
				svg.Nodes = append(svg.Nodes, FlowSVGNode{
					ID:     n.ID,
					Kind:   string(n.Kind),
					X:      x,
					Y:      ny,
					W:      flowNodeW,
					H:      flowNodeH,
					Label:  truncateLabel(n.Label, flowMaxChar),
					Sub:    truncateLabel(sub, flowMaxChar+4),
					Warn:   n.Warn,
					Full:   flowTooltip(n),
					File:   n.Source.File,
					Line:   n.Source.Line,
					LabelY: ny + 20,
					SubY:   ny + 36,
				})
				centre[n.ID] = ny + flowNodeH/2
				svg.Empty = false
			}
		}
	}
	for ci, c := range g.Columns {
		svg.Columns = append(svg.Columns, FlowSVGColumn{
			Title: c.Title,
			X:     flowPadX + ci*(flowNodeW+flowColGap),
			Count: len(c.Nodes),
		})
	}

	colOf := map[string]int{}
	for ci, c := range g.Columns {
		for _, n := range c.Nodes {
			colOf[n.ID] = ci
		}
	}
	for _, e := range g.Edges {
		y1, okFrom := centre[e.From]
		y2, okTo := centre[e.To]
		if !okFrom || !okTo {
			continue
		}
		x1 := flowPadX + colOf[e.From]*(flowNodeW+flowColGap) + flowNodeW
		x2 := flowPadX + colOf[e.To]*(flowNodeW+flowColGap)
		ctrl := flowColGap / 2
		svg.Edges = append(svg.Edges, FlowSVGEdge{
			From: e.From,
			To:   e.To,
			Path: fmt.Sprintf("M%d,%d C%d,%d %d,%d %d,%d",
				x1, y1, x1+ctrl, y1, x2-ctrl, y2, x2, y2),
			Inferred: e.Inferred,
			Op:       e.Op,
		})
	}

	svg.Width = flowPadX*2 + len(g.Columns)*flowNodeW + (len(g.Columns)-1)*flowColGap
	svg.LaneW = svg.Width - 4
	svg.Height = bottom - flowLaneGap + flowPadBot
	if svg.Empty {
		svg.Lanes = nil
		svg.Height = flowPadTop + flowPadBot
	}
	return svg
}

// sortLanes puts the cross-aggregate lane first and the rest alphabetically.
func sortLanes(lanes []string) {
	sort.SliceStable(lanes, func(i, j int) bool {
		if (lanes[i] == crossLane) != (lanes[j] == crossLane) {
			return lanes[i] == crossLane
		}
		return lanes[i] < lanes[j]
	})
}

// orderByBarycenter sorts cell by the mean y of each node's placed neighbours.
// Nodes with none keep their relative order after the rest.
func orderByBarycenter(cell []inspector.FlowNode, neighbours map[string][]string, centre map[string]int) {
	key := make(map[string]float64, len(cell))
	for _, n := range cell {
		sum, count := 0, 0
		for _, other := range neighbours[n.ID] {
			if y, ok := centre[other]; ok {
				sum += y
				count++
			}
		}
		if count == 0 {
			key[n.ID] = math.MaxFloat64
		} else {
			key[n.ID] = float64(sum) / float64(count)
		}
	}
	sort.SliceStable(cell, func(i, j int) bool { return key[cell[i].ID] < key[cell[j].ID] })
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
