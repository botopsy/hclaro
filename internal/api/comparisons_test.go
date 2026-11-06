package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/compare"
	"hclaro/internal/engine"
	"hclaro/internal/projects"
)

// compareEngine serves the synthetic comparison states by project folder and workspace,
// without a subprocess. A workspace named "broken" fails its state read.
type compareEngine struct {
	project, selected string
	large             bool
	calls             *atomic.Int64
}

func (e compareEngine) Version(context.Context) (string, error) {
	e.calls.Add(1)
	if e.project == "platform" {
		return "1.8.3", nil
	}
	return "1.9.8", nil
}
func (e compareEngine) Workspace(context.Context) (string, error) {
	e.calls.Add(1)
	return e.selected, nil
}
func (e compareEngine) State(context.Context) ([]byte, error) {
	e.calls.Add(1)
	if e.large {
		return fixtures.CompareLarge(e.selected, 1000), nil
	}
	switch e.project + "/" + e.selected {
	case "network/dev":
		return fixtures.CompareDev, nil
	case "network/prod":
		return fixtures.CompareProd, nil
	case "platform/staging":
		return fixtures.CompareProdTofu, nil
	case "platform/default":
		return fixtures.CompareProdRenamed, nil
	}
	return nil, errors.New("backend unreachable " + fixtures.Canary)
}
func (e compareEngine) ListWorkspaces(context.Context) (engine.WorkspaceList, error) {
	e.calls.Add(1)
	if e.project == "platform" {
		return engine.WorkspaceList{Names: []string{"default", "staging"}, Current: "default"}, nil
	}
	return engine.WorkspaceList{Names: []string{"default", "dev", "prod", "broken"}, Current: "dev"}, nil
}

func newCompareServer(t *testing.T, large bool) *hubServer {
	s := newHubServer(t)
	s.hub.Engines = func(root, preference, selected string, _ []string) (string, string, projects.Adapter, error) {
		name := "terraform"
		if filepath.Base(root) == "platform" {
			name = "tofu"
		}
		return name, "/synthetic/" + name, compareEngine{filepath.Base(root), selected, large, s.calls}, nil
	}
	return s
}

type comparisonDoc struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Current     bool   `json:"current"`
	StaleReason string `json:"staleReason"`
	Withheld    bool   `json:"withheld"`
	Notice      string `json:"notice"`
	Left, Right struct {
		Status     string `json:"status"`
		ContextID  string `json:"contextId"`
		Alias      string `json:"alias"`
		Engine     string `json:"engine"`
		Version    string `json:"version"`
		CapturedAt string `json:"capturedAt"`
		Error      *struct{ Code, Message string }
	}
	Summary   *compare.Summary      `json:"summary"`
	Modules   []compare.ModuleRow   `json:"modules"`
	Providers []compare.ProviderRow `json:"providers"`
	Types     []string              `json:"types"`
	Warnings  []string              `json:"warnings"`
}

type matchPage struct {
	Matches []compare.Match `json:"matches"`
	Total   int             `json:"total"`
}

func target(id, ws string) string { return fmt.Sprintf(`{"projectId":%q,"workspace":%q}`, id, ws) }

