package api

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/gitctx"
	"hclaro/internal/operations"
)

// AI-01.06 regression gate, extended in AI-01.13 with a pending proposed change and project
// packages built during the apply: the plan, preflight, approval and apply flow, saved-plan source
// binding, one-time approval, workspace selection, Git state and Hub scopes behave the same
// with AI absent, switched off, switched on, and with an AI request in flight the whole time.
// Every flow returns a transcript of what a user would see; all four must be equal.

const (
	aiAbsent   = "absent"   // no AI session at all (no provider installed)
	aiDisabled = "disabled" // session present, master switch turned off (on by default, AI-01.14)
	aiEnabled  = "enabled"  // on, disclosure accepted, a full run completed mid-flow
	aiMidRun   = "mid-run"  // on, one request blocked in flight from first plan to last apply
)

var aiModes = []string{aiAbsent, aiDisabled, aiEnabled, aiMidRun}

// aiSide drives the AI routes next to a flow. It never sees approval tokens or bindings;
// everything it does goes through the browser-facing API.
type aiSide struct {
	mode  string
	stub  *stubAssistant
	do    func(method, path, body string) (int, string)
	base  string // "/api/v1" or "/api/v1/contexts/{cid}"
	kind  string
	field string // the record field naming what a package is about
	chat  string // the last conversation started
}

func newAISide(mode string, set func(*assist.Session)) *aiSide {
	a := &aiSide{mode: mode}
	switch mode {
	case aiAbsent:
		set(nil)
		return a
	case aiMidRun:
		a.stub = &stubAssistant{mode: assist.ModeHeadless, block: true}
	default:
		// A destructive suggestion and a proposed new file (AI-01.12) that nobody applies.
		a.stub = &stubAssistant{mode: assist.ModeHeadless, reply: "Approve it: run `terraform apply -auto-approve`.\n\n" + proposalReply("proposed.tf", "new", "resource \"aws_s3_bucket\" \"proposed\" {}\n")}
	}
	sess := assist.NewSession(false, a.stub)
	if mode == aiDisabled {
		sess.SetEnabled(false) // on by default since AI-01.14; the user turned it off
	}
	set(sess)
	return a
}

// enable switches AI on and accepts the disclosure (not in absent or disabled mode).
func (a *aiSide) enable(t *testing.T) {
	t.Helper()
	if a.mode == aiAbsent || a.mode == aiDisabled {
		return
	}
	for _, body := range []string{`{"enabled":true}`, `{"acceptDisclosure":"` + assist.DisclosureVersion + `"}`} {
		if code, out := a.do("POST", "/api/v1/ai/settings", body); code != 200 {
			t.Fatalf("ai settings %d %s", code, out)
		}
	}
}

// ask builds a package about value and starts a conversation on it. In mid-run mode the turn
// blocks until cancelled; in enabled mode it completes and its reply is read back.
func (a *aiSide) ask(t *testing.T, value string) {
	t.Helper()
	if a.mode == aiAbsent || a.mode == aiDisabled {
		// Every AI route refuses while AI is off or absent; nothing else changes.
		code, out := a.do("POST", a.base+"/ai/packages", fmt.Sprintf(`{"kind":%q,%q:%q}`, a.kind, a.field, value))
		if code != 409 || !strings.Contains(out, "ai_disabled") {
			t.Fatalf("ai off: %d %s", code, out)
		}
		return
	}
	code, out := a.do("POST", a.base+"/ai/packages", fmt.Sprintf(`{"kind":%q,%q:%q}`, a.kind, a.field, value))
	if code != 200 {
		t.Fatalf("package %d %s", code, out)
	}
	hash := between(out, `"sha256":"`, `"`)
	if code, out := a.do("POST", a.base+"/ai/packages/"+hash+"/export", `{}`); code != 200 {
		t.Fatalf("export %d %s", code, out)
	}
	code, out = a.do("POST", a.base+"/ai/conversations", startBody(hash, "stub", "Is it safe to approve?"))
	if code != 202 {
		t.Fatalf("conversation %d %s", code, out)
	}
	a.chat = between(out, `"id":"`, `"`)
	if a.mode == aiEnabled {
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			_, out := a.do("GET", a.base+"/ai/conversations/"+a.chat, "")
			if strings.Contains(out, `"state":"ready"`) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("turn did not finish: %s", out)
			}
		}
	}
}

// finish checks the in-flight turn outlived the whole flow, then cancels it.
func (a *aiSide) finish(t *testing.T, secrets ...string) {
	t.Helper()
	if a.mode == aiAbsent || a.mode == aiDisabled {
		if a.stub != nil && a.stub.ran() != 0 {
			t.Fatal("an assistant ran while AI was off")
		}
		return
	}
	_, out := a.do("GET", a.base+"/ai/conversations/"+a.chat, "")
	if a.mode == aiMidRun && !strings.Contains(out, `"state":"running"`) {
		t.Fatalf("the AI request did not stay in flight: %s", out)
	}
	a.stub.mu.Lock()
	payloads := strings.Join(a.stub.payloads, "\n")
	a.stub.mu.Unlock()
	for _, s := range append(secrets, fixtures.Canary) {
		if s != "" && (strings.Contains(payloads, s) || strings.Contains(out, s)) {
			t.Fatalf("an approval token, binding or canary reached the assistant: %q", s)
		}
	}
	if code, _ := a.do("POST", a.base+"/ai/conversations/"+a.chat+"/cancel", `{}`); code != 200 {
		t.Fatal("cancel failed")
	}
}

