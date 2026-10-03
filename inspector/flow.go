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
	FlowStore      FlowKind = "store" // one node per aggregate stream
)

// FlowNode is one box in the graph.
type FlowNode struct {
	ID        string // stable and unique, e.g. "event:product/ProductCreated"
	Kind      FlowKind
	Label     string    // what the box shows
	Sub       string    // second line: owning aggregate, or a qualifier
	Aggregate string    // "" when the node belongs to no single aggregate
	Warn      string    // non-empty marks a dead end and is shown on the node
	Source    SourceRef // set by AttachSources; zero when the file is not found
}

// FlowEdge connects two node IDs. Inferred edges are drawn dashed because
// they are derived from a naming convention rather than a call expression.
type FlowEdge struct {
	From     string
	To       string
	Inferred bool
	Op       string // edges into the event store: "read" or "write"
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
	Node     string // flow node ID the gap is about, "" when it has none
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
	return BuildFlowWith(m, FlowOptions{Aggregates: []string{aggregate}})
}

// FlowOptions narrows the graph BuildFlowWith returns.
type FlowOptions struct {
	// Aggregates keeps the nodes of these aggregates plus the cross-aggregate
	// neighbours BuildFlow keeps for one (empty strings are ignored; none
	// means the whole project).
	Aggregates []string
	// Problems keeps only warned nodes and their direct neighbours.
	Problems bool
	// HideStubs drops handlers that call no known command yet.
	HideStubs bool
}

// BuildFlowWith is BuildFlow with several aggregates and the UI's extra
// filters. Filters only narrow what is drawn; warnings are still computed
// against the whole project.
func BuildFlowWith(m ProjectModel, opts FlowOptions) FlowGraph {
	b := newFlowBuilder(m)
	b.addCommands()
	b.addEvents()
	b.addStore()
	b.addHandlers()
	b.addProjections()
	b.addQueries()
	return b.graph(opts)
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

	// methods maps "<Type>.<Method>" → node ID for every service method node
	// (commands and store-reading methods a handler calls).
	methods map[string]string
	// methodAggregate maps a method node ID → its aggregate.
	methodAggregate map[string]string
	// emitsInto maps a method node ID → aggregates it emits a known event into.
	emitsInto map[string]map[string]bool
}

