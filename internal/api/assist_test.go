package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/operations"
)

// AI-01.03 handoff routes. Everything runs against enginetest.Fake or the synthetic engine in
// temporary roots; no assistant, backend or provider is contacted.

type builtPackage struct {
	assist.Package
	Disclosure assist.Disclosure `json:"disclosure"`
}

type exported struct {
	Kind, SHA256, Markdown, Filename string
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newAssistServer(t *testing.T, fake *enginetest.Fake) (*planServer, string) {
	t.Helper()
	s, root := newImportServer(t, fake)
	s.svc.Assist = assist.NewSession(false)
	return s, root
}

func doWith(h http.Handler, method, path, body string, opts ...opt) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+session)
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Hclaro-CSRF", csrf)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func (s *planServer) enableAI() {
	s.t.Helper()
	if st := turnOnAI(s.t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) }); !st.Enabled {
		s.t.Fatalf("%+v", st)
	}
}

// build returns the package and checks that its hash is the hash of its bytes.
func build(t *testing.T, w *httptest.ResponseRecorder) builtPackage {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	p := decode[builtPackage](t, w)
	if p.SHA256 != sum(p.Markdown) || p.Markdown == "" || p.Disclosure.Version != assist.DisclosureVersion {
		t.Fatalf("package %+v", p)
	}
	return p
}

// exportSame checks that the exported bytes are exactly the previewed bytes.
func exportSame(t *testing.T, w *httptest.ResponseRecorder, p builtPackage) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body)
	}
	e := decode[exported](t, w)
	if e.Markdown != p.Markdown || e.SHA256 != p.SHA256 || sum(e.Markdown) != p.SHA256 || e.Filename != "hclaro-"+p.Kind+"-"+p.SHA256[:12]+".md" {
		t.Fatalf("exported %+v differs from preview %s", e, p.SHA256)
	}
}

func TestAIEnabledByDefault(t *testing.T) {
	// No session object at all: settings report off and every route refuses.
	s, _ := newImportServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	if got := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", "")); got.Enabled || got.Handoff {
		t.Fatalf("%+v", got)
	}
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"enabled":true}`), 409, "ai_unavailable")
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan"}`), 409, "ai_disabled")

	// A session starts on (OD-AI-13), shares nothing until a click and accepts nothing yet.
	s.svc.Assist = assist.NewSession(false)
	got := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", ""))
	if !got.Enabled || !got.Handoff || got.Headless || got.Accepted || got.Disclosure.Version != assist.DisclosureVersion {
		t.Fatalf("%+v", got)
	}
	// Turned off, every route refuses.
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	planID := s.plan()
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`), 409, "ai_disabled")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+strings.Repeat("a", 64)+"/export", `{}`), 409, "ai_disabled")
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{}`), 400, "invalid_request")

	// Turning AI off again drops packages already built.
	s.enableAI()
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`))
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	s.enableAI()
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 404, "package_not_found")
}

// AI-01.14: the switch and the assistant are saved in ai.json and survive a restart; only the
// two adapters can be chosen; a failed write changes nothing.
func TestAISettingsPersistOverHTTP(t *testing.T) {
	dir := t.TempDir()
	start := func() *planServer {
		s, _ := newImportServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
		s.svc.Assist = assist.NewSession(false, &stubAssistant{id: "claude", mode: assist.ModeHeadless}, &stubAssistant{id: "codex", mode: assist.ModeHandoff})
		s.svc.Assist.UsePrefs(dir)
		return s
	}
	s := start()
	s.enableAI()
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"defaultProvider":"copilot"}`), 409, "provider_unavailable")
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"defaultProvider":"/bin/sh"}`), 409, "provider_unavailable")
	if st := decode[assist.Settings](t, s.do("POST", "/api/v1/ai/settings", `{"defaultProvider":"codex"}`)); st.DefaultProvider != "codex" || st.Headless {
		t.Fatalf("%+v", st)
	}
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	next := start()
	if st := decode[assist.Settings](t, next.do("GET", "/api/v1/ai/settings", "")); st.Enabled || st.Handoff {
		t.Fatalf("off did not survive a restart: %+v", st)
	}
	wantError(t, next.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+next.plan()+`"}`), 409, "ai_disabled")
	if st := turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return next.do(m, p, b) }); st.DefaultProvider != "codex" {
		t.Fatalf("the assistant did not survive a restart: %+v", st)
	}
	if err := os.Remove(filepath.Join(dir, "ai.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "ai.json"), 0o700); err != nil { // writes now fail
		t.Fatal(err)
	}
	wantError(t, next.do("POST", "/api/v1/ai/settings", `{"enabled":false}`), 500, "ai_settings_not_saved")
	if !next.svc.Assist.Enabled() {
		t.Fatal("a failed write turned AI off")
	}
}

