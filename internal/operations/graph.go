package operations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/graph"
	"hclaro/internal/plan"
	"hclaro/internal/workspace"
)

// Graph statuses.
const (
	GraphRunning   = "running"
	GraphCompleted = "completed"
	GraphFailed    = "failed"
	GraphCancelled = "cancelled"
)

var (
	ErrGraphNotFound = errors.New("graph not found")
	ErrGraphStale    = errors.New("graph no longer matches the current context")
	ErrNodeNotFound  = errors.New("graph node not found")
	ErrPlanNotFound  = errors.New("plan not found")
)

// GraphStart is a validated graph request; see PlanStart for the profile fields.
type GraphStart struct {
	ProfileID          string
	ContextFingerprint string
}

// GraphRun is the redacted record of one `graph` invocation for the launch workspace. Only
// the parsed model is kept (no DOT text, labels or attributes), in memory, at most maxGraphs
// for at most GraphMaxAge.
type GraphRun struct {
	ID            string            `json:"id"`
	OperationID   string            `json:"operationId"`
	Status        string            `json:"status"`
	StartedAt     string            `json:"startedAt"`
	CompletedAt   string            `json:"completedAt,omitempty"`
	Engine        string            `json:"engine"`
	EngineVersion string            `json:"engineVersion"`
	Workspace     string            `json:"workspace"`
	Context       *ExecutionContext `json:"executionContext,omitempty"`
	// Format is "resources" or "full" once parsed; see graph.Graph.
	Format      string        `json:"format,omitempty"`
	Summary     graph.Summary `json:"summary"`
	Warnings    []string      `json:"warnings"`
	Diagnostic  *Diagnostic   `json:"diagnostic,omitempty"`
	fingerprint string
	graph       *graph.Graph
	started     time.Time
}

// GraphView is a graph as returned to a client: freshness against the current launch
// context, nodes annotated against the current state snapshot and, when a plan is chosen,
// that plan's confirmed changes. A stale graph has its nodes and edges withheld.
type GraphView struct {
	GraphRun
	Current       bool          `json:"current"`
	StaleReason   string        `json:"staleReason,omitempty"`
	Withheld      bool          `json:"withheld"`
	Nodes         []graph.Node  `json:"nodes"`
	Edges         []graph.Edge  `json:"edges"`
	ResourceEdges []graph.Edge  `json:"resourceEdges"`
	Plan          *GraphPlanRef `json:"plan,omitempty"`
}

// GraphPlanRef identifies the plan whose changes are overlaid. Unmapped lists confirmed
// changes that match no graph node by address grammar.
type GraphPlanRef struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	CreatedAt string                `json:"createdAt"`
	Replace   []string              `json:"replace"`
	Changes   int                   `json:"changes"`
	Unmapped  []graph.PlannedChange `json:"unmapped"`
}

// GraphImpact is one node's neighbourhood and, with a plan, how the plan's confirmed changes
// relate to it. InImpact are changes at the node or at nodes that depend on it; Elsewhere
// are the plan's other changes. Neither list says that one change causes another.
type GraphImpact struct {
	graph.Impact
	Plan *ImpactPlan `json:"plan,omitempty"`
}

type ImpactPlan struct {
	ID        string                `json:"id"`
	InImpact  []graph.PlannedChange `json:"inImpact"`
	Elsewhere []graph.PlannedChange `json:"elsewhere"`
	Unmapped  []graph.PlannedChange `json:"unmapped"`
}

const maxGraphs = 5

// GraphMaxAge bounds how long a graph is kept.
const GraphMaxAge = 12 * time.Hour

