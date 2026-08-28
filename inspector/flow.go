package inspector

// BuildFlow turns a scanned ProjectModel into the layered graph the UI draws
// and `esb show` summarises. The graph is derived, never re-parsed: everything
// here reads fields the scanners already populated, so a flow edge can only be
// as good as the declaration it came from.
//
// The five layers follow the write-then-read path of an event-sourced request:
//
//	HTTP handler → service command → event → projection worker → query
//
// Edges within the first three layers are exact — they come from real call
// expressions and string literals. The last edge (worker → query) is inferred
// from the aggregate a query's row type belongs to, so it is marked Inferred
// and rendered dashed; the UI must not present it as fact.

import (
	"fmt"
	"sort"
	"strings"
)

// FlowKind is the layer a node belongs to.
type FlowKind string

const (
	FlowHandler    FlowKind = "handler"
	FlowCommand    FlowKind = "command"
	FlowEvent      FlowKind = "event"
	FlowProjection FlowKind = "projection"
	FlowQuery      FlowKind = "query"
)

// FlowNode is one box in the graph.
type FlowNode struct {
	ID        string // stable and unique, e.g. "event:product/ProductCreated"
	Kind      FlowKind
	Label     string // what the box shows
	Sub       string // second line: owning aggregate, or a qualifier
	Aggregate string // "" when the node belongs to no single aggregate
	Warn      string // non-empty marks a dead end and is shown on the node
}

// FlowEdge connects two node IDs. Inferred edges are drawn dashed because
// they are derived from a naming convention rather than a call expression.
type FlowEdge struct {
	From     string
	To       string
	Inferred bool
}

// FlowColumn is one rendered layer, already ordered.
type FlowColumn struct {
	Kind  FlowKind
	Title string
	Nodes []FlowNode
}

// FlowGraph is the whole picture: ordered columns plus the edges between them.
type FlowGraph struct {
	Columns []FlowColumn
	Edges   []FlowEdge
}

// Gap is one structural problem worth surfacing. Severity is "warn" for a
// broken or dead-ended path and "info" for something merely unfinished.
type Gap struct {
	Severity string
	Subject  string
	Message  string
}

// Stats are the derived counts shown above the graph.
type Stats struct {
	Aggregates        int
	Events            int
	Services          int
	Commands          int
	Handlers          int
	HandlerMethods    int
	Projections       int
	MultiProjections  int
	Queries           int
	EventFields       int
	AvgFieldsPerEvent float64
	UnproducedEvents  int // declared but no command emits them
	UnconsumedEvents  int // emitted but no worker handles them
	DynamicCommands   int // commands whose event name is computed at runtime
	Gaps              []Gap
}

// BuildFlow assembles the graph for m. When aggregate is non-empty the graph
// is narrowed to nodes touching that aggregate, which keeps a large project
// readable; nodes belonging to no aggregate are always kept.
func BuildFlow(m ProjectModel, aggregate string) FlowGraph {
	b := newFlowBuilder(m)
	b.addCommands()
	b.addEvents()
	b.addHandlers()
	b.addProjections()
	b.addQueries()
	return b.graph(aggregate)
}

// flowBuilder accumulates nodes per layer plus the edges between them, so the
// per-layer helpers stay small and independent.
type flowBuilder struct {
	model ProjectModel
	nodes map[FlowKind][]FlowNode
	edges []FlowEdge

	// declared maps aggregate → set of event names declared in domain/,
	// used to spot events a service emits without a matching struct.
	declared map[string]map[string]bool
	// emitted maps aggregate → set of event names some command emits.
	emitted map[string]map[string]bool
	// consumed maps aggregate → set of event names some worker handles.
	consumed map[string]map[string]bool
	// dynamic marks aggregates whose service computes event names at
	// runtime, which suppresses "no producer" warnings for them.
	dynamic map[string]bool
}

func newFlowBuilder(m ProjectModel) *flowBuilder {
	b := &flowBuilder{
		model:    m,
		nodes:    map[FlowKind][]FlowNode{},
		declared: map[string]map[string]bool{},
		emitted:  map[string]map[string]bool{},
		consumed: map[string]map[string]bool{},
		dynamic:  map[string]bool{},
	}
	for _, a := range m.Aggregate {
		b.declared[a.Name] = map[string]bool{}
		for _, e := range a.Events {
			b.declared[a.Name][e] = true
		}
	}
	for _, s := range m.Service {
		for _, c := range s.Commands {
			if c.Dynamic {
				b.dynamic[s.Aggregate] = true
			}
			for _, e := range c.Emits {
				b.mark(b.emitted, s.Aggregate, e)
			}
		}
	}
	for _, p := range m.Projection {
		for _, e := range p.Events {
			b.mark(b.consumed, b.aggregateOfEvent(p, e), e)
		}
	}
	return b
}