func TestAIRoutesNeedSessionOriginAndCSRF(t *testing.T) {
	s, _ := newAssistServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	s.enableAI()
	planID := s.plan()
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`))
	chat := strings.Repeat("a", 32)
	routes := map[string]string{
		"/api/v1/ai/settings":                            `{"enabled":false}`,
		"/api/v1/ai/detect":                              `{}`,
		"/api/v1/ai/packages":                            `{"kind":"plan","planId":"` + planID + `"}`,
		"/api/v1/ai/packages/" + p.SHA256 + "/export":    `{}`,
		"/api/v1/ai/conversations":                       `{"packageSha256":"` + p.SHA256 + `","disclosureVersion":"` + assist.DisclosureVersion + `"}`,
		"/api/v1/ai/conversations/" + chat + "/messages": `{"text":"hi"}`,
		"/api/v1/ai/conversations/" + chat + "/cancel":   `{}`,
		"/api/v1/ai/conversations/" + chat + "/close":    `{}`,
	}
	for path, body := range routes {
		for _, tc := range []struct {
			name string
			opts []opt
			code int
		}{
			{"no session", []opt{without("Authorization")}, 401},
			{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
			{"wrong csrf", []opt{with("X-Hclaro-CSRF", strings.Repeat("c", 64))}, 403},
			{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
			{"no origin", []opt{without("Origin")}, 403},
			{"cross-site fetch", []opt{with("Sec-Fetch-Site", "cross-site")}, 403},
			{"form post", []opt{with("Content-Type", "text/plain")}, 415},
			{"wrong host", []opt{func(r *http.Request) { r.Host = "evil.example" }}, 403},
		} {
			w := doWith(s.handler, "POST", path, body, tc.opts...)
			if w.Code != tc.code || strings.Contains(w.Body.String(), "# ") {
				t.Fatalf("%s %s: %d %s", tc.name, path, w.Code, w.Body)
			}
		}
	}
	if w := doWith(s.handler, "GET", "/api/v1/ai/settings", "", without("Authorization"), without("Content-Type")); w.Code != 401 {
		t.Fatalf("GET settings without session: %d", w.Code)
	}
	// Refused requests changed nothing: AI is still on and the package still exports.
	if !s.svc.Assist.Enabled() {
		t.Fatal("a refused request turned AI off")
	}
	if w := doWith(s.handler, "GET", "/api/v1/ai/conversations/"+chat, "", without("Authorization")); w.Code != 401 {
		t.Fatalf("GET conversation without session: %d", w.Code)
	}
	exportSame(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), p)
}

// Destroy, replace and multi-change plans: the package names each action, and copy and
// download bytes equal the preview bytes.
func TestAIPlanHandoff(t *testing.T) {
	s, root := newAssistServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	s.enableAI()
	planID := s.plan()
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`))
	for _, want := range []string{"delete", "replace", "create", "update"} {
		if !strings.Contains(p.Markdown, want) {
			t.Fatalf("plan package lacks %q:\n%s", want, p.Markdown)
		}
	}
	if strings.Contains(p.Markdown, root) || p.Kind != assist.KindPlan {
		t.Fatal("package names the project root")
	}
	exportSame(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), p)
	// The same package is rebuilt byte for byte.
	if again := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`)); again.SHA256 != p.SHA256 {
		t.Fatal("package is not deterministic")
	}

	// Unknown, malformed and wrong-kind requests.
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+strings.Repeat("0", 64)+"/export", `{}`), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+strings.ToUpper(p.SHA256)+"/export", `{}`), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+strings.Repeat("0", 32)+`"}`), 404, "plan_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"../x"}`), 404, "plan_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"state"}`), 400, "invalid_kind")
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","path":"/etc"}`), 400, "invalid_request")

	// A stale context: the configuration changed after the plan. Export and rebuild refuse.
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_instance\" \"web\" { ami = \"x\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 409, "context_changed")
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`), 409, "context_changed")
}

func TestAIPackageExpires(t *testing.T) {
	s, _ := newAssistServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	now := time.Now()
	s.svc.Assist.Now = func() time.Time { return now }
	s.enableAI()
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	now = now.Add(assist.PackageTTL + time.Second)
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 404, "package_not_found")
}

func TestAIResourceAndReload(t *testing.T) {
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions}
	s, store := newDriftServer(t, fake)
	s.svc.Assist = assist.NewSession(false)
	s.enableAI()
	page := decode[struct {
		Resources []struct{ ID, Address string }
	}](t, s.do("GET", "/api/v1/resources?limit=1", ""))
	if len(page.Resources) != 1 {
		t.Fatal("fixture has no resources")
	}
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"resource","resourceId":"`+page.Resources[0].ID+`"}`))
	// Sections this task does not collect are left out, not claimed empty.
	if !strings.Contains(p.Markdown, page.Resources[0].Address) || p.Kind != assist.KindResource || strings.Contains(p.Markdown, "## Variables") || strings.Contains(p.Markdown, "## Dependencies") || strings.Contains(p.Markdown, "## Configuration") {
		t.Fatalf("%s", p.Markdown)
	}
	exportSame(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), p)
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"resource","resourceId":"nope"}`), 404, "resource_not_found")

	// A new snapshot of the workspace makes the package stale.
	time.Sleep(1100 * time.Millisecond) // CapturedAt has one-second resolution
	store.Load(context.Background(), fake, "terraform", nil)
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 409, "context_changed")
}

// Import with missing configuration builds a package; a value that still names the project
// root is blocked with field paths only, no text and no hash.
func TestAIImportHandoffAndBlocked(t *testing.T) {
	s, root := newAssistServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanImport})
	s.enableAI()
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"import","import":{"address":"aws_s3_bucket.missing","id":"bucket-x","file":"imports.tf"}}`))
	if !strings.Contains(p.Markdown, "aws_s3_bucket.missing") || !strings.Contains(p.Markdown, "missing") {
		t.Fatalf("%s", p.Markdown)
	}
	exportSame(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), p)
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"import"}`), 400, "invalid_request")

	w := s.do("POST", "/api/v1/ai/packages", fmt.Sprintf(`{"kind":"import","import":{"address":"aws_s3_bucket.x","id":%q,"file":"imports.tf"}}`, root+"/bucket"))
	if w.Code != 422 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := decode[map[string]any](t, w)
	if body["error"] != "package_blocked" || body["markdown"] != nil || body["sha256"] != nil || strings.Contains(w.Body.String(), root) || !strings.Contains(w.Body.String(), "import.id") {
		t.Fatalf("blocked response %s", w.Body)
	}
}

func TestAIDriftHandoff(t *testing.T) {
	fake := &enginetest.Fake{Lines: lines(fixtures.DriftLog), PlanJSON: fixtures.DriftMixed}
	s, _ := newDriftServer(t, fake)
	s.svc.Assist = assist.NewSession(false)
	s.enableAI()
	_, scan := startScan(t, s)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"drift","scanId":"`+scan.ID+`"}`))
	for _, r := range scan.Resources {
		if !strings.Contains(p.Markdown, r.Address) {
			t.Fatalf("drift package lacks %s", r.Address)
		}
	}
	exportSame(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), p)
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"drift","scanId":"`+strings.Repeat("0", 32)+`"}`), 404, "scan_not_found")
}

// Hub: packages are bound to their read context. Another context, the launch project and a
// reloaded context refuse the hash.
func TestAIHubHandoffStaysInItsContext(t *testing.T) {
	s := newScopedServer(t)
	s.svc.Assist = assist.NewSession(false)
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("network", "prod", "")
	done := s.waitScoped(a, s.plan(a).ID)
	p := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"plan","planId":"`+done.PlanID+`"}`))
	if strings.Contains(p.Markdown, s.roots["network"]) {
		t.Fatal("package names the project root")
	}
	exportSame(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+p.SHA256+"/export", `{}`), p)
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/packages/"+p.SHA256+"/export", `{}`), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/packages", `{"kind":"plan","planId":"`+done.PlanID+`"}`), 404, "plan_not_found")

	// Resource and drift of the Hub workspace.
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+b+"/resources?limit=1", ""))
	if len(page.Resources) == 0 {
		t.Fatal("hub fixture has no resources")
	}
	{
		r := build(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/packages", `{"kind":"resource","resourceId":"`+page.Resources[0].ID+`"}`))
		exportSame(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/packages/"+r.SHA256+"/export", `{}`), r)
	}
	scan := s.scan(a)
	if scan.Status != operations.ScanCompleted && scan.Status != operations.ScanPartial {
		t.Fatalf("hub scan %s", scan.Status)
	}
	{
		d := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"drift","scanId":"`+scan.ID+`"}`))
		exportSame(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+d.SHA256+"/export", `{}`), d)
	}

	imp := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"import","import":{"address":"aws_s3_bucket.missing","id":"bucket-x","file":"imports.tf"}}`))
	exportSame(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+imp.SHA256+"/export", `{}`), imp)

	// CSRF guards the Hub routes too.
	if w := s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+p.SHA256+"/export", `{}`, without("X-Hclaro-CSRF")); w.Code != 403 {
		t.Fatalf("export without CSRF: %d", w.Code)
	}
	// Reopening replaces the context: the old one, and its packages, are gone.
	s.openLoaded("network", "dev", "")
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+p.SHA256+"/export", `{}`), 404, "context_closed")
}

