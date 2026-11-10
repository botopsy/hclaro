package api

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// Tests for Hub-scoped drift scans and graphs (M24.09). Engine commands run the synthetic
// CLI in temporary roots; nothing contacts a real backend, provider or account.

type analysisOptionsBody struct {
	CanScan            bool                `json:"canScan"`
	CanGenerate        bool                `json:"canGenerate"`
	Code               string              `json:"code"`
	Reason             string              `json:"reason"`
	Workspace          string              `json:"workspace"`
	Scope              *projects.ScopeView `json:"scope"`
	Command            []string            `json:"command"`
	ContextFingerprint string              `json:"contextFingerprint"`
}

func (s *scopedServer) analysisOptions(cid, kind string) analysisOptionsBody {
	s.t.Helper()
	w := s.do("GET", "/api/v1/contexts/"+cid+"/"+kind+"/options", "")
	if w.Code != 200 {
		s.t.Fatalf("%s options: %d %s", kind, w.Code, w.Body)
	}
	return decode[analysisOptionsBody](s.t, w)
}

// scan starts a drift scan with the options' fingerprint, as the UI does, and waits for it.
func (s *scopedServer) scan(cid string) operations.DriftScanView {
	s.t.Helper()
	o := s.analysisOptions(cid, "drift-scans")
	if !o.CanScan || o.Scope == nil || o.Scope.ContextID != cid {
		s.t.Fatalf("%+v", o)
	}
	w := s.do("POST", "/api/v1/contexts/"+cid+"/drift-scans", fmt.Sprintf(`{"acknowledgeProviderAccess":true,"contextFingerprint":%q}`, o.ContextFingerprint))
	if w.Code != 202 {
		s.t.Fatalf("scan: %d %s", w.Code, w.Body)
	}
	started := decode[struct {
		Operation scopedOp             `json:"operation"`
		Scan      operations.DriftScan `json:"scan"`
	}](s.t, w)
	if started.Operation.Kind != "drift" || started.Operation.Scope.ContextID != cid || started.Scan.OperationID != started.Operation.ID {
		s.t.Fatalf("%+v", started)
	}
	s.waitScoped(cid, started.Operation.ID)
	return decode[operations.DriftScanView](s.t, s.do("GET", "/api/v1/contexts/"+cid+"/drift-scans/"+started.Scan.ID, ""))
}

func (s *scopedServer) graph(cid string) operations.GraphView {
	s.t.Helper()
	o := s.analysisOptions(cid, "graphs")
	if !o.CanGenerate || o.Scope == nil || len(o.Command) != 2 || o.Command[1] != "graph" {
		s.t.Fatalf("%+v", o)
	}
	w := s.do("POST", "/api/v1/contexts/"+cid+"/graphs", fmt.Sprintf(`{"acknowledgeEngineRun":true,"contextFingerprint":%q}`, o.ContextFingerprint))
	if w.Code != 202 {
		s.t.Fatalf("graph: %d %s", w.Code, w.Body)
	}
	started := decode[struct {
		Operation scopedOp            `json:"operation"`
		Graph     operations.GraphRun `json:"graph"`
	}](s.t, w)
	s.waitScoped(cid, started.Operation.ID)
	return decode[operations.GraphView](s.t, s.do("GET", "/api/v1/contexts/"+cid+"/graphs/"+started.Graph.ID, ""))
}

