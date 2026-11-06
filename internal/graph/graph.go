package graph

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"hclaro/internal/state"
)

// Edge confidences. A declared edge is in the engine output. A derived edge joins two
// resources through engine-internal nodes (variables, locals, outputs, module calls,
// providers): it follows declared edges only, but is not itself an engine edge.
const (
	Declared = "declared"
	Derived  = "derived"
)

// Node confidences, set when a graph is annotated against a state snapshot.
const (
	// Exact: a configuration resource whose address matches recorded instances by address
	// grammar (instance keys removed).
	Exact = "exact"
	// Configuration: a well-formed resource address with no recorded instance (not created
	// yet, count = 0, or a different workspace).
	Configuration = "configuration"
	// Internal: an engine node that is not a resource (variable, provider, module call…).
	Internal = "internal"
	// Unknown: the node name could not be read as an address; it is never mapped.
	Unknown = "unknown"
)

// Bounds for derived edges and analysis output.
const (
	maxDerivedSteps = 4_000_000
	MaxReach        = 2000
	maxInstances    = 25
)

// Node is one graph node. IDs are positional ("n12") and stable for one graph only.
type Node struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	// ResourceAddress is the configuration address of a resource or data node.
	ResourceAddress string `json:"resourceAddress,omitempty"`
	ModulePath      string `json:"modulePath"`
	Kind            string `json:"nodeKind"`
	Type            string `json:"type,omitempty"`
	// Confidence, Instances and InstanceCount are filled by Annotate.
	Confidence    string     `json:"confidence"`
	Provider      string     `json:"provider,omitempty"`
	Instances     []Instance `json:"instances,omitempty"`
	InstanceCount int        `json:"instanceCount"`
	InCycle       bool       `json:"inCycle,omitempty"`
	// Planned lists confirmed planned changes mapped onto this node; set only by Overlay.
	Planned []PlannedChange `json:"planned,omitempty"`
	// Redacted is set when the engine's node name looked like a secret and was withheld.
	Redacted bool `json:"redacted,omitempty"`
	parsed   parsed
}

// Instance is a recorded resource instance mapped to a node.
type Instance struct {
	ResourceID string `json:"resourceId"`
	Address    string `json:"address"`
}

type Edge struct {
	// From depends on To: To is created or read before From.
	From       string `json:"from"`
	To         string `json:"to"`
	Kind       string `json:"edgeKind"`
	Confidence string `json:"confidence"`
}

// Summary counts what was parsed and what was left out.
type Summary struct {
	Nodes        int `json:"nodes"`
	Edges        int `json:"edges"`
	Resources    int `json:"resources"`
	DataSources  int `json:"dataSources"`
	Modules      int `json:"modules"`
	Internal     int `json:"internal"`
	Unknown      int `json:"unknown"`
	Dropped      int `json:"dropped"`
	Redacted     int `json:"redacted"`
	Cycles       int `json:"cycles"`
	CycleNodes   int `json:"cycleNodes"`
	DerivedEdges int `json:"derivedEdges"`
	// DerivedTruncated is set when deriving resource links hit Hclaro's work bound; some
	// links through internal nodes may be missing from ResourceEdges (never from Edges).
	DerivedTruncated bool `json:"derivedTruncated"`
}

// Graph is a parsed, redacted dependency graph. It holds no raw DOT, labels or attributes.
type Graph struct {
	// Format is "full" (engine walk graph with internal nodes, Terraform < 1.7 and OpenTofu)
	// or "resources" (Terraform 1.7+ resource-only output).
	Format        string   `json:"format"`
	Nodes         []Node   `json:"nodes"`
	Edges         []Edge   `json:"edges"`
	ResourceEdges []Edge   `json:"resourceEdges"`
	Summary       Summary  `json:"summary"`
	Warnings      []string `json:"warnings"`
	out, in       [][]int
}

func isResource(kind string) bool { return kind == KindResource || kind == KindData }

func nodeID(i int) string { return "n" + strconv.Itoa(i) }

// NodeIndex returns the position of a node ID, or -1.
func (g *Graph) NodeIndex(id string) int {
	if !strings.HasPrefix(id, "n") || len(id) > 7 {
		return -1
	}
	i, err := strconv.Atoi(id[1:])
	if err != nil || i < 0 || i >= len(g.Nodes) || g.Nodes[i].ID != id {
		return -1
	}
	return i
}