func newFlowBuilder(m ProjectModel) *flowBuilder {
	b := &flowBuilder{
		model:    m,
		nodes:    map[FlowKind][]FlowNode{},
		declared: map[string]map[string]bool{},
		emitted:  map[string]map[string]bool{},
		consumed: map[string]map[string]bool{},
		dynamic:  map[string]bool{},

		methods:         map[string]string{},
		methodAggregate: map[string]string{},
		emitsInto:       map[string]map[string]bool{},
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
			for _, r := range c.Other {
				b.mark(b.emitted, r.Aggregate, r.Event)
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
//
// It also adds a node for every exported service method a handler calls that
// reads the event store without emitting (CycleResolver.Cycle): that is the
// handler reading the write model directly, which addStore draws.
func (b *flowBuilder) addCommands() {
	serviceAggregate := map[string]string{}
	for _, s := range b.model.Service {
		serviceAggregate[s.Name] = s.Aggregate
		for _, c := range s.Commands {
			id := commandID(s.Name, c.Name)
			b.methods[s.Struct+"."+c.Name] = id
			b.methodAggregate[id] = s.Aggregate
			into := map[string]bool{}
			if len(c.Emits) > 0 {
				into[s.Aggregate] = true
			}
			for _, r := range c.Other {
				into[r.Aggregate] = true
			}
			b.emitsInto[id] = into
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
			for _, r := range c.Other {
				b.link(id, eventID(r.Aggregate, r.Event))
			}
		}
	}

	called := map[string]bool{}
	for _, h := range b.model.Handler {
		for _, hm := range h.Methods {
			for _, c := range hm.Calls {
				called[c] = true
			}
		}
	}
	for _, a := range b.model.StoreAccess {
		key := a.Struct + "." + a.Method
		if b.methods[key] != "" || !called[key] {
			continue
		}
		id := commandID(a.Service, a.Method)
		agg := serviceAggregate[a.Service]
		if agg == "" {
			agg = soleOf(union(a.Reads, a.Writes))
		}
		b.methods[key] = id
		b.methodAggregate[id] = agg
		b.add(FlowNode{
			ID:        id,
			Kind:      FlowCommand,
			Label:     a.Method,
			Sub:       a.Service,
			Aggregate: agg,
		})
	}
}

// addStore adds one event store node per aggregate and the edges into it:
//
//   - event → store (write): every event is appended to its aggregate's stream.
//   - method → store (read): a method that loads an aggregate it does not also
//     write. A command's load of its own aggregate before storing is implied
//     by its write and left out, so the graph stays readable; what remains is
//     cross-aggregate reads and handlers reading the write model directly.
//   - method → store (write): a method that writes an aggregate without
//     emitting a known event of it itself — typically by calling another
//     service's exported command (RecordTransaction → envelopes.Spend), or a
//     dynamic emitter — so the cross-aggregate write is not lost.
func (b *flowBuilder) addStore() {
	used := map[string]bool{}
	for _, n := range b.nodes[FlowEvent] {
		b.edges = append(b.edges, FlowEdge{From: n.ID, To: storeID(n.Aggregate), Op: "write"})
		used[n.Aggregate] = true
	}
	for _, a := range b.model.StoreAccess {
		id := b.methods[a.Struct+"."+a.Method]
		if id == "" {
			continue
		}
		for _, agg := range a.Reads {
			if contains(a.Writes, agg) {
				continue
			}
			b.edges = append(b.edges, FlowEdge{From: id, To: storeID(agg), Op: "read"})
			used[agg] = true
		}
		for _, agg := range a.Writes {
			if b.emitsInto[id][agg] {
				continue
			}
			b.edges = append(b.edges, FlowEdge{From: id, To: storeID(agg), Op: "write"})
			used[agg] = true
		}
	}
	for agg := range used {
		b.add(FlowNode{
			ID:        storeID(agg),
			Kind:      FlowStore,
			Label:     agg,
			Sub:       "event store",
			Aggregate: agg,
		})
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
	commands, commandAggregate := b.methods, b.methodAggregate
	writes := map[string]bool{} // method node ID → it writes the store
	for id, into := range b.emitsInto {
		if len(into) > 0 {
			writes[id] = true
		}
	}
	for _, a := range b.model.StoreAccess {
		if id := b.methods[a.Struct+"."+a.Method]; id != "" && len(a.Writes) > 0 {
			writes[id] = true
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
			aggregate := h.Aggregate
			readOnly := true
			for _, call := range method.Calls {
				target, ok := commands[call]
				if !ok {
					continue
				}
				b.link(id, target)
				linked = true
				readOnly = readOnly && !writes[target]
				// A handler without the generated svc field (it holds a
				// resolver, say) takes the aggregate of what it calls.
				if aggregate == "" {
					aggregate = commandAggregate[target]
				}
			}
			sub := aggregate
			if !linked {
				warn = "calls no known command"
			} else if readOnly {
				// Only reaches the store to load aggregates: it reads the
				// write model instead of a projection.
				sub = "baca write model langsung"
			}
			b.add(FlowNode{
				ID:        id,
				Kind:      FlowHandler,
				Label:     h.Name + "." + method.Name,
				Sub:       sub,
				Aggregate: aggregate,
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
	{FlowCommand, "Service method"},
	{FlowEvent, "Event"},
	{FlowStore, "Event store"},
	{FlowProjection, "Projection"},
	{FlowQuery, "Query"},
}

// graph materialises the ordered columns and drops edges whose endpoints were
// filtered out, so the caller never renders a dangling line.
func (b *flowBuilder) graph(opts FlowOptions) FlowGraph {
	var g FlowGraph
	wanted := map[string]bool{}
	for _, a := range opts.Aggregates {
		if a != "" {
			wanted[a] = true
		}
	}
	filtering := len(wanted) > 0
	keep := map[string]bool{}
	if filtering {
		for _, nodes := range b.nodes {
			for _, n := range nodes {
				if wanted[n.Aggregate] {
					keep[n.ID] = true
				}
			}
		}
		// A command of another aggregate that stores one of this aggregate's
		// events (through a helper of this aggregate's service) is part of
		// this aggregate's story, so it stays in the filtered view.
		// Likewise downstream: a multi-aggregate projection (one read-model
		// worker for many aggregates) belongs to no single aggregate, but it
		// handles this aggregate's events, so it stays too.
		base := map[string]bool{}
		for id := range keep {
			base[id] = true
		}
		for _, e := range b.edges {
			if base[e.To] && strings.HasPrefix(e.From, "command:") {
				keep[e.From] = true
			}
			if base[e.From] && strings.HasPrefix(e.From, "event:") && strings.HasPrefix(e.To, "projection:") {
				keep[e.To] = true
			}
		}
		// Handlers of the methods kept above, so a cross-aggregate read or
		// write is shown with the endpoint that triggers it.
		for _, e := range b.edges {
			if strings.HasPrefix(e.From, "handler:") && keep[e.To] && !base[e.To] {
				keep[e.From] = true
			}
		}
	}
	if !filtering {
		for _, nodes := range b.nodes {
			for _, n := range nodes {
				keep[n.ID] = true
			}
		}
	}
	if opts.HideStubs {
		for _, n := range b.nodes[FlowHandler] {
			if n.Warn != "" {
				delete(keep, n.ID)
			}
		}
	}
	if opts.Problems {
		problem := map[string]bool{}
		for _, nodes := range b.nodes {
			for _, n := range nodes {
				if keep[n.ID] && n.Warn != "" {
					problem[n.ID] = true
				}
			}
		}
		near := map[string]bool{}
		for id := range problem {
			near[id] = true
		}
		for _, e := range b.edges {
			if problem[e.From] && keep[e.To] {
				near[e.To] = true
			}
			if problem[e.To] && keep[e.From] {
				near[e.From] = true
			}
		}
		keep = near
	}
	for _, layer := range flowLayers {
		var nodes []FlowNode
		for _, n := range b.nodes[layer.Kind] {
			if keep[n.ID] {
				nodes = append(nodes, n)
			}
		}
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].Aggregate != nodes[j].Aggregate {
				return nodes[i].Aggregate < nodes[j].Aggregate
			}
			return nodes[i].Label < nodes[j].Label
		})
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
				for _, c := range s.Commands {
					for _, r := range c.Other {
						if r.Aggregate == a.Name && r.Event == name {
							flow.Producers = append(flow.Producers, s.Name+"."+c.Name)
						}
					}
					if s.Aggregate != a.Name {
						continue
					}
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
	warnAt := func(node, subject, msg string) {
		gaps = append(gaps, Gap{Severity: "warn", Subject: subject, Message: msg, Node: node})
	}
	infoAt := func(node, subject, msg string) {
		gaps = append(gaps, Gap{Severity: "info", Subject: subject, Message: msg, Node: node})
	}
	info := func(subject, msg string) { infoAt("", subject, msg) }

	handlerFor := map[string]bool{}
	for _, h := range m.Handler {
		handlerFor[h.Aggregate] = true
		if len(h.Methods) == 0 {
			infoAt(handlerID(h.Name, ""), "handler "+h.Name, "belum memanggil service — masih body TODO hasil generate")
		}
	}
	for _, u := range m.UnregisteredWorker {
		if u.Host != "" {
			info("projection "+u.Name, "worker standalone tidak dijalankan di main.go; event-nya dihitung lewat "+u.Host)
		} else {
			info("projection "+u.Name, "file worker ada di projection/ tapi tidak dijalankan di main.go — tidak dihitung")
		}
	}
	projectionFor := map[string]bool{}
	for _, p := range m.Projection {
		for _, a := range p.Aggregates {
			projectionFor[a] = true
		}
		if len(p.Events) == 0 {
			warnAt(projectionID(p.Name), "projection "+p.Name, "subscribe ke "+strings.Join(p.Aggregates, ", ")+" tapi belum handle event apa pun")
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
				warnAt(eventID(a.Name, e), "event "+a.Name+"/"+e, msg)
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