var graphMessages = map[string]string{
	"credentials":              "The engine could not read the backend (for example, an expired SSO session or a missing permission). Sign in again in your terminal, then generate the graph again.",
	"init_required":            "This working directory needs initialization, or modules or providers are missing. Run init yourself, then read the project again. Nothing was initialized automatically.",
	"network":                  "The backend was unreachable. Check VPN and network access, then generate the graph again.",
	"state_locked":             "The state is locked by another operation, so the engine could not read it. Hclaro never force-unlocks state.",
	"graph_failed":             "The engine could not produce a graph. Run `graph` with your CLI to see the error; its output is not shown here because it may contain secrets.",
	"graph_format":             "The graph output could not be read as the DOT format the engines produce. Nothing was kept.",
	"graph_too_large":          "The graph exceeds Hclaro's size limits (16 MiB, 20,000 nodes or 200,000 edges). Nothing was kept.",
	"graph_unavailable":        "This engine adapter cannot generate graphs.",
	"timeout":                  "Generating the graph exceeded its time limit and was stopped.",
	"cancelled":                "Generating the graph was cancelled. Nothing was kept.",
	"unsafe_cli_args":          "Inherited TF_CLI_ARGS or TF_CLI_ARGS_graph contains flags other than -no-color. Injected flags could change what the graph shows, so Hclaro refused to run. Values were not logged.",
	"workspace_error":          "The active workspace could not be confirmed before generating the graph. Check workspace selection with your CLI.",
	"workspace_changed":        "The active workspace changed since Hclaro read this project. Read the project again first.",
	"shutdown":                 "Hclaro stopped while the graph was being generated. Nothing was kept.",
	"scope_changed":            scopeChangedMessage,
	"configuration_unreadable": "Hclaro could not read the configuration, so a graph could not be tied to it. Nothing was run. The log names the file or limit; fix it, then generate the graph again.",
}

func graphMessage(code string) string {
	if message := graphMessages[code]; message != "" {
		return message
	}
	return graphMessages["graph_failed"]
}

// GraphCapability reports whether a graph can be generated for the launch snapshot and, if
// not, why. It runs no command.
func (m *Manager) GraphCapability() (bool, string) {
	return m.graphCapability(m.store.Get())
}

func (m *Manager) graphCapability(snap workspace.Snapshot) (bool, string) {
	info := snap.Info
	switch {
	case m.planner == nil:
		return false, "Dependency graphs are generated only for a project opened from the terminal."
	case snap.Status == "loading":
		return false, "Hclaro is still reading the workspace."
	case info.ExecutionMode == "remote-exec":
		return false, "This workspace runs operations remotely. Hclaro has not verified `graph` with remote execution, so graphs are unsupported here. Use your CLI."
	case !info.Capabilities.CanPlan:
		reason := info.Capabilities.PlanReason
		if reason == "" {
			reason = "Planning is not available for this workspace."
		}
		return false, "Generating a graph needs the same initialized working directory as planning. " + reason
	case !engine.GraphSupport(info.Engine, info.Version):
		return false, "This engine version is not in the range Hclaro has verified for `graph`."
	}
	return true, ""
}

// StartGraph runs `graph` for the launch workspace in the background, in the same
// one-at-a-time slot and root claim as plans. It never plans, refreshes or applies.
func (m *Manager) StartGraph(req GraphStart) (Operation, GraphRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Operation{}, GraphRun{}, ErrClosed
	}
	if m.running != nil {
		return Operation{}, GraphRun{}, ErrBusy
	}
	snap := m.store.Get()
	if snap.Status == "loading" {
		return Operation{}, GraphRun{}, ErrLoading
	}
	if ok, reason := m.graphCapability(snap); !ok {
		return Operation{}, GraphRun{}, reject("graph_unsupported", reason)
	}
	bound, environ, err := m.bind(snap, req.ProfileID, req.ContextFingerprint)
	if err != nil {
		return Operation{}, GraphRun{}, err
	}
	release, ok := m.Locks.Acquire(m.root, "graph")
	if !ok {
		return Operation{}, GraphRun{}, ErrBusy
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(m.base, m.Timeout)
	command := append([]string{snap.Info.Engine}, engine.GraphArgs()...)
	op := Operation{ID: m.NewID(), Kind: "graph", Status: Running, Stage: "Starting", StartedAt: now.UTC().Format(time.RFC3339), Command: command, VarFiles: []string{}, Replace: []string{}, Diagnostics: []EngineDiagnostic{}, LogRetained: true, Context: bound}
	run := &GraphRun{
		ID: m.NewID(), OperationID: op.ID, Status: GraphRunning, StartedAt: op.StartedAt, Engine: snap.Info.Engine,
		EngineVersion: snap.Info.Version, Workspace: snap.Info.Workspace, Context: bound.Clone(), Warnings: []string{}, started: now,
	}
	op.GraphID = run.ID
	e := &entry{op: op, log: NewLog(logEvents, logBytes), cancel: cancel, start: now, release: release, environ: environ, meta: recordMeta{m.root, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace}}
	m.entries = append(m.entries, e)
	m.trim()
	m.running = e
	m.recordLocked(e)
	m.graphs = append(m.graphs, run)
	if len(m.graphs) > maxGraphs {
		m.graphs = m.graphs[len(m.graphs)-maxGraphs:]
	}
	m.wg.Add(1)
	go m.runGraph(ctx, e, snap, run)
	return m.snapshot(e), run.clone(), nil
}