func secretShaped(s string) bool {
	if state.Suspicious(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Parse reads `graph` output. It fails closed on anything that is not a directed DOT graph
// within the size bounds.
func Parse(dot []byte) (*Graph, error) {
	d, err := parseDOT(dot)
	if err != nil {
		return nil, err
	}
	if !d.Directed {
		return nil, fmt.Errorf("%w: expected a directed graph", ErrSyntax)
	}
	g := &Graph{Format: "resources", Nodes: []Node{}, Edges: []Edge{}, ResourceEdges: []Edge{}, Warnings: []string{}}
	keys := map[string]int{}
	position := make([]int, len(d.Nodes))
	odd := 0
	for i, n := range d.Nodes {
		if strings.HasPrefix(n.ID, "[root] ") {
			g.Format = "full"
		}
		name, drop, unusual := normalize(n.ID)
		p := classify(name)
		if drop || p.kind == kindMeta {
			position[i] = -1
			g.Summary.Dropped++
			continue
		}
		if unusual {
			odd++
			p = parsed{kind: KindUnknown, address: name}
		}
		if j, ok := keys[name]; ok {
			position[i] = j
			continue
		}
		node := Node{ID: nodeID(len(g.Nodes)), DisplayName: name, ModulePath: p.module, Kind: p.kind, Type: p.typ, parsed: p}
		if isResource(p.kind) {
			node.ResourceAddress = p.resource
		}
		if p.kind == KindProvider {
			node.Provider = name
		}
		if secretShaped(name) {
			node = Node{ID: node.ID, DisplayName: "[redacted node]", Kind: KindUnknown, Redacted: true, parsed: parsed{kind: KindUnknown}}
			g.Summary.Redacted++
		}
		keys[name] = len(g.Nodes)
		position[i] = len(g.Nodes)
		g.Nodes = append(g.Nodes, node)
	}
	g.out = make([][]int, len(g.Nodes))
	g.in = make([][]int, len(g.Nodes))
	seen := map[[2]int]bool{}
	for _, e := range d.Edges {
		a, b := position[d.index[e.From]], position[d.index[e.To]]
		if a < 0 || b < 0 || a == b || seen[[2]int{a, b}] {
			continue
		}
		seen[[2]int{a, b}] = true
		g.out[a] = append(g.out[a], b)
		g.in[b] = append(g.in[b], a)
		g.Edges = append(g.Edges, Edge{From: nodeID(a), To: nodeID(b), Kind: "depends_on", Confidence: Declared})
	}
	for _, n := range g.Nodes {
		switch {
		case n.Kind == KindResource:
			g.Summary.Resources++
		case n.Kind == KindData:
			g.Summary.DataSources++
		case n.Kind == KindModule:
			g.Summary.Modules++
			g.Summary.Internal++
		case n.Kind == KindUnknown:
			g.Summary.Unknown++
		default:
			g.Summary.Internal++
		}
	}
	g.Summary.Nodes, g.Summary.Edges = len(g.Nodes), len(g.Edges)
	g.markCycles()
	g.deriveResourceEdges()
	if g.Format == "full" {
		g.Warnings = append(g.Warnings, "This engine's graph includes internal nodes (variables, locals, outputs, module calls and providers). Links between resources through them are shown as derived.")
	}
	if g.Summary.Dropped > 0 {
		g.Warnings = append(g.Warnings, fmt.Sprintf("%d engine bookkeeping node%s (root, close, destroy and count-boundary nodes) %s left out.", g.Summary.Dropped, plural(g.Summary.Dropped), verb(g.Summary.Dropped)))
	}
	if odd > 0 {
		g.Warnings = append(g.Warnings, fmt.Sprintf("%d node%s had a form Hclaro does not recognize and %s shown as unknown, never mapped to state.", odd, plural(odd), verb(odd)))
	}
	if g.Summary.Redacted > 0 {
		g.Warnings = append(g.Warnings, fmt.Sprintf("%d node name%s looked like a secret and %s withheld.", g.Summary.Redacted, plural(g.Summary.Redacted), verb(g.Summary.Redacted)))
	}
	if g.Summary.Cycles > 0 {
		g.Warnings = append(g.Warnings, fmt.Sprintf("The graph has %d cycle%s. Nodes in a cycle depend on each other; the engine reports a cycle as an error when it plans.", g.Summary.Cycles, plural(g.Summary.Cycles)))
	}
	if g.Summary.DerivedTruncated {
		g.Warnings = append(g.Warnings, "The graph is too large to derive every link between resources through internal nodes. Some derived links are missing; declared links and reachability are complete.")
	}
	if len(g.Nodes) == 0 {
		g.Warnings = append(g.Warnings, "The graph has no resources. The configuration may declare none, or the engine produced an empty graph.")
	}
	return g, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func verb(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// markCycles finds strongly connected components with more than one node (Tarjan,
// iterative so deep graphs cannot exhaust the stack).
func (g *Graph) markCycles() {
	n := len(g.Nodes)
	index, low := make([]int, n), make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	counter := 0
	type frame struct{ v, next int }
	for root := 0; root < n; root++ {
		if index[root] >= 0 {
			continue
		}
		calls := []frame{{root, 0}}
		index[root], low[root] = counter, counter
		counter++
		stack = append(stack, root)
		onStack[root] = true
		for len(calls) > 0 {
			f := &calls[len(calls)-1]
			if f.next < len(g.out[f.v]) {
				w := g.out[f.v][f.next]
				f.next++
				if index[w] < 0 {
					index[w], low[w] = counter, counter
					counter++
					stack = append(stack, w)
					onStack[w] = true
					calls = append(calls, frame{w, 0})
				} else if onStack[w] {
					low[f.v] = min(low[f.v], index[w])
				}
				continue
			}
			v := f.v
			calls = calls[:len(calls)-1]
			if len(calls) > 0 {
				parent := calls[len(calls)-1].v
				low[parent] = min(low[parent], low[v])
			}
			if low[v] == index[v] {
				var component []int
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					component = append(component, w)
					if w == v {
						break
					}
				}
				if len(component) > 1 {
					g.Summary.Cycles++
					g.Summary.CycleNodes += len(component)
					for _, w := range component {
						g.Nodes[w].InCycle = true
					}
				}
			}
		}
	}
}

// deriveResourceEdges links each resource or data node to the nearest resource or data
// nodes it depends on, walking only declared edges and passing only through internal nodes.
func (g *Graph) deriveResourceEdges() {
	steps := 0
	mark := make([]int, len(g.Nodes))
	for i := range mark {
		mark[i] = -1
	}
	for src, n := range g.Nodes {
		if !isResource(n.Kind) {
			continue
		}
		direct := map[int]bool{}
		for _, w := range g.out[src] {
			direct[w] = true
		}
		mark[src] = src
		queue := []int{src}
		var targets []int
		for len(queue) > 0 {
			v := queue[0]
			queue = queue[1:]
			for _, w := range g.out[v] {
				steps++
				if steps > maxDerivedSteps {
					g.Summary.DerivedTruncated = true
					return
				}
				if mark[w] == src {
					continue
				}
				mark[w] = src
				if isResource(g.Nodes[w].Kind) {
					targets = append(targets, w)
					continue
				}
				queue = append(queue, w)
			}
		}
		slices.Sort(targets)
		for _, t := range targets {
			confidence := Declared
			if !direct[t] {
				confidence = Derived
				g.Summary.DerivedEdges++
			}
			g.ResourceEdges = append(g.ResourceEdges, Edge{From: nodeID(src), To: nodeID(t), Kind: "depends_on", Confidence: confidence})
		}
	}
}

// Annotate returns a copy of the nodes mapped against a state index. A resource node maps
// to recorded instances only when its address equals an instance address with every
// instance key removed (or equals it exactly, for an instanced node) and the mode matches.
func (g *Graph) Annotate(index state.Index) []Node {
	byConfig := map[string][]state.Resource{}
	for _, r := range index.Resources {
		key, ok := ConfigAddress(r.Address)
		if !ok {
			continue
		}
		byConfig[key] = append(byConfig[key], r)
	}
	out := make([]Node, len(g.Nodes))
	for i, n := range g.Nodes {
		n.Instances = nil
		switch {
		case n.Kind == KindUnknown:
			n.Confidence = Unknown
		case !isResource(n.Kind):
			n.Confidence = Internal
		default:
			mode := "managed"
			if n.Kind == KindData {
				mode = "data"
			}
			key := n.ResourceAddress
			if n.parsed.instanced {
				key, _ = ConfigAddress(n.ResourceAddress)
			}
			var matches []state.Resource
			for _, r := range byConfig[key] {
				if r.Mode == mode && (!n.parsed.instanced || r.Address == n.ResourceAddress) {
					matches = append(matches, r)
				}
			}
			n.Confidence = Configuration
			n.InstanceCount = len(matches)
			if len(matches) > 0 {
				n.Confidence = Exact
				n.Provider = matches[0].Provider
				for _, r := range matches[:min(len(matches), maxInstances)] {
					n.Instances = append(n.Instances, Instance{ResourceID: r.ID, Address: r.Address})
				}
			}
		}
		out[i] = n
	}
	return out
}

// Reach is a node reached from the selected node, with its distance in edges.
type Reach struct {
	Node     string `json:"node"`
	Distance int    `json:"distance"`
}

// Impact describes one node's neighbourhood. Upstream are the nodes it depends on (directly
// or transitively); Downstream are the nodes that depend on it, which is the potential impact
// of changing it. Neither says what a plan will do.
type Impact struct {
	Node               string   `json:"node"`
	DirectDependencies []string `json:"directDependencies"`
	DirectDependents   []string `json:"directDependents"`
	Upstream           []Reach  `json:"upstream"`
	Downstream         []Reach  `json:"downstream"`
	UpstreamTotal      int      `json:"upstreamTotal"`
	DownstreamTotal    int      `json:"downstreamTotal"`
	// DownstreamResources counts resource and data nodes that depend on the node: the
	// possible fan-out.
	DownstreamResources int  `json:"downstreamResources"`
	InCycle             bool `json:"inCycle"`
	// Truncated is set when Upstream or Downstream lists were capped at MaxReach entries.
	Truncated bool `json:"truncated"`
}

// Analyze computes the neighbourhood of node i. Traversal visits each node at most once, so
// it terminates on cycles.
func (g *Graph) Analyze(i int) Impact {
	im := Impact{Node: nodeID(i), DirectDependencies: ids(g.out[i]), DirectDependents: ids(g.in[i]), InCycle: g.Nodes[i].InCycle}
	var resources int
	im.Upstream, im.UpstreamTotal, _ = g.reach(i, g.out)
	im.Downstream, im.DownstreamTotal, resources = g.reach(i, g.in)
	im.DownstreamResources = resources
	im.Truncated = im.UpstreamTotal > len(im.Upstream) || im.DownstreamTotal > len(im.Downstream)
	return im
}

// Reachable returns the set of node indices that depend on node i (its potential impact).
func (g *Graph) Reachable(i int) map[int]bool {
	seen := map[int]bool{i: true}
	queue := []int{i}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range g.in[v] {
			if !seen[w] {
				seen[w] = true
				queue = append(queue, w)
			}
		}
	}
	delete(seen, i)
	return seen
}

func ids(list []int) []string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = nodeID(v)
	}
	slices.Sort(out)
	return out
}