func (s *hubServer) compare(left, right, rules string, extra ...string) comparisonDoc {
	s.t.Helper()
	if rules == "" {
		rules = "[]"
	}
	body := fmt.Sprintf(`{"left":%s,"right":%s,"rules":%s,"acknowledgeStateRead":true%s}`, left, right, rules, strings.Join(extra, ""))
	w := s.do("POST", "/api/v1/comparisons", body)
	if w.Code != 202 {
		s.t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	return s.await(decode[comparisonDoc](s.t, w).ID)
}

func (s *hubServer) await(id string) comparisonDoc {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := s.do("GET", "/api/v1/comparisons/"+id, "")
		if w.Code != 200 {
			s.t.Fatalf("get: %d %s", w.Code, w.Body)
		}
		doc := decode[comparisonDoc](s.t, w)
		if doc.Status != "reading" || time.Now().After(deadline) {
			return doc
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestComparisonOfTwoCLIWorkspaces(t *testing.T) {
	s := newCompareServer(t, false)
	p := s.add("network")
	doc := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), "")
	if doc.Status != "ready" || !doc.Current || doc.Withheld || doc.Summary == nil {
		t.Fatalf("%+v", doc)
	}
	if doc.Left.Alias != "Infra" || doc.Left.Engine != "terraform" || doc.Left.Version != "1.9.8" || doc.Left.CapturedAt == "" || doc.Right.ContextID == doc.Left.ContextID {
		t.Fatalf("identity %+v / %+v", doc.Left, doc.Right)
	}
	if !strings.Contains(doc.Notice, "not necessarily drift") {
		t.Fatal("missing drift notice")
	}
	if doc.Summary.Managed.OnlyLeft != 1 || doc.Summary.Data.OnlyRight != 1 || len(doc.Types) == 0 || len(doc.Modules) != 3 || len(doc.Providers) != 2 {
		t.Fatalf("summary %+v modules %d providers %d", doc.Summary, len(doc.Modules), len(doc.Providers))
	}
	w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources?status=only-left,only-right", "")
	page := decode[matchPage](t, w)
	if page.Total != 3 {
		t.Fatalf("one-sided %d", page.Total)
	}
	w = s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources?mode=data", "")
	if page = decode[matchPage](t, w); page.Total != 2 {
		t.Fatalf("data sources %d", page.Total)
	}
	w = s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources?query=aws_db_instance", "")
	page = decode[matchPage](t, w)
	if page.Total != 1 {
		t.Fatalf("query %d", page.Total)
	}
	w = s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources/"+page.Matches[0].ID, "")
	detail := decode[compare.Detail](t, w)
	if w.Code != 200 || detail.LeftAddress != "aws_db_instance.main" || len(detail.Attributes) == 0 {
		t.Fatalf("detail %d %s", w.Code, w.Body)
	}
	for _, q := range []string{"status=drifted", "mode=x", "offset=-1", "limit=500"} {
		if w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources?"+q, ""); w.Code != 400 {
			t.Fatalf("%s: %d", q, w.Code)
		}
	}
	if w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources/"+strings.Repeat("0", 32), ""); w.Code != 404 {
		t.Fatalf("unknown match %d", w.Code)
	}
	list := decode[struct{ Comparisons []comparisonDoc }](t, s.do("GET", "/api/v1/comparisons", ""))
	if len(list.Comparisons) != 1 || list.Comparisons[0].Summary != nil {
		t.Fatalf("list %+v", list)
	}
}

func TestComparisonExportIsRedacted(t *testing.T) {
	s := newCompareServer(t, false)
	p := s.add("network")
	doc := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), `[{"kind":"substitute","left":"dev","right":"prod"},{"kind":"ignore","path":"region"}]`)
	w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/export?format=json", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("json export %d", w.Code)
	}
	body := w.Body.String()
	for _, forbidden := range []string{fixtures.Canary, s.dir, "postgres", "t3.small", "us-east-1"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("report contains %q", forbidden)
		}
	}
	var rep report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || rep.Left.Workspace != "dev" || len(rep.Resources) == 0 || len(rep.Rules) != 2 {
		t.Fatalf("report %v %+v", err, rep)
	}
	w = s.do("GET", "/api/v1/comparisons/"+doc.ID+"/export?format=markdown", "")
	md := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") || !strings.Contains(md, "not necessarily drift") || !strings.Contains(md, "## Only in the left workspace") || !strings.Contains(md, "can hide meaningful divergence") {
		t.Fatalf("markdown %d\n%s", w.Code, md)
	}
	for _, forbidden := range []string{fixtures.Canary, s.dir, "t3.small"} {
		if strings.Contains(md, forbidden) {
			t.Fatalf("markdown contains %q", forbidden)
		}
	}
	if w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/export?format=html", ""); w.Code != 400 {
		t.Fatalf("html export %d", w.Code)
	}
}

func TestComparisonOfDifferentRootsAndEngines(t *testing.T) {
	s := newCompareServer(t, false)
	network, platform := s.add("network"), s.add("platform")
	doc := s.compare(target(network.ID, "prod"), target(platform.ID, "staging"), "")
	if doc.Status != "ready" || doc.Right.Engine != "tofu" || doc.Right.Version != "1.8.3" {
		t.Fatalf("%+v", doc.Right)
	}
	if doc.Summary.Managed.Different+doc.Summary.Managed.OnlyLeft+doc.Summary.Managed.OnlyRight+doc.Summary.Managed.Uncomparable != 0 {
		t.Fatalf("mixed engines %+v", doc.Summary.Managed)
	}
	notes := 0
	for _, p := range doc.Providers {
		notes += len(p.Notes)
	}
	if notes != 2 {
		t.Fatalf("registry notes %+v", doc.Providers)
	}
	// A renamed module pairs only with an explicit rule.
	doc = s.compare(target(network.ID, "prod"), target(platform.ID, "default"), `[{"kind":"map","left":"module.app","right":"module.application"}]`)
	if doc.Summary.Mapped != 4 || doc.Summary.Managed.OnlyLeft != 0 {
		t.Fatalf("mapped %+v", doc.Summary)
	}
}