func (b *flowBuilder) mark(set map[string]map[string]bool, aggregate, event string) {
	if set[aggregate] == nil {
		set[aggregate] = map[string]bool{}
	}
	set[aggregate][event] = true
}

// aggregateOfEvent resolves which of a worker's subscribed aggregates declares
// event. A multi-aggregate worker can list several, so the declaration decides;
// with no match the first subscription is used, which keeps the node attached
// to something rather than dropping the edge.
func (b *flowBuilder) aggregateOfEvent(p Projection, event string) string {
	for _, agg := range p.Aggregates {
		if b.declared[agg][event] {
			return agg
		}
	}
	if len(p.Aggregates) > 0 {
		return p.Aggregates[0]
	}
	return ""
}

func commandID(service, name string) string { return "command:" + service + "." + name }
func eventID(aggregate, name string) string { return "event:" + aggregate + "/" + name }
func handlerID(file, method string) string  { return "handler:" + file + "." + method }
func projectionID(name string) string       { return "projection:" + name }
func queryID(name string) string            { return "query:" + name }
func (b *flowBuilder) add(n FlowNode)       { b.nodes[n.Kind] = append(b.nodes[n.Kind], n) }
func (b *flowBuilder) link(from, to string) { b.edges = append(b.edges, FlowEdge{From: from, To: to}) }
func (b *flowBuilder) guess(from, to string) {
	b.edges = append(b.edges, FlowEdge{From: from, To: to, Inferred: true})
}

// addCommands creates one node per service command and links it to every event
// it emits. A command that emits nothing recognisable is still shown, warned,
// so a dynamic emitter does not silently vanish from the picture.
func (b *flowBuilder) addCommands() {
	for _, s := range b.model.Service {
		for _, c := range s.Commands {
			id := commandID(s.Name, c.Name)
			warn := ""
			if c.Dynamic && len(c.Emits) == 0 {
				warn = "event name computed at runtime"
			}
			b.add(FlowNode{
				ID:        id,
				Kind:      FlowCommand,
				Label:     c.Name,
				Sub:       s.Name,
				Aggregate: s.Aggregate,
				Warn:      warn,
			})
			for _, e := range c.Emits {
				b.link(id, eventID(s.Aggregate, e))
			}
		}
	}
}

// addEvents creates one node per declared event, plus a node for any event a
// command emits without a matching struct in domain/ — that mismatch is drift
// worth seeing rather than hiding.
func (b *flowBuilder) addEvents() {
	for _, a := range b.model.Aggregate {
		for _, detail := range a.EventDetails {
			b.add(FlowNode{
				ID:        eventID(a.Name, detail.Name),
				Kind:      FlowEvent,
				Label:     detail.Name,
				Sub:       fmt.Sprintf("%s · %d field", a.Name, len(detail.Fields)),
				Aggregate: a.Name,
				Warn:      b.eventWarn(a.Name, detail.Name),
			})
		}
	}
	for aggregate, events := range b.emitted {
		for event := range events {
			if b.declared[aggregate][event] {
				continue
			}
			b.add(FlowNode{
				ID:        eventID(aggregate, event),
				Kind:      FlowEvent,
				Label:     event,
				Sub:       aggregate,
				Aggregate: aggregate,
				Warn:      "emitted but not declared in domain/",
			})
		}
	}
}

// eventWarn reports the dead end a declared event sits on, if any. "No
// producer" is suppressed for aggregates with a dynamic emitter, because there
// the scanner genuinely cannot tell and a warning would be noise.
func (b *flowBuilder) eventWarn(aggregate, event string) string {
	produced := b.emitted[aggregate][event] || b.dynamic[aggregate]
	consumed := b.consumed[aggregate][event]
	switch {
	case !produced && !consumed:
		return "no producer, no consumer"
	case !produced:
		return "no command emits it"
	case !consumed:
		return "no projection handles it"
	}
	return ""
}