// network/dev and network/prod share a root and record identical addresses; platform/staging
// is OpenTofu. Each scan and graph runs in its own root and workspace, is listed and readable
// only in its own context, and nothing reaches the launch project. Nothing is applied,
// refreshed or kept as an applicable plan.
func TestScopedDriftAndGraphStayInTheirOwnContext(t *testing.T) {
	s := newScopedServer(t)
	type target struct{ project, ws, cid string }
	targets := []*target{{"network", "dev", ""}, {"network", "prod", ""}, {"platform", "staging", ""}}
	scans, graphs := map[string]operations.DriftScanView{}, map[string]operations.GraphView{}
	for i, tc := range targets {
		tc.cid = s.openLoaded(tc.project, tc.ws, "")
		scan := s.scan(tc.cid)
		if scan.Workspace != tc.ws || !scan.Current || len(scan.Resources) == 0 || scan.Status == operations.ScanFailed {
			t.Fatalf("%s/%s: %+v", tc.project, tc.ws, scan)
		}
		calls := s.planCalls()
		if len(calls) != i+1 || !slices.Contains(calls[i], "-refresh-only") {
			t.Fatalf("%d plan calls: %q", len(calls), calls)
		}
		if cwd, _ := filepath.EvalSymlinks(spyField(calls[i], "CWD=")); cwd != s.roots[tc.project] || spyField(calls[i], "TF_WORKSPACE=") != tc.ws {
			t.Fatalf("scan ran in %q workspace %q", cwd, spyField(calls[i], "TF_WORKSPACE="))
		}
		g := s.graph(tc.cid)
		if g.Workspace != tc.ws || !g.Current || len(g.Nodes) == 0 {
			t.Fatalf("%+v", g.GraphRun)
		}
		scans[tc.cid], graphs[tc.cid] = scan, g
	}
	// The engines produce different graphs: Terraform the resource graph, OpenTofu the full walk.
	if graphs[targets[0].cid].Format == graphs[targets[2].cid].Format {
		t.Fatalf("formats: %s", graphs[targets[0].cid].Format)
	}
	missing := func(path, code string) string {
		t.Helper()
		w := s.do("GET", path, "")
		wantError(t, w, 404, code)
		return w.Body.String()
	}
	for _, own := range targets {
		list := decode[struct {
			Scans []operations.DriftScanView `json:"scans"`
		}](t, s.do("GET", "/api/v1/contexts/"+own.cid+"/drift-scans", ""))
		if len(list.Scans) != 1 || list.Scans[0].ID != scans[own.cid].ID {
			t.Fatalf("%s/%s scans: %+v", own.project, own.ws, list)
		}
		glist := decode[struct {
			Graphs []operations.GraphView `json:"graphs"`
		}](t, s.do("GET", "/api/v1/contexts/"+own.cid+"/graphs", ""))
		if len(glist.Graphs) != 1 || glist.Graphs[0].ID != graphs[own.cid].ID {
			t.Fatalf("%s/%s graphs: %+v", own.project, own.ws, glist)
		}
		change := scans[own.cid].Resources[0].ID
		if w := s.do("GET", "/api/v1/contexts/"+own.cid+"/drift-scans/"+scans[own.cid].ID+"/resources/"+change, ""); w.Code != 200 {
			t.Fatalf("own detail: %d %s", w.Code, w.Body)
		}
		node := graphs[own.cid].Nodes[0].ID
		if w := s.do("GET", "/api/v1/contexts/"+own.cid+"/graphs/"+graphs[own.cid].ID+"/nodes/"+node, ""); w.Code != 200 {
			t.Fatalf("own node: %d %s", w.Code, w.Body)
		}
		// Another context's IDs, and the launch routes, answer exactly like unknown IDs.
		unknownScan := missing("/api/v1/contexts/"+own.cid+"/drift-scans/"+strings.Repeat("e", 32), "scan_not_found")
		unknownGraph := missing("/api/v1/contexts/"+own.cid+"/graphs/"+strings.Repeat("e", 32), "graph_not_found")
		for _, other := range targets {
			if other == own {
				continue
			}
			if body := missing("/api/v1/contexts/"+other.cid+"/drift-scans/"+scans[own.cid].ID, "scan_not_found"); body != unknownScan {
				t.Fatalf("%s != %s", body, unknownScan)
			}
			missing("/api/v1/contexts/"+other.cid+"/drift-scans/"+scans[own.cid].ID+"/resources/"+change, "change_not_found")
			if body := missing("/api/v1/contexts/"+other.cid+"/graphs/"+graphs[own.cid].ID, "graph_not_found"); body != unknownGraph {
				t.Fatalf("%s != %s", body, unknownGraph)
			}
			missing("/api/v1/contexts/"+other.cid+"/graphs/"+graphs[own.cid].ID+"/nodes/"+node, "graph_not_found")
		}
		missing("/api/v1/drift-scans/"+scans[own.cid].ID, "scan_not_found")
		missing("/api/v1/graphs/"+graphs[own.cid].ID, "graph_not_found")
	}
	// A plan of another context cannot be overlaid on this context's graph.
	a, b := targets[0].cid, targets[1].cid
	done := s.waitScoped(b, s.plan(b).ID)
	missing("/api/v1/contexts/"+a+"/graphs/"+graphs[a].ID+"?planId="+done.PlanID, "plan_not_found")
	if w := s.do("GET", "/api/v1/contexts/"+b+"/graphs/"+graphs[b].ID+"?planId="+done.PlanID, ""); w.Code != 200 {
		t.Fatalf("own plan overlay: %d %s", w.Code, w.Body)
	}
	// A scan's refresh-only plan is never listed as a plan of its context.
	if plans := decode[struct {
		Plans []planSummary `json:"plans"`
	}](t, s.do("GET", "/api/v1/contexts/"+a+"/plans", "")); len(plans.Plans) != 0 {
		t.Fatalf("%+v", plans)
	}
	if len(s.launch.DriftScans()) != 0 || len(s.launch.Graphs()) != 0 || len(s.launch.Operations()) != 0 {
		t.Fatal("a scoped scan or graph reached the launch manager")
	}
	if len(s.calls("apply")) != 0 || len(s.calls("refresh")) != 0 || len(s.calls("import")) != 0 {
		t.Fatal("a read-only operation wrote state")
	}
}