func (m *Manager) runGraph(ctx context.Context, e *entry, snap workspace.Snapshot, run *GraphRun) {
	defer m.wg.Done()
	defer e.cancel()
	g, fingerprint, code, exitCode := m.executeGraph(ctx, e, snap)
	m.mu.Lock()
	o := &e.op
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	o.ExitCode = exitCode
	o.Stage = "Finished"
	run.CompletedAt = o.CompletedAt
	run.fingerprint = fingerprint
	switch {
	case g != nil:
		o.Status, run.Status = Succeeded, GraphCompleted
		run.graph, run.Format, run.Summary = g, g.Format, g.Summary
		run.Warnings = append(run.Warnings, g.Warnings...)
	case code == "cancelled" || code == "shutdown":
		o.Status, run.Status = Cancelled, GraphCancelled
	default:
		o.Status, run.Status = Failed, GraphFailed
	}
	if code != "" {
		d := &Diagnostic{Code: code, Message: graphMessage(code)}
		o.Diagnostic, run.Diagnostic = d, d
	}
	o.LogDropped = e.log.Dropped()
	m.running = nil
	e.release()
	summary := run.Summary
	m.mu.Unlock()
	if g != nil {
		e.log.Append("info", fmt.Sprintf("Graph read: %d resources, %d data sources, %d internal nodes, %d links.", summary.Resources, summary.DataSources, summary.Internal, summary.Edges))
	} else {
		e.log.Append("error", graphMessage(code))
	}
	e.log.Append("info", "Only a redacted summary of the graph was kept. No plan, refresh or apply was run.")
	m.recordFinal(e, nil, nil)
	e.log.Finish()
}

func (m *Manager) executeGraph(ctx context.Context, e *entry, snap workspace.Snapshot) (g *graph.Graph, fingerprint, code string, exitCode *int) {
	stopped := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case e.op.CancelRequested:
			return "cancelled"
		case m.closed:
			return "shutdown"
		}
		return "timeout"
	}
	m.stage(e, "Checking workspace")
	bound := e.op.Context
	planner := m.bound(e.environ, snap.Info.Workspace)
	if bound != nil {
		e.log.Append("info", "Execution profile "+bound.ProfileName+" (revision "+fmt.Sprint(bound.Revision)+"), workspace "+bound.Workspace+". Identity not verified.")
	}
	grapher, ok := planner.(engine.Grapher)
	if !ok {
		return nil, "", "graph_unavailable", nil
	}
	if ok, changed := m.verify(ctx, e, "graph"); !ok {
		if !changed {
			return nil, "", stopped(), nil
		}
		return nil, "", "scope_changed", nil
	}
	name, err := planner.Workspace(ctx)
	switch {
	case ctx.Err() != nil:
		return nil, "", stopped(), nil
	case errors.Is(err, engine.ErrUnsafeArgs):
		return nil, "", "unsafe_cli_args", nil
	case err != nil:
		return nil, "", "workspace_error", nil
	case name != snap.Info.Workspace:
		return nil, "", "workspace_changed", nil
	}
	// Taken before the engine runs, so edits made meanwhile make the result stale.
	fingerprint, err = m.fingerprintFor(snap, nil, "", bound, e.environ)
	if err != nil {
		e.log.Append("error", unreadableNote(err))
		return nil, "", "configuration_unreadable", nil
	}
	m.stage(e, "Generating graph")
	dot, err := grapher.Graph(ctx)
	if err != nil {
		var exit *engine.ExitError
		switch {
		case ctx.Err() != nil || errors.Is(err, engine.ErrCancelled):
			return nil, fingerprint, stopped(), nil
		case errors.Is(err, engine.ErrUnsafeArgs):
			return nil, fingerprint, "unsafe_cli_args", nil
		case errors.Is(err, engine.ErrGraphTooLarge):
			zero := 0
			return nil, fingerprint, "graph_too_large", &zero
		case errors.As(err, &exit):
			n := exit.Code
			code := exit.Hint
			if code == "" || code == "stale_plan" {
				code = "graph_failed"
			}
			return nil, fingerprint, code, &n
		}
		return nil, fingerprint, "graph_failed", nil
	}
	zero := 0
	m.stage(e, "Reading graph")
	parsed, err := graph.Parse(dot)
	switch {
	case errors.Is(err, graph.ErrTooLarge):
		return nil, fingerprint, "graph_too_large", &zero
	case err != nil:
		return nil, fingerprint, "graph_format", &zero
	}
	return parsed, fingerprint, "", &zero
}