// addHandlers creates one node per handler method that reaches a service, and
// links it to the command it calls. A handler still carrying the generated
// TODO body has no methods, so it gets a single warned placeholder node.
func (b *flowBuilder) addHandlers() {
	commands := map[string]map[string]string{} // aggregate → method → command node ID
	for _, s := range b.model.Service {
		commands[s.Aggregate] = map[string]string{}
		for _, c := range s.Commands {
			commands[s.Aggregate][c.Name] = commandID(s.Name, c.Name)
		}
	}

	for _, h := range b.model.Handler {
		if len(h.Methods) == 0 {
			b.add(FlowNode{
				ID:        handlerID(h.Name, ""),
				Kind:      FlowHandler,
				Label:     h.Name,
				Sub:       h.Aggregate,
				Aggregate: h.Aggregate,
				Warn:      "no service call yet",
			})
			continue
		}
		for _, method := range h.Methods {
			id := handlerID(h.Name, method.Name)
			warn := ""
			linked := false
			for _, call := range method.Calls {
				target, ok := commands[h.Aggregate][call]
				if !ok {
					continue
				}
				b.link(id, target)
				linked = true
			}
			if !linked {
				warn = "calls no known command"
			}
			b.add(FlowNode{
				ID:        id,
				Kind:      FlowHandler,
				Label:     h.Name + "." + method.Name,
				Sub:       h.Aggregate,
				Aggregate: h.Aggregate,
				Warn:      warn,
			})
		}
	}
}

// addProjections creates one node per worker and links every event it handles
// into it. A worker that subscribes but has no case yet is warned rather than
// given speculative edges.
func (b *flowBuilder) addProjections() {
	for _, p := range b.model.Projection {
		id := projectionID(p.Name)
		sub := strings.Join(p.Aggregates, ", ")
		if p.Multi {
			sub = "multi · " + sub
		}
		warn := ""
		if len(p.Events) == 0 {
			warn = "subscribes but handles no event yet"
		}
		aggregate := ""
		if len(p.Aggregates) == 1 {
			aggregate = p.Aggregates[0]
		}
		b.add(FlowNode{
			ID:        id,
			Kind:      FlowProjection,
			Label:     p.Name,
			Sub:       sub,
			Aggregate: aggregate,
			Warn:      warn,
		})
		for _, e := range p.Events {
			b.link(eventID(b.aggregateOfEvent(p, e), e), id)
		}
	}
}

// addQueries creates one node per query and attaches it to the workers that
// feed its aggregate. That last hop is a naming inference, not a parsed call,
// so the edges are marked Inferred.
func (b *flowBuilder) addQueries() {
	for _, q := range b.model.Query {
		id := queryID(q.Name)
		warn := ""
		linked := false
		for _, p := range b.model.Projection {
			for _, agg := range p.Aggregates {
				if agg == q.Aggregate && q.Aggregate != "" {
					b.guess(projectionID(p.Name), id)
					linked = true
					break
				}
			}
		}
		if !linked {
			warn = "no projection feeds it"
		}
		b.add(FlowNode{
			ID:        id,
			Kind:      FlowQuery,
			Label:     q.Name,
			Sub:       q.Aggregate,
			Aggregate: q.Aggregate,
			Warn:      warn,
		})
	}
}

// flowLayers is the fixed column order and titles of the rendered graph.
var flowLayers = []struct {
	Kind  FlowKind
	Title string
}{
	{FlowHandler, "HTTP handler"},
	{FlowCommand, "Service command"},
	{FlowEvent, "Event"},
	{FlowProjection, "Projection"},
	{FlowQuery, "Query"},
}

