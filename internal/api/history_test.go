package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

type historyServer struct {
	*hubServer
	manager *operations.Manager
	fake    *engine.Fake
	store   *history.Store
	config  string
	root    string
}

// newHistoryServer serves a launch project planned with the fake engine and a Hub, both
// recording into one history in config.
func newHistoryServer(t *testing.T, config string) *historyServer {
	t.Helper()
	t.Setenv("TF_DATA_DIR", "")
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State, PlanJSON: fixtures.PlanAllActions}
	ws := workspace.New(workspace.Info{Project: "demo", Engine: "terraform", Version: "1.9.8", Workspace: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	ws.Load(context.Background(), fake, "terraform")
	root := t.TempDir()
	hub := projects.NewHub(projects.Open(config, time.Now()), root)
	t.Cleanup(hub.Close)
	store := history.Open(config, time.Now())
	store.Sync = false
	hub.UseHistory(store)
	manager := operations.NewManager(ws, fake, root)
	manager.TempDir = t.TempDir()
	manager.Reload = func(context.Context) {}
	manager.Recorder = store
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	h := Handler(ws, manager, hub, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	return &historyServer{&hubServer{t, h, hub, &atomic.Int64{}, ""}, manager, fake, store, config, root}
}

func (s *historyServer) plan(replace ...string) string {
	s.t.Helper()
	op, err := s.manager.StartPlan(nil, replace)
	if err != nil {
		s.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := s.store.Get(op.ID); ok && e.Status != history.StatusRunning {
			return op.ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Fatal("plan did not finish")
	return ""
}

type historyPage struct {
	Status    string
	Notice    string
	Total     int
	Stored    int
	Retention history.HistoryRetentionPolicy
	Entries   []struct {
		history.HistoryEntry
		Live bool
	}
	Projects []struct {
		ID, Name   string
		Registered bool
		Workspaces []string
	}
}

func TestHistoryListDetailAndFilters(t *testing.T) {
	s := newHistoryServer(t, t.TempDir())
	first := s.plan()
	s.fake.PlanErr = &engine.ExitError{Code: 1}
	s.fake.Lines = [][]byte{[]byte(`{"type":"diagnostic","diagnostic":{"severity":"error","summary":"x","detail":"password=` + fixtures.Canary + `"}}`), []byte("raw " + fixtures.Canary)}
	failed := s.plan()

	page := decode[historyPage](t, s.do("GET", "/api/v1/history", ""))
	if page.Status != history.Ready || page.Total != 2 || page.Stored != 2 || page.Entries[0].ID != failed || page.Entries[1].ID != first {
		t.Fatalf("%+v", page)
	}
	if !page.Entries[0].Live || len(page.Entries[0].Excerpt) != 0 || page.Entries[0].Status != history.StatusFailed || page.Entries[1].Plan == nil {
		t.Fatalf("list entry: %+v", page.Entries[0])
	}
	if len(page.Projects) != 1 || page.Projects[0].Name != filepath.Base(s.root) || page.Projects[0].Registered || strings.Join(page.Projects[0].Workspaces, ",") != "demo" {
		t.Fatalf("projects: %+v", page.Projects)
	}
	project := page.Projects[0].ID
	if p := decode[historyPage](t, s.do("GET", "/api/v1/history?projectId="+project+"&workspace=demo&status=failed", "")); p.Total != 1 || p.Entries[0].ID != failed {
		t.Fatalf("%+v", p)
	}
	if p := decode[historyPage](t, s.do("GET", "/api/v1/history?projectId="+strings.Repeat("f", 32), "")); p.Total != 0 || len(p.Entries) != 0 || p.Stored != 2 {
		t.Fatalf("another project's filter returned entries: %+v", p)
	}
	if p := decode[historyPage](t, s.do("GET", "/api/v1/history?query=aws_&kind=plan&limit=1", "")); p.Total != 1 || len(p.Entries) != 1 {
		t.Fatalf("%+v", p)
	}
	for _, q := range []string{"projectId=../x", "workspace=-lock=false", "kind=shell", "status=ok", "limit=0", "limit=201", "offset=-1", "path=/etc/passwd", "query=" + strings.Repeat("a", 201), "query=%0a"} {
		if w := s.do("GET", "/api/v1/history?"+q, ""); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	w := s.do("GET", "/api/v1/history/"+failed, "")
	detail := decode[struct {
		history.HistoryEntry
		Live bool
	}](t, w)
	if len(detail.Excerpt) == 0 || detail.Diagnostic == nil || detail.Outcome.Result != "not_planned" || !detail.Live {
		t.Fatalf("%+v", detail)
	}
	if strings.Contains(w.Body.String(), `"session"`) || strings.Contains(w.Body.String(), s.root) {
		t.Fatal("session marker or root path in the response")
	}
	for _, path := range []string{"/api/v1/history/" + strings.Repeat("0", 32), "/api/v1/history/not-an-id"} {
		if w := s.do("GET", path, ""); w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := s.do("GET", "/history", ""); w.Code != 200 || w.Body.String() != "app" {
		t.Fatal("history client route")
	}
}

func TestHistoryMutationsNeedSessionAndCSRF(t *testing.T) {
	s := newHistoryServer(t, t.TempDir())
	id := s.plan()
	for _, tc := range []struct {
		method, path, body string
		opts               []opt
		code               int
	}{
		{"GET", "/api/v1/history", "", []opt{without("Authorization")}, 401},
		{"GET", "/api/v1/history/" + id, "", []opt{with("Authorization", "Bearer "+strings.Repeat("0", 64))}, 401},
		{"DELETE", "/api/v1/history/" + id, "{}", []opt{without("X-Hclaro-CSRF")}, 403},
		{"DELETE", "/api/v1/history/" + id, "{}", []opt{with("Origin", "https://evil.example")}, 403},
		{"DELETE", "/api/v1/history", `{"confirm":true}`, []opt{without("X-Hclaro-CSRF")}, 403},
		{"PATCH", "/api/v1/history/retention", `{"maxEntries":10,"maxAgeDays":1,"maxBytes":65536}`, []opt{without("X-Hclaro-CSRF")}, 403},
		{"POST", "/api/v1/history/export", `{"format":"json","path":"/tmp/x.json"}`, nil, 405},
		{"PUT", "/api/v1/history/" + id, `{}`, nil, 405},
	} {
		if w := s.do(tc.method, tc.path, tc.body, tc.opts...); w.Code != tc.code {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	if _, ok := s.store.Get(id); !ok {
		t.Fatal("a refused request deleted history")
	}
}

func TestHistoryDeleteClearAndRetention(t *testing.T) {
	config := t.TempDir()
	s := newHistoryServer(t, config)
	a, b, c := s.plan(), s.plan(), s.plan()
	projectFiles := func() string {
		list, _ := os.ReadDir(s.root)
		names := []string{}
		for _, e := range list {
			names = append(names, e.Name())
		}
		return strings.Join(names, ",")
	}
	beforeFiles, beforeCalls := projectFiles(), len(s.fake.Calls())
	if w := s.do("DELETE", "/api/v1/history/"+a, `{}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("DELETE", "/api/v1/history/"+a, `{}`); w.Code != 404 {
		t.Fatalf("second delete: %d", w.Code)
	}
	if w := s.do("DELETE", "/api/v1/history/"+b, `{"force":true}`); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"confirm":false}`, `{"confirm":true,"projectId":"x"}`, `{"confirm":true,"workspace":"demo"}`} {
		if w := s.do("DELETE", "/api/v1/history", body); w.Code != 400 {
			t.Errorf("clear %s: %d", body, w.Code)
		}
	}
	// Clearing another project removes nothing here.
	if r := decode[map[string]int](t, s.do("DELETE", "/api/v1/history", `{"confirm":true,"projectId":"`+strings.Repeat("e", 32)+`"}`)); r["removed"] != 0 {
		t.Fatal(r)
	}
	if r := decode[map[string]int](t, s.do("DELETE", "/api/v1/history", `{"confirm":true}`)); r["removed"] != 2 {
		t.Fatal(r)
	}
	if _, ok := s.store.Get(c); ok {
		t.Fatal("not cleared")
	}
	if projectFiles() != beforeFiles || len(s.fake.Calls()) != beforeCalls {
		t.Fatal("deleting history touched the project or ran the engine")
	}
	// A running operation's entry cannot be deleted.
	s.fake.Block = true
	op, err := s.manager.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := s.do("DELETE", "/api/v1/history/"+op.ID, `{}`); w.Code != 409 {
		t.Fatalf("running: %d", w.Code)
	}
	_, _ = s.manager.Cancel(op.ID)

	w := s.do("PATCH", "/api/v1/history/retention", `{"maxEntries":9,"maxAgeDays":30,"maxBytes":1048576}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "between 10 and 5000") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("PATCH", "/api/v1/history/retention", `{"maxEntries":10,"maxAgeDays":30,"maxBytes":1048576,"path":"/x"}`); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if p := decode[history.HistoryRetentionPolicy](t, s.do("PATCH", "/api/v1/history/retention", `{"maxEntries":10,"maxAgeDays":30,"maxBytes":1048576}`)); p.MaxEntries != 10 {
		t.Fatal(p)
	}
	if p := decode[historyPage](t, s.do("GET", "/api/v1/history", "")); p.Retention.MaxEntries != 10 {
		t.Fatal(p.Retention)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(config, "history.json"))), `"maxEntries":10`) {
		t.Fatal("retention not saved")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHistoryCompareAndExport(t *testing.T) {
	s := newHistoryServer(t, t.TempDir())
	a := s.plan()
	s.fake.PlanJSON = fixtures.PlanCollateral
	b := s.plan()
	s.fake.PlanErr = &engine.ExitError{Code: 1}
	s.fake.Lines = [][]byte{[]byte(`{"type":"diagnostic","diagnostic":{"severity":"error","summary":"x","detail":"token=` + fixtures.Canary + `"}}`)}
	failed := s.plan()
	w := s.do("GET", "/api/v1/history/compare?left="+a+"&right="+b, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	c := decode[history.PlanComparison](t, w)
	if c.Left != a || c.Right != b || len(c.Added)+len(c.Removed)+len(c.Changed)+c.Same == 0 {
		t.Fatalf("%+v", c)
	}
	if w := s.do("GET", "/api/v1/history/compare?left="+a+"&right="+failed, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "plans_incompatible") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, q := range []string{"left=" + a, "left=x&right=" + b, "left=" + a + "&right=" + b + "&path=/etc"} {
		if w := s.do("GET", "/api/v1/history/compare?"+q, ""); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	if w := s.do("GET", "/api/v1/history/compare?left="+a+"&right="+strings.Repeat("0", 32), ""); w.Code != 404 {
		t.Fatal(w.Code)
	}

	w = s.do("GET", "/api/v1/history/export?format=json&status=succeeded", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), `attachment; filename="hclaro-history-`) {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	var report history.Report
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || report.Total != 2 || report.Filter.Status != "succeeded" || report.Omitted[0] != "Log excerpts are not exported." {
		t.Fatalf("%v %+v", err, report)
	}
	for _, e := range report.Entries {
		if len(e.Excerpt) != 0 {
			t.Fatal("excerpt exported")
		}
	}
	w = s.do("GET", "/api/v1/history/export?format=markdown", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/markdown; charset=utf-8" || !strings.Contains(w.Body.String(), "# Hclaro operation history") || strings.Contains(w.Body.String(), s.root) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, q := range []string{"format=html", "", "format=json&path=/tmp/h.json", "format=json&projectId=x"} {
		if w := s.do("GET", "/api/v1/history/export?"+q, ""); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
}

func TestRestartKeepsHistoryButNeverReappliesAPlan(t *testing.T) {
	config := t.TempDir()
	s := newHistoryServer(t, config)
	id := s.plan()
	e, _ := s.store.Get(id)
	planID := e.Plan.PlanID
	s.manager.Close(5 * time.Second)

	restarted := newHistoryServer(t, config)
	page := decode[historyPage](t, restarted.do("GET", "/api/v1/history", ""))
	if page.Total != 1 || page.Entries[0].ID != id || page.Entries[0].Live || page.Entries[0].Plan.PlanID != planID {
		t.Fatalf("%+v", page)
	}
	if w := restarted.do("GET", "/api/v1/plans/"+planID, ""); w.Code != 404 {
		t.Fatalf("historical plan served as reviewable: %d", w.Code)
	}
	if w := restarted.do("POST", "/api/v1/plans/"+planID+"/approval", `{}`); w.Code == 200 {
		t.Fatalf("historical plan approved: %d %s", w.Code, w.Body)
	}
	if w := restarted.do("POST", "/api/v1/plans/"+planID+"/apply", `{"confirmationToken":"`+strings.Repeat("a", 64)+`","confirmation":""}`); w.Code == 200 || w.Code == 202 {
		t.Fatalf("historical plan applied: %d", w.Code)
	}
	if strings.Contains(strings.Join(restarted.fake.Calls(), ","), "apply") {
		t.Fatal("apply ran")
	}
	if w := restarted.do("GET", "/api/v1/operations/"+id+"/events", ""); w.Code != 404 {
		t.Fatalf("a historical log is served: %d", w.Code)
	}
}

// TestHistoryGolden pins the history API responses the UI tests are driven by: a plan, its
// apply, a second plan, a failed plan with a canary, their details and a comparison.
func TestHistoryGolden(t *testing.T) {
	s := newHistoryServer(t, t.TempDir())
	n := 0
	s.manager.NewID = func() string { n++; return fmt.Sprintf("%032x", n) }
	s.fake.ApplyLines = [][]byte{[]byte(`{"type":"version"}`), []byte(`{"type":"change_summary","changes":{"add":1,"change":1,"remove":2,"operation":"apply"}}`)}
	first := s.plan()
	e, _ := s.store.Get(first)
	approval, err := s.manager.Approve(e.Plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := s.manager.StartApply(e.Plan.PlanID, approval.Token, approval.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if a, ok := s.store.Get(apply.ID); ok && a.Status != history.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("apply did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.fake.PlanJSON = fixtures.PlanCollateral
	second := s.plan()
	s.fake.PlanErr = &engine.ExitError{Code: 1}
	s.fake.Lines = [][]byte{[]byte(`{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Invalid provider configuration","detail":"secret=` + fixtures.Canary + `"}}`)}
	failed := s.plan()
	get := func(path string) json.RawMessage {
		w := s.do("GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return json.RawMessage(w.Body.Bytes())
	}
	out := map[string]json.RawMessage{
		"page":    get("/api/v1/history"),
		"plan":    get("/api/v1/history/" + first),
		"apply":   get("/api/v1/history/" + apply.ID),
		"failed":  get("/api/v1/history/" + failed),
		"compare": get("/api/v1/history/compare?left=" + first + "&right=" + second),
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	page := decode[historyPage](t, s.do("GET", "/api/v1/history", ""))
	text := strings.NewReplacer(page.Projects[0].ID, strings.Repeat("a", 32), `"`+filepath.Base(s.root)+`"`, `"network"`).Replace(string(b))
	for pattern, replacement := range map[string]string{
		`"20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z"`: `"2026-11-06T10:00:00Z"`,
		`"durationMs": [0-9]+`:                      `"durationMs": 1000`,
		`"(fingerprint)": "[0-9a-f]{12}"`:           `"$1": "0123456789ab"`,
	} {
		text = regexp.MustCompile(pattern).ReplaceAllString(text, replacement)
	}
	path := filepath.Join("..", "..", "fixtures", "history", "history.golden.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != text {
		t.Fatalf("history response differs from golden; rerun with -update and review the diff\n%s", text)
	}
}