func (r *GraphRun) clone() GraphRun {
	out := *r
	out.Context = r.Context.Clone()
	out.Warnings = slices.Clone(r.Warnings)
	if r.Diagnostic != nil {
		d := *r.Diagnostic
		out.Diagnostic = &d
	}
	return out
}

func (m *Manager) expireGraphsLocked(now time.Time) {
	m.graphs = slices.DeleteFunc(m.graphs, func(g *GraphRun) bool {
		return g.Status != GraphRunning && now.Sub(g.started) > GraphMaxAge
	})
}

// Graphs lists retained graphs, newest first, without nodes or edges.
func (m *Manager) Graphs() []GraphView {
	m.mu.Lock()
	m.expireGraphsLocked(time.Now())
	runs := make([]GraphRun, 0, len(m.graphs))
	for i := len(m.graphs) - 1; i >= 0; i-- {
		runs = append(runs, m.graphs[i].clone())
	}
	m.mu.Unlock()
	out := make([]GraphView, 0, len(runs))
	for _, r := range runs {
		v := GraphView{GraphRun: r, Nodes: []graph.Node{}, Edges: []graph.Edge{}, ResourceEdges: []graph.Edge{}}
		v.StaleReason = m.graphStaleReason(r)
		v.Current = v.StaleReason == ""
		out = append(out, v)
	}
	return out
}

func (m *Manager) findGraph(id string) (GraphRun, *graph.Graph, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireGraphsLocked(time.Now())
	for _, r := range m.graphs {
		if r.ID == id {
			// The parsed graph is immutable once stored, so it is shared, not copied.
			return r.clone(), r.graph, true
		}
	}
	return GraphRun{}, nil, false
}

// graphStaleReason checks a graph against the current launch context: same workspace, engine
// and version, and an unchanged context fingerprint (configuration, data directory,
// environment and execution profile). A running graph is current.
func (m *Manager) graphStaleReason(r GraphRun) string {
	if r.Status == GraphRunning {
		return ""
	}
	snap := m.store.Get()
	switch {
	case snap.Status == "loading":
		return "The project is being read again. Generate the graph again once it is ready."
	case snap.Info.Workspace != r.Workspace:
		return fmt.Sprintf("This graph was generated for workspace %s; the project now reads workspace %s.", r.Workspace, snap.Info.Workspace)
	case snap.Info.Engine != r.Engine || snap.Info.Version != r.EngineVersion:
		return "The engine or its version changed since this graph was generated."
	case r.fingerprint == "":
		return "This graph has no recorded context."
	}
	current, rej := m.contextFingerprint(nil, r.Context, "graph", "Generate it again with the current profile.")
	switch {
	case rej != nil:
		return rej.Reason
	case current != r.fingerprint:
		return "Configuration, the data directory or the environment changed since this graph was generated. Generate it again to see current dependencies."
	}
	return ""
}

