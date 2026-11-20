package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
)

// AI-01.12 over HTTP: proposals are listed with Hclaro's diff, written only on apply, under the
// workspace lock, and a written file makes the workspace's saved plans stale. Nothing runs the
// engine.

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func proposalReply(path, base, text string) string {
	return "Rename the bucket.\n```hclaro-change path=" + path + " base=" + base + "\n" + text + "```\n"
}

func TestAIProposedChangeOverHTTP(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	s, root := newImportServer(t, fake)
	const old = "resource \"aws_s3_bucket\" \"legacy_logs\" {}\n" // newImportServer's main.tf
	const next = "resource \"aws_s3_bucket\" \"legacy_logs\" {\n  bucket = \"logs\"\n}\n"
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: proposalReply("main.tf", sha(old), next)}
	s.svc.Assist = assist.NewSession(false, stub)
	s.svc.Assist.Store = assist.OpenStore(t.TempDir(), time.Now())
	planID := s.plan()

	s.svc.Assist.SetEnabled(false) // on by default (AI-01.14); these checks start from off
	wantError(t, s.do("GET", "/api/v1/ai/conversations/"+strings.Repeat("a", 32)+"/changes", ""), 409, "ai_disabled")
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	w := s.do("POST", "/api/v1/ai/conversations", chatBody("Give the bucket a name."))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, s.do, "/api/v1", id)
	base := "/api/v1/ai/conversations/" + id + "/changes"

	list := decode[struct{ Changes []assist.ChangeView }](t, s.do("GET", base, ""))
	if len(list.Changes) != 1 || list.Changes[0].Status != assist.ChangeReady || !strings.Contains(list.Changes[0].Diff, "+  bucket = \"logs\"") {
		t.Fatalf("%+v", list)
	}
	v := list.Changes[0]
	apply := `{"turn":1,"index":0,"baseSha256":"` + v.BaseSHA256 + `","newSha256":"` + v.NewSHA256 + `"}`
	if v.Turn != 1 {
		t.Fatalf("turn %d", v.Turn)
	}

	// Without the CSRF token nothing is written.
	r := httptest.NewRequest("POST", "http://"+host+base+"/apply", strings.NewReader(apply))
	r.Header.Set("Authorization", "Bearer "+session)
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	s.handler.ServeHTTP(rw, r)
	if rw.Code != 403 {
		t.Fatalf("no CSRF: %d %s", rw.Code, rw.Body)
	}

	// While a plan runs in the workspace, apply is refused and writes nothing.
	fake.Block = true
	op := decode[operations.Operation](t, s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`))
	wantError(t, s.do("POST", base+"/apply", apply), 409, assist.RefuseChangeBusy)
	s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`)
	s.wait(op.ID)
	fake.Block = false
	if b, _ := os.ReadFile(filepath.Join(root, "main.tf")); string(b) != old {
		t.Fatal("written while a plan ran")
	}

	calls := len(fake.Calls())
	wantError(t, s.do("POST", base+"/apply", `{"turn":1,"index":0,"baseSha256":"`+v.BaseSHA256+`","newSha256":"`+sha("x")+`"}`), 409, assist.RefuseChangeReviewed)
	wantError(t, s.do("POST", base+"/apply", `{"turn":0,"index":0}`), 404, assist.RefuseChangeNotFound)
	if w := s.do("POST", base+"/apply", apply); w.Code != 200 || decode[assist.ChangeView](t, w).Status != assist.ChangeApplied {
		t.Fatalf("apply %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "main.tf")); string(b) != next {
		t.Fatalf("written %q", b)
	}

	// The saved plan is stale through the existing configuration fingerprint, and the apply
	// gate refuses it.
	type planDetail struct {
		ContextChanged bool                   `json:"contextChanged"`
		Apply          operations.ApplyStatus `json:"apply"`
	}
	detail := decode[planDetail](t, s.do("GET", "/api/v1/plans/"+planID, ""))
	if !detail.ContextChanged || detail.Apply.Available {
		t.Fatalf("plan after the change: %+v", detail)
	}
	if w := s.do("POST", "/api/v1/plans/"+planID+"/preflight", `{}`); w.Code == 200 {
		if w := s.do("POST", "/api/v1/plans/"+planID+"/approval", `{"preflightBinding":"x","acknowledged":[]}`); w.Code == 200 {
			t.Fatal("a stale plan was approved")
		}
	}

	if w := s.do("POST", base+"/undo", `{"turn":1,"index":0}`); w.Code != 200 {
		t.Fatalf("undo %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "main.tf")); string(b) != old {
		t.Fatal("undo did not restore the file")
	}
	if got := fake.Calls()[calls:]; len(got) != 0 {
		t.Fatalf("a proposed change ran the engine: %v", got)
	}
	if stub.ran() != 1 {
		t.Fatal("deciding a change ran the assistant")
	}
}

func TestAIProposedChangeInHubWorkspace(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: proposalReply("hclaro_ai.tf", "new", "locals {\n  owner = \"net\"\n}\n")}
	s.svc.Assist = assist.NewSession(false, stub)
	s.svc.Assist.Store = assist.OpenStore(t.TempDir(), time.Now())
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("platform", "staging", "")
	w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations", chatBody("Add an owner local."))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	do := func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) }
	waitChat(t, do, "/api/v1/contexts/"+a, id)
	list := decode[struct{ Changes []assist.ChangeView }](t, s.do("GET", "/api/v1/contexts/"+a+"/ai/conversations/"+id+"/changes", ""))
	if len(list.Changes) != 1 || list.Changes[0].Status != assist.ChangeReady {
		t.Fatalf("%+v", list)
	}
	body := `{"turn":1,"index":0,"newSha256":"` + list.Changes[0].NewSHA256 + `"}`
	spied := func() string { b, _ := os.ReadFile(s.spy); return string(b) }
	before := spied()
	// Another workspace view cannot apply it.
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/conversations/"+id+"/changes/apply", body), 404, "conversation_not_found")
	if w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations/"+id+"/changes/apply", body); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got, err := os.ReadFile(filepath.Join(s.roots["network"], "hclaro_ai.tf")); err != nil || !strings.Contains(string(got), "owner") {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(s.roots["platform"], "hclaro_ai.tf")); err == nil {
		t.Fatal("written into another project")
	}
	// Creating the workspace's execution scope for the lock and writing run no engine command.
	if after := spied(); after != before {
		t.Fatalf("a proposed change ran the engine: %q", strings.TrimPrefix(after, before))
	}
}

