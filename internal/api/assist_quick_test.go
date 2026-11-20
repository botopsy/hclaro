package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/internal/assist"
	"hclaro/internal/cost"
	"hclaro/internal/cost/costtest"
)

// AI-01.10 over HTTP: quick actions are project packages whose first turn is a qa-* prompt; the
// Cost Analysis action can attach the session's existing cost estimate, never starting one.

var quickActions = []string{"qa-describe@1", "qa-architecture@1", "qa-security@1", "qa-cost@1", "qa-issues@1", "qa-improve@1"}

type quickPackage struct {
	reviewPackage
	CostAvailable bool `json:"costAvailable"`
}

const costTitle = "Cost estimate (Infracost, from this session)"

func TestAIQuickActionsOverHTTP(t *testing.T) {
	s, runner, _ := newCostServer(t, false)
	stub := reviewReady(s)
	do := s.do

	for _, id := range quickActions {
		w := do("POST", "/api/v1/ai/packages", `{"kind":"project","action":"`+id+`"}`)
		build(t, w)
		p := decode[quickPackage](t, w)
		prompt, _ := assist.LookupPrompt(id)
		if p.Kind != assist.KindProject || p.Prompt != id || p.Review != assist.FirstTurn(p.Markdown, prompt) || p.CostAvailable || strings.Contains(p.Markdown, costTitle) {
			t.Fatalf("%s preview: %+v", id, p.Prompt)
		}
		w = do("POST", "/api/v1/ai/conversations", reviewBody(p.SHA256, id))
		if w.Code != 202 {
			t.Fatalf("%s: %d %s", id, w.Code, w.Body)
		}
		c := waitChat(t, do, "/api/v1", decode[assist.Conversation](t, w).ID)
		if c.State != assist.StateReady || c.Kind != assist.KindProject || c.Turns[0].Prompt != id || stub.last() != p.Review {
			t.Fatalf("%s: turn 1 stdin is not the previewed text: %+v", id, c)
		}
		if id != "qa-improve@1" {
			continue
		}
		// A follow-up continues like any conversation, replaying the previewed first turn.
		if w := do("POST", "/api/v1/ai/conversations/"+c.ID+"/messages", `{"text":"Which first?"}`); w.Code != 202 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if c = waitChat(t, do, "/api/v1", c.ID); len(c.Turns) != 4 || !strings.HasPrefix(stub.last(), p.Review) {
			t.Fatalf("follow-up: %+v", c)
		}
	}

	// Actions fit project packages only; review prompts and unknown IDs are not actions.
	for _, body := range []string{
		`{"kind":"project","action":"review-plan@1"}`,
		`{"kind":"project","action":"qa-describe@9"}`,
		`{"kind":"project","cost":true}`,
		`{"kind":"project","action":"qa-describe@1","cost":true}`,
		`{"kind":"resource","resourceId":"x","action":"qa-describe@1"}`,
	} {
		wantError(t, do("POST", "/api/v1/ai/packages", body), 400, "invalid_request")
	}
	plain := build(t, do("POST", "/api/v1/ai/packages", `{"kind":"project"}`))
	wantError(t, do("POST", "/api/v1/ai/conversations", reviewBody(plain.SHA256, "review-plan@1")), 400, "invalid_request")

	// Cost: nothing to attach before an estimate exists.
	qaCost := func(body string) quickPackage {
		w := do("POST", "/api/v1/ai/packages", body)
		build(t, w)
		return decode[quickPackage](t, w)
	}
	if p := qaCost(`{"kind":"project","action":"qa-cost@1"}`); p.CostAvailable {
		t.Fatal("cost offered without an estimate")
	}
	wantError(t, do("POST", "/api/v1/ai/packages", `{"kind":"project","action":"qa-cost@1","cost":true}`), 409, "cost_unavailable")
	if n := len(runner.Calls()); n != 0 {
		t.Fatalf("a quick action started Infracost: %v", runner.Calls())
	}

	// With an existing estimate: offered, attached only on request, and no new run.
	if w := do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"`+cost.DisclosureVersion+`"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := do("POST", "/api/v1/plans/"+s.plan()+"/cost-estimates", `{}`)
	if e := s.estimate(decode[cost.CostEstimate](t, w).ID); e.Status != cost.Completed {
		t.Fatalf("%+v", e)
	}
	calls := len(runner.Calls())
	without := qaCost(`{"kind":"project","action":"qa-cost@1"}`)
	if !without.CostAvailable || strings.Contains(without.Markdown, costTitle) {
		t.Fatal("cost data attached without opt-in, or not offered")
	}
	with := qaCost(`{"kind":"project","action":"qa-cost@1","cost":true}`)
	if !strings.Contains(with.Markdown, "## "+costTitle) || !strings.Contains(with.Markdown, "monthly change of the plan 9.89") ||
		!strings.Contains(with.Markdown, "not a bill or verified pricing") || !strings.Contains(with.Markdown, "type aws_instance: 2 changed") ||
		!strings.Contains(with.Markdown, assist.CostHiddenLine) || with.SHA256 == without.SHA256 {
		t.Fatalf("cost section:\n%s", with.Markdown)
	}
	w = do("POST", "/api/v1/ai/conversations", reviewBody(with.SHA256, "qa-cost@1"))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if c := waitChat(t, do, "/api/v1", decode[assist.Conversation](t, w).ID); c.Turns[0].Prompt != "qa-cost@1" || stub.last() != with.Review {
		t.Fatalf("%+v", c)
	}
	// Other actions never offer cost data; after opt-out the old estimate is not offered either.
	if p := qaCost(`{"kind":"project","action":"qa-security@1"}`); p.CostAvailable {
		t.Fatal("cost offered to another action")
	}
	do("POST", "/api/v1/cost/opt-out", `{}`)
	if p := qaCost(`{"kind":"project","action":"qa-cost@1"}`); p.CostAvailable {
		t.Fatal("cost offered after opt-out")
	}
	if n := len(runner.Calls()); n != calls {
		t.Fatalf("a quick action started Infracost: %v", runner.Calls()[calls:])
	}
}