func (g *Graph) reach(start int, adj [][]int) ([]Reach, int, int) {
	distance := map[int]int{start: 0}
	queue := []int{start}
	out := []Reach{}
	total, resources := 0, 0
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range adj[v] {
			if _, ok := distance[w]; ok {
				continue
			}
			distance[w] = distance[v] + 1
			queue = append(queue, w)
			total++
			if isResource(g.Nodes[w].Kind) {
				resources++
			}
			if len(out) < MaxReach {
				out = append(out, Reach{Node: nodeID(w), Distance: distance[w]})
			}
		}
	}
	return out, total, resources
}

// PlannedChange is one non-no-op change from a full, current plan.
type PlannedChange struct {
	ChangeID string `json:"changeId"`
	Address  string `json:"address"`
	Mode     string `json:"mode"`
	Action   string `json:"action"`
	// Node is the graph node the change maps to; empty when it maps to none.
	Node string `json:"node,omitempty"`
}

// Overlay maps planned changes to nodes by address grammar: an exact instanced node first,
// then the configuration resource. Changes that match no node are returned separately.
func (g *Graph) Overlay(nodes []Node, changes []PlannedChange) (unmapped []PlannedChange) {
	exact := map[string]int{}
	for i, n := range nodes {
		if isResource(n.Kind) {
			exact[n.ResourceAddress] = i
		}
	}
	for _, c := range changes {
		i, ok := exact[c.Address]
		if !ok {
			if key, parsed := ConfigAddress(c.Address); parsed {
				i, ok = exact[key]
			}
		}
		if ok && (c.Mode == "data") != (nodes[i].Kind == KindData) {
			ok = false
		}
		if !ok {
			unmapped = append(unmapped, c)
			continue
		}
		c.Node = nodes[i].ID
		nodes[i].Planned = append(nodes[i].Planned, c)
	}
	return unmapped
}