// Results live in their scope: evicting or closing a scope drops them, so retention stays
// bounded by MaxScopes times the launch bounds however many contexts are opened.
func TestScopedDriftAndGraphRetentionFollowsTheScope(t *testing.T) {
	s := newScopedServer(t)
	s.hub.MaxScopes = 1
	a := s.openLoaded("network", "dev", "")
	scan := s.scan(a)
	g := s.graph(a)
	b := s.openLoaded("platform", "staging", "")
	s.analysisOptions(b, "graphs") // creates B's scope and evicts A's idle one
	wantError(t, s.do("GET", "/api/v1/contexts/"+a+"/drift-scans/"+scan.ID, ""), 404, "scan_not_found")
	wantError(t, s.do("GET", "/api/v1/contexts/"+a+"/graphs/"+g.ID, ""), 404, "graph_not_found")
	if list := decode[map[string][]any](t, s.do("GET", "/api/v1/contexts/"+a+"/drift-scans", "")); len(list["scans"]) != 0 {
		t.Fatalf("%+v", list)
	}
	// Listing does not recreate the scope or run anything.
	calls := len(s.planCalls())
	s.do("GET", "/api/v1/contexts/"+a+"/graphs", "")
	if len(s.planCalls()) != calls || len(s.calls("graph")) != 1 {
		t.Fatal("a GET ran the engine")
	}
}