// costSection is the cost section of a package, or "".
func costSection(md string) string {
	_, after, ok := strings.Cut(md, "## "+costTitle+"\n")
	if !ok {
		return ""
	}
	sec, _, _ := strings.Cut(after, "\n## ")
	return sec
}

func (s *planServer) estimateNow() cost.CostEstimate {
	s.t.Helper()
	w := s.do("POST", "/api/v1/plans/"+s.plan()+"/cost-estimates", `{}`)
	if w.Code != 202 {
		s.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return s.estimate(decode[cost.CostEstimate](s.t, w).ID)
}

// Only the session's newest, completed, current estimate can be attached, and a previewed
// package is refused once that stops being true. Hidden lines are marked, never silent.
func TestAIQuickCostLifecycle(t *testing.T) {
	s, runner, root := newCostServer(t, false)
	stub := reviewReady(s)
	do := s.do
	qaCost := func(body string) quickPackage {
		t.Helper()
		w := do("POST", "/api/v1/ai/packages", body)
		build(t, w)
		return decode[quickPackage](t, w)
	}
	withCost := `{"kind":"project","action":"qa-cost@1","cost":true}`
	start := func(p quickPackage, prompt string) int {
		return do("POST", "/api/v1/ai/conversations", reviewBody(p.SHA256, prompt)).Code
	}
	do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"`+cost.DisclosureVersion+`"}`)
	if e := s.estimateNow(); e.Status != cost.Completed {
		t.Fatalf("%+v", e)
	}

	// The fixture's random_password type line looks secret: hidden whole, counted, marked incomplete.
	p := qaCost(withCost)
	sec := costSection(p.Markdown)
	full := decode[struct{ CostHidden int }](t, do("POST", "/api/v1/ai/packages", withCost))
	if full.CostHidden != 1 || !strings.Contains(sec, assist.CostHiddenLine) || !strings.Contains(sec, "incomplete: 1 of 18 lines above were hidden") ||
		strings.Contains(p.Markdown, "random_password") || strings.Contains(p.Markdown, "infracost.io") || strings.Contains(sec, "aws_instance.") {
		t.Fatalf("hidden %d, cost section:\n%s", full.CostHidden, sec)
	}
	for _, field := range []string{"monthly change of the plan", "coverage 60%", "type aws_instance: 2 changed", "caveat: Estimates use public list prices"} {
		if !strings.Contains(sec, field) {
			t.Fatalf("cost section lacks %q:\n%s", field, sec)
		}
	}

	// A newer completed estimate replaces the previewed one.
	s.estimateNow()
	if code := start(p, "qa-cost@1"); code != 409 {
		t.Fatalf("replaced estimate: %d", code)
	}
	// A newer failed estimate: the older completed one is not offered.
	runner.Set(func(r *costtest.Runner) { r.Mode = "pricing" })
	if e := s.estimateNow(); e.Status != cost.Failed {
		t.Fatalf("%+v", e)
	}
	if qaCost(`{"kind":"project","action":"qa-cost@1"}`).CostAvailable {
		t.Fatal("an estimate older than a failed one was offered")
	}
	wantError(t, do("POST", "/api/v1/ai/packages", withCost), 409, "cost_unavailable")
	runner.Set(func(r *costtest.Runner) { r.Mode = "" })

	// Opt-out while the preview is open.
	s.estimateNow()
	p = qaCost(withCost)
	do("POST", "/api/v1/cost/opt-out", `{}`)
	if code := start(p, "qa-cost@1"); code != 409 {
		t.Fatalf("after opt-out: %d", code)
	}
	do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"`+cost.DisclosureVersion+`"}`)

	// A plan context change outside the package (variables) makes the estimate stale; a package
	// without cost data stays usable.
	s.estimateNow()
	p = qaCost(withCost)
	plain := qaCost(`{"kind":"project","action":"qa-cost@1"}`)
	if err := os.WriteFile(filepath.Join(root, "terraform.tfvars"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := start(p, "qa-cost@1"); code != 409 {
		t.Fatalf("stale estimate: %d", code)
	}
	if qaCost(`{"kind":"project","action":"qa-cost@1"}`).CostAvailable {
		t.Fatal("stale estimate offered")
	}
	w := do("POST", "/api/v1/ai/conversations", reviewBody(plain.SHA256, "qa-cost@1"))
	if w.Code != 202 {
		t.Fatalf("package without cost data: %d %s", w.Code, w.Body)
	}
	waitChat(t, do, "/api/v1", decode[assist.Conversation](t, w).ID)
	if stub.last() != plain.Review || costSection(stub.last()) != "" {
		t.Fatal("cost data sent without the attachment")
	}
}

// Hub workspaces never get the launch project's cost data, even when it has an estimate.
func TestAIHubQuickActionsGetNoCost(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.launch.Assist = assist.NewSession(false, stub)
	svc := cost.New(costtest.New(), false)
	svc.TempDir = t.TempDir()
	s.launch.Cost = svc
	t.Cleanup(func() { svc.Close(5 * time.Second) })
	if _, err := svc.OptIn(context.Background(), cost.DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(cost.Input{Binding: cost.Binding{PlanID: "p", Workspace: "dev"}, Document: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, _, ok := svc.LatestCompleted(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("estimate did not complete")
		}
	}
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	w := s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"project","action":"qa-cost@1"}`)
	build(t, w)
	if p := decode[quickPackage](t, w); p.CostAvailable || costSection(p.Markdown) != "" || p.Prompt != "qa-cost@1" {
		t.Fatalf("Hub package offered cost: %+v", p.CostAvailable)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"project","action":"qa-cost@1","cost":true}`), 409, "cost_unavailable")
}