func TestComparisonRequestValidation(t *testing.T) {
	s := newCompareServer(t, false)
	p := s.add("network")
	valid := fmt.Sprintf(`{"left":%s,"right":%s,"rules":[],"acknowledgeStateRead":true}`, target(p.ID, "dev"), target(p.ID, "prod"))
	for _, tc := range []struct {
		name, body string
		opts       []opt
		code       int
		errorCode  string
	}{
		{"no csrf", valid, []opt{without("X-Hclaro-CSRF")}, 403, ""},
		{"cross-site", valid, []opt{with("Origin", "https://evil.example")}, 403, ""},
		{"no session", valid, []opt{without("Authorization")}, 401, ""},
		{"no acknowledgement", strings.Replace(valid, `,"acknowledgeStateRead":true`, "", 1), nil, 400, "acknowledgement_required"},
		{"unknown field", strings.Replace(valid, `"rules"`, `"path":"/etc","rules"`, 1), nil, 400, "invalid_request"},
		{"command field", strings.Replace(valid, `"rules":[]`, `"rules":[],"command":"apply"`, 1), nil, 400, "invalid_request"},
		{"missing side", fmt.Sprintf(`{"left":%s,"acknowledgeStateRead":true}`, target(p.ID, "dev")), nil, 400, "invalid_target"},
		{"same target", fmt.Sprintf(`{"left":%s,"right":%s,"acknowledgeStateRead":true}`, target(p.ID, "dev"), target(p.ID, "dev")), nil, 400, "same_target"},
		{"flag workspace", fmt.Sprintf(`{"left":%s,"right":%s,"acknowledgeStateRead":true}`, target(p.ID, "dev"), target(p.ID, "-lock=false")), nil, 400, "invalid_workspace"},
		{"path project", fmt.Sprintf(`{"left":%s,"right":%s,"acknowledgeStateRead":true}`, target(p.ID, "dev"), target("../../etc", "prod")), nil, 400, "invalid_target"},
		{"secret rule", fmt.Sprintf(`{"left":%s,"right":%s,"rules":[{"kind":"ignore","path":"id"},{"kind":"substitute","left":%q,"right":"x"}],"acknowledgeStateRead":true}`, target(p.ID, "dev"), target(p.ID, "prod"), fixtures.Canary), nil, 400, "invalid_rule"},
		{"bad rule kind", fmt.Sprintf(`{"left":%s,"right":%s,"rules":[{"kind":"exec","left":"a","right":"b"}],"acknowledgeStateRead":true}`, target(p.ID, "dev"), target(p.ID, "prod")), nil, 400, "invalid_rule"},
	} {
		w := s.do("POST", "/api/v1/comparisons", tc.body, tc.opts...)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body)
		}
		if tc.errorCode != "" && !strings.Contains(w.Body.String(), `"error":"`+tc.errorCode+`"`) {
			t.Fatalf("%s: %s", tc.name, w.Body)
		}
	}
	if w := s.do("POST", "/api/v1/comparisons", fmt.Sprintf(`{"left":%s,"right":%s,"rules":[{"kind":"ignore","path":"id"},{"kind":"substitute","left":%q,"right":"x"}],"acknowledgeStateRead":true}`, target(p.ID, "dev"), target(p.ID, "prod"), fixtures.Canary)); !strings.Contains(w.Body.String(), `"index":1`) {
		t.Fatalf("rule index %s", w.Body)
	}
	if calls := s.calls.Load(); calls != 0 {
		t.Fatalf("refused requests ran %d engine commands", calls)
	}
	if len(s.hub.Comparisons()) != 0 {
		t.Fatal("a refused request stored a comparison")
	}
	for _, path := range []string{"/api/v1/comparisons/" + strings.Repeat("0", 32), "/api/v1/comparisons/not-an-id"} {
		if w := s.do("GET", path, ""); w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := s.do("DELETE", "/api/v1/comparisons/"+strings.Repeat("0", 32), "{}"); w.Code != 405 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := s.do("GET", "/compare", ""); w.Code != 200 || w.Body.String() != "app" {
		t.Fatalf("client route %d", w.Code)
	}
}