// Starting a scan or graph validates its body like the launch routes, takes no target
// choice, and is refused before the engine runs when the gate is off, the context is closed
// or the project changed.
func TestScopedDriftAndGraphRequestValidation(t *testing.T) {
	s := newScopedServer(t)
	a := s.openLoaded("network", "dev", "")
	idle := s.openLoaded("network", "prod", "")
	scan := `{"acknowledgeProviderAccess":true,"contextFingerprint":""}`
	graph := `{"acknowledgeEngineRun":true,"contextFingerprint":""}`
	cases := []struct {
		path, body string
		status     int
		code       string
	}{
		{"drift-scans", `{"contextFingerprint":""}`, 400, "acknowledgement_required"},
		{"drift-scans", `{"acknowledgeProviderAccess":false}`, 400, "acknowledgement_required"},
		{"graphs", `{"acknowledgeProviderAccess":true}`, 400, "invalid_request"},
		{"graphs", `{}`, 400, "acknowledgement_required"},
		{"drift-scans", strings.Replace(scan, `{`, `{"profileId":"x",`, 1), 400, "invalid_request"},
		{"drift-scans", strings.Replace(scan, `{`, `{"workspace":"prod",`, 1), 400, "invalid_request"},
		{"drift-scans", strings.Replace(scan, `{`, `{"rootPath":"/tmp",`, 1), 400, "invalid_request"},
		{"drift-scans", strings.Replace(scan, `{`, `{"target":["aws_s3_bucket.logs"],`, 1), 400, "invalid_request"},
		{"graphs", strings.Replace(graph, `{`, `{"command":["apply"],`, 1), 400, "invalid_request"},
		{"graphs", strings.Replace(graph, `{`, `{"executable":"/bin/sh",`, 1), 400, "invalid_request"},
		{"drift-scans", strings.Replace(scan, `""`, `"x"`, 1), 409, "context_changed"},
		{"graphs", strings.Replace(graph, `""`, `"x"`, 1), 409, "context_changed"},
		{"drift-scans", strings.Replace(scan, `{`, `{"varFiles":["../x.tfvars"],`, 1), 400, "invalid_var_files"},
		{"drift-scans", strings.Replace(scan, `""`, `"`+strings.Repeat("x", 129)+`"`, 1), 400, "invalid_request"},
	}
	for _, c := range cases {
		wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/"+c.path, c.body), c.status, c.code)
	}
	// A context without a scope has nothing to list, and unknown IDs look the same as in a scope.
	if list := decode[map[string][]any](t, s.do("GET", "/api/v1/contexts/"+idle+"/graphs", "")); len(list["graphs"]) != 0 {
		t.Fatalf("%+v", list)
	}
	wantError(t, s.do("GET", "/api/v1/contexts/"+idle+"/drift-scans/"+strings.Repeat("e", 32)+"/resources/"+strings.Repeat("e", 32), ""), 404, "change_not_found")
	wantError(t, s.do("GET", "/api/v1/contexts/"+idle+"/graphs/"+strings.Repeat("e", 32)+"/nodes/n1", ""), 404, "graph_not_found")
	wantError(t, s.do("GET", "/api/v1/contexts/"+strings.Repeat("f", 32)+"/drift-scans", ""), 404, "context_closed")

	// The project folder is replaced: refused, the context closed, nothing ran.
	moved := s.roots["network"] + "-moved"
	if err := os.Rename(s.roots["network"], moved); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(s.roots["network"], os.DirFS(moved)); err != nil {
		t.Fatal(err)
	}
	w := s.do("POST", "/api/v1/contexts/"+a+"/drift-scans", scan)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "scope_changed") {
		t.Fatalf("changed project: %d %s", w.Code, w.Body)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/graphs", graph), 404, "context_closed")

	// Gate off: every route answers scoped_execution_disabled first.
	s.hub.ScopedExecution = false
	for _, p := range []string{"/drift-scans/options", "/drift-scans", "/drift-scans/" + strings.Repeat("e", 32), "/graphs/options", "/graphs", "/graphs/" + strings.Repeat("e", 32)} {
		wantError(t, s.do("GET", "/api/v1/contexts/"+idle+p, ""), 404, projects.CodeScopedDisabled)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+idle+"/drift-scans", scan), 404, projects.CodeScopedDisabled)
	wantError(t, s.do("POST", "/api/v1/contexts/"+idle+"/graphs", graph), 404, projects.CodeScopedDisabled)
	if len(s.planCalls()) != 0 || len(s.calls("graph")) != 0 {
		t.Fatal("a refused request ran the engine")
	}
}

// A workspace whose execution is unsupported gets options explaining why, not an error.
func TestScopedAnalysisOptionsExplainUnsupportedWorkspaces(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("legacy", "default", "") // not initialized
	for _, kind := range []string{"drift-scans", "graphs"} {
		o := s.analysisOptions(cid, kind)
		if o.CanScan || o.CanGenerate || o.Code != projects.CodeExecutionUnsupported || o.Reason == "" || o.Scope != nil {
			t.Fatalf("%s: %+v", kind, o)
		}
	}
}
