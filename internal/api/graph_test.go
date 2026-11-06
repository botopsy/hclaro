package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

type graphView struct {
	operations.GraphView
}

func startGraph(t *testing.T, s *planServer) (operations.Operation, graphView) {
	t.Helper()
	w := s.do("POST", "/api/v1/graphs", `{"acknowledgeEngineRun":true}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	started := decode[struct {
		Operation operations.Operation `json:"operation"`
		Graph     operations.GraphRun  `json:"graph"`
	}](t, w)
	done := s.wait(started.Operation.ID)
	return done, decode[graphView](t, s.do("GET", "/api/v1/graphs/"+started.Graph.ID, ""))
}

func planAndWait(t *testing.T, s *planServer) string {
	t.Helper()
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := s.wait(decode[operations.Operation](t, w).ID)
	if op.PlanID == "" {
		t.Fatalf("%+v", op)
	}
	return op.PlanID
}

func nodeID(t *testing.T, v graphView, name string) string {
	t.Helper()
	for _, n := range v.Nodes {
		if n.DisplayName == name {
			return n.ID
		}
	}
	t.Fatalf("no node %s", name)
	return ""
}

func TestGraphAPI(t *testing.T) {
	fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform, PlanJSON: fixtures.PlanAllActions}
	s, store := newDriftServer(t, fake)
	options := decode[struct {
		CanGenerate bool     `json:"canGenerate"`
		Command     []string `json:"command"`
	}](t, s.do("GET", "/api/v1/graphs/options", ""))
	if !options.CanGenerate || !slices.Equal(options.Command, []string{"terraform", "graph"}) {
		t.Fatalf("%+v", options)
	}
	// Browsing runs nothing.
	for _, path := range []string{"/api/v1/graphs/options", "/api/v1/graphs", "/api/v1/workspace"} {
		s.do("GET", path, "")
	}
	if slices.Contains(fake.Calls(), "graph") {
		t.Fatal("GET ran graph")
	}
	for body, code := range map[string]int{`{}`: 400, `{"acknowledgeEngineRun":false}`: 400, `{"acknowledgeEngineRun":true,"type":"apply"}`: 400, `{"acknowledgeEngineRun":true,"plan":"/tmp/x.tfplan"}`: 400, `{"acknowledgeEngineRun":true,"profileId":"../x"}`: 400} {
		if w := s.do("POST", "/api/v1/graphs", body); w.Code != code {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	planID := planAndWait(t, s)
	op, view := startGraph(t, s)
	if op.Kind != "graph" || view.Status != "completed" || !view.Current || len(view.Nodes) != 18 || len(view.ResourceEdges) == 0 {
		t.Fatalf("%+v %+v", op, view.GraphRun)
	}
	if slices.Contains(fake.Calls(), "apply") || len(fake.Requests()) != 1 {
		t.Fatal("graph planned or applied")
	}
	// Instances link to the resource inspector.
	for _, n := range view.Nodes {
		for _, i := range n.Instances {
			if w := s.do("GET", "/api/v1/resources/"+i.ResourceID, ""); w.Code != 200 {
				t.Fatalf("%s does not resolve", i.Address)
			}
		}
	}
	sg := nodeID(t, view, "aws_security_group.web")
	impact := decode[operations.GraphImpact](t, s.do("GET", "/api/v1/graphs/"+view.ID+"/nodes/"+sg+"?planId="+planID, ""))
	if impact.DownstreamResources != 6 || impact.Plan == nil || len(impact.Plan.InImpact) != 6 {
		t.Fatalf("%+v", impact)
	}
	overlay := decode[graphView](t, s.do("GET", "/api/v1/graphs/"+view.ID+"?planId="+planID, ""))
	if overlay.Plan == nil || overlay.Plan.ID != planID {
		t.Fatalf("%+v", overlay.Plan)
	}
	events := s.do("GET", "/api/v1/operations/"+op.ID+"/events", "")
	if !strings.Contains(events.Body.String(), "Graph read") || !strings.Contains(events.Body.String(), "event: end") {
		t.Fatalf("%s", events.Body)
	}
	zero := strings.Repeat("0", 32)
	for path, code := range map[string]int{
		"/api/v1/graphs/" + zero:                                         404,
		"/api/v1/graphs/..%2f..":                                         404,
		"/api/v1/graphs/" + view.ID + "?planId=" + zero:                  404,
		"/api/v1/graphs/" + view.ID + "?planId=../x":                     404,
		"/api/v1/graphs/" + view.ID + "/nodes/n9999":                     404,
		"/api/v1/graphs/" + view.ID + "/nodes/x":                         404,
		"/api/v1/graphs/" + view.ID + "/nodes/" + sg + "?planId=" + zero: 404,
		"/api/v1/graphs/" + zero + "/nodes/n0":                           404,
	} {
		if w := s.do("GET", path, ""); w.Code != code {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	// Another workspace: withheld, impact refused, the list has no node names.
	fake.SetWorkspace("other")
	store.Load(t.Context(), fake, "terraform")
	stale := decode[graphView](t, s.do("GET", "/api/v1/graphs/"+view.ID, ""))
	if stale.Current || len(stale.Nodes) != 0 || !stale.Withheld {
		t.Fatalf("%+v", stale.GraphRun)
	}
	if w := s.do("GET", "/api/v1/graphs/"+view.ID+"/nodes/"+sg, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "graph_stale") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("GET", "/api/v1/graphs/"+view.ID+"?planId="+planID, ""); w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if list := s.do("GET", "/api/v1/graphs", ""); strings.Contains(list.Body.String(), "aws_security_group") {
		t.Fatalf("list leaked nodes: %s", list.Body)
	}
}

func TestGraphAPIFailuresAndUnsupported(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{GraphDOT: []byte(`digraph { "aws_instance.a" -> "` + canary + `" `)})
	op, view := startGraph(t, s)
	if op.Status != "failed" || view.Diagnostic == nil || view.Diagnostic.Code != "graph_format" {
		t.Fatalf("%+v", view.GraphRun)
	}
	remote := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State, GraphDOT: fixtures.GraphTerraform}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "remote", ExecutionMode: "remote-exec"})
	store.Load(t.Context(), remote, "terraform")
	manager := operations.NewManager(store, remote, t.TempDir())
	t.Cleanup(func() { manager.Close(time.Second) })
	rs := &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), remote, manager}
	options := decode[struct {
		CanGenerate bool   `json:"canGenerate"`
		Reason      string `json:"reason"`
	}](t, rs.do("GET", "/api/v1/graphs/options", ""))
	if options.CanGenerate || !strings.Contains(options.Reason, "remotely") {
		t.Fatalf("%+v", options)
	}
	if w := rs.do("POST", "/api/v1/graphs", `{"acknowledgeEngineRun":true}`); w.Code != 409 || !strings.Contains(w.Body.String(), "graph_unsupported") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if slices.Contains(remote.Calls(), "graph") {
		t.Fatal("unsupported graph ran")
	}
}

func TestGraphRouteGuard(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{GraphDOT: fixtures.GraphTerraform})
	zero := strings.Repeat("0", 32)
	for _, tc := range []struct {
		name, method, path, origin, csrfHeader string
		code                                   int
	}{
		{"no origin", "POST", "/api/v1/graphs", "", csrf, 403},
		{"foreign origin", "POST", "/api/v1/graphs", "http://evil.example", csrf, 403},
		{"no csrf", "POST", "/api/v1/graphs", "http://" + host, "", 403},
		{"no post on a graph", "POST", "/api/v1/graphs/" + zero, "http://" + host, csrf, 405},
		{"no apply from a graph", "POST", "/api/v1/graphs/" + zero + "/apply", "http://" + host, csrf, 405},
		{"no post on a node", "POST", "/api/v1/graphs/" + zero + "/nodes/n0", "http://" + host, csrf, 405},
		{"no delete", "DELETE", "/api/v1/graphs/" + zero, "http://" + host, csrf, 405},
	} {
		r := httptest.NewRequest(tc.method, "http://"+host+tc.path, strings.NewReader(`{"acknowledgeEngineRun":true}`))
		r.Header.Set("Authorization", "Bearer "+session)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.csrfHeader != "" {
			r.Header.Set("X-Hclaro-CSRF", tc.csrfHeader)
		}
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.name, w.Code)
		}
	}
	if slices.Contains(s.fake.Calls(), "graph") {
		t.Fatal("refused request ran the engine")
	}
	r := httptest.NewRequest("GET", "http://"+host+"/api/v1/graphs", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

// TestGraphGolden pins the graph, overlay and impact responses the UI consumes.
func TestGraphGolden(t *testing.T) {
	fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform, PlanJSON: fixtures.PlanAllActions}
	s, _ := newDriftServer(t, fake)
	n := 0
	s.manager.NewID = func() string { n++; return fmt.Sprintf("%032x", n) }
	planID := planAndWait(t, s)
	_, view := startGraph(t, s)
	var doc map[string]any
	if err := json.Unmarshal(s.do("GET", "/api/v1/graphs/"+view.ID+"?planId="+planID, "").Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	doc["startedAt"], doc["completedAt"] = "2026-11-06T10:00:00Z", "2026-11-06T10:00:01Z"
	doc["plan"].(map[string]any)["createdAt"] = "2026-11-06T09:59:00Z"
	var impact any
	sg := nodeID(t, view, "aws_security_group.web")
	if err := json.Unmarshal(s.do("GET", "/api/v1/graphs/"+view.ID+"/nodes/"+sg+"?planId="+planID, "").Body.Bytes(), &impact); err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(map[string]any{"graph": doc, "impact": impact, "planId": planID}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "fixtures", "graph", "terraform.golden.json")
	if *updateGolden {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != string(got) {
		t.Fatalf("graph response differs from golden; rerun with -update and review the diff\n%s", got)
	}
}