// Graph returns one graph annotated against the current snapshot. planID, when set, overlays
// that plan's confirmed changes; the plan must be full and current (see graphPlan).
func (m *Manager) Graph(id, planID string) (GraphView, error) {
	r, g, ok := m.findGraph(id)
	if !ok {
		return GraphView{}, ErrGraphNotFound
	}
	v := GraphView{GraphRun: r, Nodes: []graph.Node{}, Edges: []graph.Edge{}, ResourceEdges: []graph.Edge{}}
	v.StaleReason = m.graphStaleReason(r)
	v.Current = v.StaleReason == ""
	if !v.Current || g == nil {
		v.Withheld = !v.Current && g != nil
		if planID != "" && !v.Current {
			return v, ErrGraphStale
		}
		return v, nil
	}
	v.Nodes = g.Annotate(m.store.Get().Index)
	v.Edges, v.ResourceEdges = g.Edges, g.ResourceEdges
	if planID != "" {
		p, changes, err := m.graphPlan(planID, r)
		if err != nil {
			return GraphView{}, err
		}
		unmapped := g.Overlay(v.Nodes, changes)
		v.Plan = &GraphPlanRef{ID: p.ID, Kind: p.Kind, CreatedAt: p.CreatedAt, Replace: slices.Clone(p.Replace), Changes: len(changes), Unmapped: nonNil(unmapped)}
	}
	return v, nil
}

// GraphImpact analyzes one node of a current graph. With planID, the plan's confirmed changes
// are split into those at the node or its dependents and the rest.
func (m *Manager) GraphImpact(id, nodeID, planID string) (GraphImpact, error) {
	r, g, ok := m.findGraph(id)
	if !ok || g == nil {
		return GraphImpact{}, ErrGraphNotFound
	}
	if m.graphStaleReason(r) != "" {
		return GraphImpact{}, ErrGraphStale
	}
	i := g.NodeIndex(nodeID)
	if i < 0 {
		return GraphImpact{}, ErrNodeNotFound
	}
	out := GraphImpact{Impact: g.Analyze(i)}
	if planID == "" {
		return out, nil
	}
	_, changes, err := m.graphPlan(planID, r)
	if err != nil {
		return GraphImpact{}, err
	}
	nodes := g.Annotate(m.store.Get().Index)
	unmapped := g.Overlay(nodes, changes)
	reach := g.Reachable(i)
	ip := &ImpactPlan{ID: planID, InImpact: []graph.PlannedChange{}, Elsewhere: []graph.PlannedChange{}, Unmapped: nonNil(unmapped)}
	for j, n := range nodes {
		for _, c := range n.Planned {
			if j == i || reach[j] {
				ip.InImpact = append(ip.InImpact, c)
			} else {
				ip.Elsewhere = append(ip.Elsewhere, c)
			}
		}
	}
	out.Plan = ip
	return out, nil
}

func nonNil(list []graph.PlannedChange) []graph.PlannedChange {
	if list == nil {
		return []graph.PlannedChange{}
	}
	return list
}

// graphPlan returns a plan's confirmed changes when the plan can be trusted as the full,
// current plan for the graph's context: same workspace and engine, not errored, incomplete
// or deferred, and its context unchanged. No-op changes are not changes.
func (m *Manager) graphPlan(planID string, r GraphRun) (*Plan, []graph.PlannedChange, error) {
	p, ok := m.Plan(planID)
	if !ok {
		return nil, nil, ErrPlanNotFound
	}
	switch changed, _ := m.ContextStatus(p); {
	case p.Workspace != r.Workspace || p.Engine != r.Engine:
		return nil, nil, reject("plan_not_current", "This plan was created for another workspace or engine than the graph.")
	case p.Errored || p.Incomplete || p.Deferred > 0:
		return nil, nil, reject("plan_not_current", "This plan is errored or incomplete, so it does not show every planned change. Run a complete plan first.")
	case changed:
		return nil, nil, reject("plan_not_current", "Configuration, variable files or the context changed since this plan was created, so it may not show current planned changes. Run a new plan.")
	}
	changes := []graph.PlannedChange{}
	for _, c := range p.Changes {
		if c.Action == plan.NoOp {
			continue
		}
		changes = append(changes, graph.PlannedChange{ChangeID: c.ID, Address: c.Address, Mode: c.Mode, Action: c.Action})
	}
	return p, changes, nil
}