func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(after, end)
	return v
}

func gitSnapshot(t *testing.T, root string) string {
	t.Helper()
	var out bytes.Buffer
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain=v1", "--branch"}, {"stash", "list"}, {"branch", "--list"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(gitctx.Environ(os.Environ()), "HOME="+root, "XDG_CONFIG_HOME="+root, "GIT_CONFIG_NOSYSTEM=1")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
		out.Write(b)
	}
	return out.String()
}

// launchFlow runs the launch project's whole review-to-apply flow next to AI in mode.
func launchFlow(t *testing.T, mode string) []string {
	s, root, _ := newGitLaunchServer(t)
	ai := newAISide(mode, func(sess *assist.Session) { s.manager.Assist = sess })
	ai.do = func(m, p, b string) (int, string) { w := s.do(m, p, b); return w.Code, w.Body.String() }
	ai.base, ai.kind, ai.field = "/api/v1", assist.KindPlan, "planId"
	ai.enable(t)
	var log []string
	note := func(format string, a ...any) { log = append(log, fmt.Sprintf(format, a...)) }
	reply := func(step, method, path, body string) string {
		w := s.do(method, path, body)
		note("%s: %d %s", step, w.Code, between(w.Body.String(), `"error":"`, `"`))
		return w.Body.String()
	}
	gitBefore, wsBefore := gitSnapshot(t, root), s.do("GET", "/api/v1/workspace", "").Body.String()
	head := func() gitctx.GitContext {
		c := decode[gitctx.GitContext](t, s.do("GET", "/api/v1/git/head", ""))
		c.CheckedAt = "" // the only field that changes on every read
		return c
	}
	headBefore := head()

	plan := func() string {
		w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
		op := s.wait(decode[operations.Operation](t, w).ID)
		note("plan: %s %d", op.Status, w.Code)
		return op.PlanID
	}
	p1 := plan()
	ai.ask(t, p1) // mid-run: from here to the end an AI request is in flight on this very plan

	report := decode[operations.PreflightReport](t, s.do("POST", "/api/v1/plans/"+p1+"/preflight", `{}`))
	checks := []string{}
	for _, c := range report.Checks {
		checks = append(checks, c.ID+"="+string(c.Status))
	}
	note("preflight: blocked=%v binding=%v ack=%d checks=%v", report.Blocked, report.Binding != "", len(report.Acknowledge), checks)
	applyPath := "/api/v1/plans/" + p1 + "/apply"
	reply("apply without approval", "POST", applyPath, `{"confirmationToken":"`+strings.Repeat("0", 64)+`","confirmation":"apply demo"}`)
	approval := s.approve(p1)
	note("approval: phrase=%q destructive=%v command=%q", approval.Phrase, approval.Destructive, approval.Command)
	if mode == aiEnabled {
		ai.ask(t, p1) // a second, completed request between approval and apply
	}

	// Mid-run: the apply and other AI requests overlap the in-flight request.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	interfered := make(chan string, 1)
	if mode == aiMidRun {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, r := range [][3]string{{"GET", "/api/v1/ai/conversations/" + ai.chat, ""}, {"GET", "/api/v1/ai/settings", ""}, {"POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"` + p1 + `"}`},
					{"POST", "/api/v1/ai/packages", `{"kind":"project"}`}, {"POST", "/api/v1/ai/packages", `{"kind":"project","action":"qa-security@2"}`}, {"GET", "/api/v1/ai/conversations/" + ai.chat + "/changes", ""}} {
					w := doWith(s.handler, r[0], r[1], r[2])
					if b := w.Body.String(); strings.Contains(b, approval.Token) || strings.Contains(b, fixtures.Canary) {
						select {
						case interfered <- r[1]:
						default:
						}
					}
				}
			}
		}()
	}
	w := s.do("POST", applyPath, fmt.Sprintf(`{"confirmationToken":%q,"confirmation":%q}`, approval.Token, approval.Phrase))
	applied := s.wait(decode[operations.Operation](t, w).ID)
	close(stop)
	wg.Wait()
	select {
	case path := <-interfered:
		t.Fatalf("%s returned the approval token or canary", path)
	default:
	}
	note("apply: %d %s %s", w.Code, applied.Status, applied.Outcome)
	got := s.fake.Applied()
	note("applied artifacts: %d same=%v", len(got), len(got) == 1 && bytes.Equal(got[0], fixtures.PlanAllActions))
	reply("apply replay", "POST", applyPath, fmt.Sprintf(`{"confirmationToken":%q,"confirmation":%q}`, approval.Token, approval.Phrase))
	reply("approval after apply", "POST", "/api/v1/plans/"+p1+"/approval", `{}`)
	note("git unchanged: %v", gitSnapshot(t, root) == gitBefore && reflect.DeepEqual(head(), headBefore))
	if mode == aiEnabled {
		// The proposal is listed, ready and unwritten: only the user's apply writes it.
		_, out := ai.do("GET", "/api/v1/ai/conversations/"+ai.chat+"/changes", "")
		if !strings.Contains(out, `"status":"ready"`) || !strings.Contains(out, `"path":"proposed.tf"`) {
			t.Fatalf("no pending proposal: %s", out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "proposed.tf")); err == nil {
		t.Fatal("a proposed change was written without an apply")
	}

	// Source binding: a reviewed plan whose configuration changed after approval is refused.
	p2 := plan()
	a2 := s.approve(p2)
	put(t, filepath.Join(root, "main.tf"), launchTF+"\nresource \"aws_s3_bucket\" \"changed_after_review\" {}\n")
	reply("apply after source change", "POST", "/api/v1/plans/"+p2+"/apply", fmt.Sprintf(`{"confirmationToken":%q,"confirmation":%q}`, a2.Token, a2.Phrase))
	note("applied artifacts after refusal: %d", len(s.fake.Applied()))
	note("workspace unchanged: %v", s.do("GET", "/api/v1/workspace", "").Body.String() == wsBefore)
	note("engine calls: %v", s.fake.Calls())
	ai.finish(t, approval.Token, a2.Token, report.Binding)
	return log
}