func TestComparisonUnavailableSideIsNotEmpty(t *testing.T) {
	s := newCompareServer(t, false)
	network, legacy := s.add("network"), s.add("legacy")
	for _, tc := range []struct {
		name, right, code string
	}{
		{"read failure", target(network.ID, "broken"), "state_read"},
		{"uninitialized", target(legacy.ID, "default"), "uninitialized"},
		{"missing workspace", target(network.ID, "qa"), "workspace_missing"},
		{"unregistered project", target(strings.Repeat("a", 32), "prod"), "project_not_found"},
	} {
		doc := s.compare(target(network.ID, "dev"), tc.right, "")
		if doc.Status != "partial" || !doc.Withheld || doc.Summary != nil || doc.Right.Status != "error" || doc.Right.Error == nil || doc.Left.Status != "ready" {
			t.Fatalf("%s: %+v", tc.name, doc)
		}
		if tc.code != "state_read" && doc.Right.Error.Code != tc.code {
			t.Fatalf("%s: code %s", tc.name, doc.Right.Error.Code)
		}
		if w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "comparison_partial") {
			t.Fatalf("%s: resources %d %s", tc.name, w.Code, w.Body)
		}
		if w := s.do("GET", "/api/v1/comparisons/"+doc.ID+"/export?format=json", ""); w.Code != 409 {
			t.Fatalf("%s: export %d", tc.name, w.Code)
		}
	}
}