// Each Hclaro process has its own session token and its own in-memory package store: a
// hash built in one is unknown in another, switching AI off in one leaves the other on, and
// a restart (a new session) starts with the saved switch and no packages.
func TestAIPackagesBelongToTheirProcess(t *testing.T) {
	a, _ := newAssistServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	b, _ := newAssistServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	a.enableAI()
	b.enableAI()
	p := build(t, a.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+a.plan()+`"}`))
	wantError(t, b.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 404, "package_not_found")
	a.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	if !b.svc.Assist.Enabled() {
		t.Fatal("switching AI off in one process changed another")
	}
	b.svc.Assist = assist.NewSession(false) // restart, no saved settings: on (OD-AI-13)
	if !b.svc.Assist.Enabled() {
		t.Fatal("AI is off after a restart without saved settings")
	}
	b.enableAI()
	wantError(t, b.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 404, "package_not_found")
}

func TestAIHubRoutesNeedSessionOriginAndCSRF(t *testing.T) {
	s := newScopedServer(t)
	s.svc.Assist = assist.NewSession(false)
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	cid := s.openLoaded("network", "dev", "")
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+cid+"/resources?limit=1", ""))
	body := `{"kind":"resource","resourceId":"` + page.Resources[0].ID + `"}`
	p := build(t, s.do("POST", "/api/v1/contexts/"+cid+"/ai/packages", body))
	routes := map[string]string{"/api/v1/contexts/" + cid + "/ai/packages": body, "/api/v1/contexts/" + cid + "/ai/packages/" + p.SHA256 + "/export": `{}`,
		"/api/v1/contexts/" + cid + "/ai/conversations":                                          `{"packageSha256":"` + p.SHA256 + `","disclosureVersion":"` + assist.DisclosureVersion + `"}`,
		"/api/v1/contexts/" + cid + "/ai/conversations/" + strings.Repeat("a", 32) + "/messages": `{"text":"hi"}`,
		"/api/v1/contexts/" + cid + "/ai/conversations/" + strings.Repeat("a", 32) + "/close":    `{}`}
	for path, b := range routes {
		for _, tc := range []struct {
			name string
			opts []opt
			code int
		}{
			{"no session", []opt{without("Authorization")}, 401},
			{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
			{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
			{"no origin", []opt{without("Origin")}, 403},
			{"cross-site fetch", []opt{with("Sec-Fetch-Site", "cross-site")}, 403},
			{"form post", []opt{with("Content-Type", "text/plain")}, 415},
			{"wrong host", []opt{func(r *http.Request) { r.Host = "evil.example" }}, 403},
		} {
			if w := s.do("POST", path, b, tc.opts...); w.Code != tc.code || strings.Contains(w.Body.String(), "# ") {
				t.Fatalf("%s %s: %d %s", tc.name, path, w.Code, w.Body)
			}
		}
	}
	// Malformed context IDs and hashes.
	wantError(t, s.do("POST", "/api/v1/contexts/"+strings.Repeat("f", 32)+"/ai/packages", body), 404, "context_closed")
	wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/ai/packages/"+p.SHA256[:63]+"/export", `{}`), 404, "package_not_found")
	exportSame(t, s.do("POST", "/api/v1/contexts/"+cid+"/ai/packages/"+p.SHA256+"/export", `{}`), p)
}

// A scope evicted while its read context stays open drops its scans; their packages are stale.
func TestAIHubEvictedScopePackageIsStale(t *testing.T) {
	s := newScopedServer(t)
	s.hub.MaxScopes = 1
	s.svc.Assist = assist.NewSession(false)
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("network", "prod", "")
	scan := s.scan(a)
	d := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"drift","scanId":"`+scan.ID+`"}`))
	exportSame(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+d.SHA256+"/export", `{}`), d)
	s.scan(b) // takes the only scope slot from a
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+d.SHA256+"/export", `{}`), 409, "context_changed")
}