// graph materialises the ordered columns and drops edges whose endpoints were
// filtered out, so the caller never renders a dangling line.
func (b *flowBuilder) graph(aggregate string) FlowGraph {
	var g FlowGraph
	keep := map[string]bool{}
	for _, layer := range flowLayers {
		nodes := b.nodes[layer.Kind]
		if aggregate != "" {
			filtered := nodes[:0:0]
			for _, n := range nodes {
				if n.Aggregate == aggregate {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].Aggregate != nodes[j].Aggregate {
				return nodes[i].Aggregate < nodes[j].Aggregate
			}
			return nodes[i].Label < nodes[j].Label
		})
		for _, n := range nodes {
			keep[n.ID] = true
		}
		g.Columns = append(g.Columns, FlowColumn{Kind: layer.Kind, Title: layer.Title, Nodes: nodes})
	}

	for _, e := range b.edges {
		if keep[e.From] && keep[e.To] {
			g.Edges = append(g.Edges, e)
		}
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g
}

// EventFlow is one declared event with the commands that emit it and the
// workers that handle it — the row shape `esb show` prints, and the same
// producer/consumer facts the graph draws as edges.
type EventFlow struct {
	Aggregate string
	Event     string
	Producers []string // "<service>.<Command>", or "(runtime)" for a dynamic emitter
	Consumers []string // projection worker names
	Warn      string   // same dead-end text the graph puts on the event node
}

// BuildEventFlows lists every declared event with its producers and consumers,
// ordered by aggregate then event name.
func BuildEventFlows(m ProjectModel) []EventFlow {
	b := newFlowBuilder(m)
	var out []EventFlow
	for _, a := range m.Aggregate {
		for _, name := range a.Events {
			flow := EventFlow{
				Aggregate: a.Name,
				Event:     name,
				Warn:      b.eventWarn(a.Name, name),
			}
			for _, s := range m.Service {
				if s.Aggregate != a.Name {
					continue
				}
				for _, c := range s.Commands {
					if c.Dynamic && len(c.Emits) == 0 {
						flow.Producers = append(flow.Producers, "(runtime)")
						continue
					}
					for _, e := range c.Emits {
						if e == name {
							flow.Producers = append(flow.Producers, s.Name+"."+c.Name)
						}
					}
				}
			}
			for _, p := range m.Projection {
				for _, e := range p.Events {
					if e == name && b.aggregateOfEvent(p, e) == a.Name {
						flow.Consumers = append(flow.Consumers, p.Name)
					}
				}
			}
			sort.Strings(flow.Producers)
			sort.Strings(flow.Consumers)
			out = append(out, flow)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Aggregate != out[j].Aggregate {
			return out[i].Aggregate < out[j].Aggregate
		}
		return out[i].Event < out[j].Event
	})
	return out
}

// BuildStats derives the counters and the gap list shown alongside the graph.
// It reuses BuildFlow's node warnings so the two views can never disagree
// about what is broken.
func BuildStats(m ProjectModel) Stats {
	s := Stats{
		Aggregates:  len(m.Aggregate),
		Services:    len(m.Service),
		Handlers:    len(m.Handler),
		Projections: len(m.Projection),
		Queries:     len(m.Query),
	}
	for _, a := range m.Aggregate {
		s.Events += len(a.EventDetails)
		for _, d := range a.EventDetails {
			s.EventFields += len(d.Fields)
		}
	}
	if s.Events > 0 {
		s.AvgFieldsPerEvent = float64(s.EventFields) / float64(s.Events)
	}
	for _, svc := range m.Service {
		s.Commands += len(svc.Commands)
		for _, c := range svc.Commands {
			if c.Dynamic {
				s.DynamicCommands++
			}
		}
	}
	for _, h := range m.Handler {
		s.HandlerMethods += len(h.Methods)
	}
	for _, p := range m.Projection {
		if p.Multi {
			s.MultiProjections++
		}
	}

	b := newFlowBuilder(m)
	for _, a := range m.Aggregate {
		for _, e := range a.Events {
			produced := b.emitted[a.Name][e] || b.dynamic[a.Name]
			if !produced {
				s.UnproducedEvents++
			}
			if !b.consumed[a.Name][e] {
				s.UnconsumedEvents++
			}
		}
	}
	s.Gaps = buildGaps(m, b)
	return s
}

// buildGaps walks the same derived sets the graph uses and reports each dead
// end once, ordered so the list is stable between runs.
func buildGaps(m ProjectModel, b *flowBuilder) []Gap {
	var gaps []Gap
	warn := func(subject, msg string) { gaps = append(gaps, Gap{Severity: "warn", Subject: subject, Message: msg}) }
	info := func(subject, msg string) { gaps = append(gaps, Gap{Severity: "info", Subject: subject, Message: msg}) }

	handlerFor := map[string]bool{}
	for _, h := range m.Handler {
		handlerFor[h.Aggregate] = true
		if len(h.Methods) == 0 {
			info("handler "+h.Name, "belum memanggil service — masih body TODO hasil generate")
		}
	}
	projectionFor := map[string]bool{}
	for _, p := range m.Projection {
		for _, a := range p.Aggregates {
			projectionFor[a] = true
		}
		if len(p.Events) == 0 {
			warn("projection "+p.Name, "subscribe ke "+strings.Join(p.Aggregates, ", ")+" tapi belum handle event apa pun")
		}
	}
	commandFor := map[string]bool{}
	for _, s := range m.Service {
		if len(s.Commands) > 0 {
			commandFor[s.Aggregate] = true
			continue
		}
		info("service "+s.Name, "belum punya command yang menyimpan event")
	}

	for _, a := range m.Aggregate {
		if !handlerFor[a.Name] {
			info("aggregate "+a.Name, "belum punya handler")
		}
		if !commandFor[a.Name] {
			info("aggregate "+a.Name, "belum punya service command")
		}
		if !projectionFor[a.Name] {
			info("aggregate "+a.Name, "belum punya projection")
		}
		for _, e := range a.Events {
			if msg := b.eventWarn(a.Name, e); msg != "" {
				warn("event "+a.Name+"/"+e, msg)
			}
		}
	}

	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Severity != gaps[j].Severity {
			return gaps[i].Severity < gaps[j].Severity // "info" before "warn"
		}
		return gaps[i].Subject < gaps[j].Subject
	})
	return gaps
}