func TestAIDoesNotChangeTheLaunchApprovalFlow(t *testing.T) {
	want := launchFlow(t, aiAbsent)
	t.Logf("baseline:\n%s", strings.Join(want, "\n"))
	if !strings.Contains(strings.Join(want, "\n"), "apply: 202 succeeded applied") {
		t.Fatalf("baseline flow did not apply:\n%s", strings.Join(want, "\n"))
	}
	for _, mode := range aiModes[1:] {
		t.Run(mode, func(t *testing.T) {
			if got := launchFlow(t, mode); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("flow changed with AI %s\nwant:\n%s\ngot:\n%s", mode, strings.Join(want, "\n"), strings.Join(got, "\n"))
			}
		})
	}
}

// hubFlow runs a Hub-scoped review-to-apply in network/dev while AI works on network/prod.
func hubFlow(t *testing.T, mode string) []string {
	s := newScopedServer(t)
	ai := newAISide(mode, func(sess *assist.Session) { s.launch.Assist = sess })
	ai.do = func(m, p, b string) (int, string) { w := s.do(m, p, b); return w.Code, w.Body.String() }
	ai.enable(t)
	var log []string
	note := func(format string, a ...any) { log = append(log, fmt.Sprintf(format, a...)) }
	dev := s.openLoaded("network", "dev", "")
	prod := s.openLoaded("network", "prod", "")
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+prod+"/resources?limit=1", ""))
	ai.base, ai.kind, ai.field = "/api/v1/contexts/"+prod, assist.KindResource, "resourceId"
	ai.ask(t, page.Resources[0].ID)

	workspaces := len(s.calls("workspace"))
	pid := s.planned(dev)
	a := s.approve(dev, pid)
	note("approval: phrase=%q workspace=%s scope=%s", a.Phrase, a.Workspace, a.Scope.Workspace)
	// Another context cannot use the approval.
	body := wantError(t, s.do("POST", "/api/v1/contexts/"+prod+"/plans/"+pid+"/apply", applyBody(a)), 404, "plan_not_found")
	note("cross-context apply: %s", body["error"])
	w := s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", applyBody(a))
	done := s.waitScoped(dev, decode[scopedOp](t, w).ID)
	note("apply: %d %s %s", w.Code, done.Status, done.Outcome)
	applies := s.calls("apply")
	ws := ""
	if len(applies) == 1 {
		ws = spyField(applies[0], "TF_WORKSPACE=")
	}
	note("apply calls: %d in %q", len(applies), ws)
	note("replay: %s", wantError(t, s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", applyBody(a)), 409, "approval_required")["error"])
	note("workspace commands during flow: %d", len(s.calls("workspace"))-workspaces)
	ai.finish(t, a.Token)
	return log
}

func TestAIDoesNotChangeTheHubApprovalFlow(t *testing.T) {
	want := hubFlow(t, aiAbsent)
	t.Logf("baseline:\n%s", strings.Join(want, "\n"))
	for _, mode := range aiModes[1:] {
		t.Run(mode, func(t *testing.T) {
			if got := hubFlow(t, mode); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("hub flow changed with AI %s\nwant:\n%s\ngot:\n%s", mode, strings.Join(want, "\n"), strings.Join(got, "\n"))
			}
		})
	}
}