// Over HTTP the new-file check asks the project's real Git: a name its .gitignore rules ignore
// is listed as invalid and cannot be applied; a name they do not ignore is ready.
func TestAIProposedNewFileIgnoredByGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	git := exec.Command("git", "init", "-q")
	git.Dir = root
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*_override.tf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reply := proposalReply("main_override.tf", "new", "locals {}\n") + proposalReply("extra.tf", "new", "locals {}\n")
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: reply}
	s.svc.Assist = assist.NewSession(false, stub)
	s.svc.Assist.Store = assist.OpenStore(t.TempDir(), time.Now())
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	w := s.do("POST", "/api/v1/ai/conversations", chatBody("Add files."))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, s.do, "/api/v1", id)
	base := "/api/v1/ai/conversations/" + id + "/changes"
	list := decode[struct{ Changes []assist.ChangeView }](t, s.do("GET", base, ""))
	if len(list.Changes) != 2 || list.Changes[0].Code != "change_ignored" || list.Changes[1].Status != assist.ChangeReady {
		t.Fatalf("%+v", list)
	}
	ignored := `{"turn":1,"index":0,"newSha256":"` + list.Changes[0].NewSHA256 + `"}`
	if w := s.do("POST", base+"/apply", ignored); w.Code == 200 {
		t.Fatalf("ignored file applied: %s", w.Body)
	}
	if _, err := os.Lstat(filepath.Join(root, "main_override.tf")); err == nil {
		t.Fatal("ignored file written")
	}
	ok := `{"turn":1,"index":1,"newSha256":"` + list.Changes[1].NewSHA256 + `"}`
	if w := s.do("POST", base+"/apply", ok); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