func TestComparisonGoesStaleWhenAContextChanges(t *testing.T) {
	s := newCompareServer(t, false)
	p := s.add("network")
	doc := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), "")
	// Another tab reads prod again: the comparison's snapshot is no longer the newest.
	if w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"prod","reload":true}`, p.ID)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	stale := s.await(doc.ID)
	if stale.Status != "stale" || stale.Current || !stale.Withheld || stale.Summary != nil || !strings.Contains(stale.StaleReason, "read again") {
		t.Fatalf("%+v", stale)
	}
	for _, path := range []string{"/resources", "/export?format=json"} {
		if w := s.do("GET", "/api/v1/comparisons/"+doc.ID+path, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "comparison_stale") {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	// Removing the project also invalidates.
	doc = s.compare(target(p.ID, "dev"), target(p.ID, "prod"), "")
	if w := s.do("DELETE", "/api/v1/projects/"+p.ID, "{}"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if stale := s.await(doc.ID); stale.Status != "stale" {
		t.Fatalf("after removal %+v", stale)
	}
}

func TestComparisonRulesReuseOpenReads(t *testing.T) {
	s := newCompareServer(t, false)
	p := s.add("network")
	before := s.target(p.ID, "dev")
	if before.Open || s.calls.Load() != 0 {
		t.Fatalf("target status ran commands or found a read: %+v", before)
	}
	first := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), "")
	calls := s.calls.Load()
	second := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), `[{"kind":"ignore","path":"tags"}]`)
	if s.calls.Load() != calls {
		t.Fatal("changing rules read state again")
	}
	if first.Left.ContextID != second.Left.ContextID || first.Summary.Managed.Different == second.Summary.Managed.Different && second.Summary.IgnoredAttributes == 0 {
		t.Fatalf("rules not applied: %+v / %+v", first.Summary, second.Summary)
	}
	after := s.target(p.ID, "dev")
	if !after.Open || after.CapturedAt == "" || after.Status != "ready" || s.calls.Load() != calls {
		t.Fatalf("after %+v", after)
	}
	third := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), "", `,"reload":true`)
	if s.calls.Load() == calls || third.Left.ContextID == first.Left.ContextID {
		t.Fatal("reload did not read again")
	}
	if s.await(first.ID).Status != "stale" {
		t.Fatal("reloading did not invalidate the earlier comparison")
	}
}

func (s *hubServer) target(projectID, ws string) projects.TargetStatus {
	s.t.Helper()
	w := s.do("GET", "/api/v1/comparisons/target?projectId="+projectID+"&workspace="+ws, "")
	if w.Code != 200 {
		s.t.Fatalf("target %d %s", w.Code, w.Body)
	}
	return decode[projects.TargetStatus](s.t, w)
}

func TestComparisonResultsStayInTheirComparison(t *testing.T) {
	s := newCompareServer(t, false)
	network, platform := s.add("network"), s.add("platform")
	a := s.compare(target(network.ID, "dev"), target(network.ID, "prod"), "")
	b := s.compare(target(platform.ID, "staging"), target(platform.ID, "default"), "")
	page := decode[matchPage](t, s.do("GET", "/api/v1/comparisons/"+a.ID+"/resources?query=aws_iam_role.debug", ""))
	if page.Total != 1 {
		t.Fatal("dev-only role missing")
	}
	if w := s.do("GET", "/api/v1/comparisons/"+b.ID+"/resources/"+page.Matches[0].ID, ""); w.Code != 404 {
		t.Fatalf("another comparison's match: %d %s", w.Code, w.Body)
	}
	all := decode[matchPage](t, s.do("GET", "/api/v1/comparisons/"+b.ID+"/resources?limit=200", ""))
	for _, m := range all.Matches {
		if m.LeftAddress == "aws_iam_role.debug" {
			t.Fatal("comparison B lists comparison A's instance")
		}
	}
}

func TestThousandResourceComparisonOverHTTP(t *testing.T) {
	s := newCompareServer(t, true)
	p := s.add("network")
	start := time.Now()
	doc := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), `[{"kind":"substitute","left":"dev","right":"prod"}]`)
	if doc.Summary.Managed.Total != 1000 || doc.Summary.Managed.Different != 200 {
		t.Fatalf("%+v", doc.Summary.Managed)
	}
	for offset := 0; offset < 1000; offset += 200 {
		page := decode[matchPage](t, s.do("GET", fmt.Sprintf("/api/v1/comparisons/%s/resources?offset=%d&limit=200", doc.ID, offset), ""))
		if len(page.Matches) != 200 || page.Total != 1000 {
			t.Fatalf("page at %d: %d", offset, len(page.Matches))
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("1,000-resource comparison over HTTP took %s", elapsed)
	}
}

// TestComparisonGolden pins the comparison, resource page and detail responses the UI
// consumes. Random IDs, times and the temporary root are replaced with fixed values.
func TestComparisonGolden(t *testing.T) {
	s := newCompareServer(t, false)
	p := s.add("network")
	doc := s.compare(target(p.ID, "dev"), target(p.ID, "prod"), `[{"kind":"substitute","left":"dev","right":"prod"},{"kind":"ignore","path":"region"},{"kind":"map","left":"module.legacy","right":"module.app"}]`)
	get := func(path string) any {
		var v any
		if err := json.Unmarshal(s.do("GET", "/api/v1/comparisons/"+doc.ID+path, "").Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	page := decode[matchPage](t, s.do("GET", "/api/v1/comparisons/"+doc.ID+"/resources?limit=200", ""))
	id := func(address string) string {
		for _, m := range page.Matches {
			if m.LeftAddress == address || m.RightAddress == address {
				return m.ID
			}
		}
		t.Fatalf("no %s", address)
		return ""
	}
	out := map[string]any{
		"comparison": get(""),
		"page":       get("/resources?limit=200"),
		"bucket":     get("/resources/" + id("aws_s3_bucket.logs")),
		"database":   get("/resources/" + id("aws_db_instance.main")),
		"web":        get("/resources/" + id("module.app.aws_instance.web[2]")),
	}
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	// The root appears JSON-encoded: on Windows its backslashes are escaped.
	root, err := json.Marshal(filepath.Join(s.dir, "network"))
	if err != nil {
		t.Fatal(err)
	}
	text = strings.NewReplacer(
		doc.ID, strings.Repeat("c", 32),
		p.ID, strings.Repeat("a", 32),
		doc.Left.ContextID, strings.Repeat("1", 64),
		doc.Right.ContextID, strings.Repeat("2", 64),
		strings.Trim(string(root), `"`), "/synthetic/network",
	).Replace(text)
	text = regexp.MustCompile(`"20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z"`).ReplaceAllString(text, `"2026-11-06T10:00:00Z"`)
	path := filepath.Join("..", "..", "fixtures", "compare", "dev-prod.golden.json")
	if *updateGolden {
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
		t.Fatalf("comparison response differs from golden; rerun with -update and review the diff\n%s", text)
	}
}
