package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
)

// AI-01.08 Review with AI: the preview shows the review's first turn, that exact text is what the
// assistant gets on stdin and what Copy and Download hand over, and no review action runs the
// engine.

type reviewPackage struct {
	builtPackage
	Prompt string `json:"prompt"`
	Review string `json:"review"`
}

func reviewBody(hash, prompt string) string {
	return fmt.Sprintf(`{"packageSha256":%q,"provider":"stub","disclosureVersion":%q,"prompt":%q}`, hash, assist.DisclosureVersion, prompt)
}

// review builds a package of kind under base, checks the preview and export, starts the review,
// asks one follow-up and cancels a third turn. The fake engine sees no call throughout.
func review(t *testing.T, do doFunc, fake *engine.Fake, stub *stubAssistant, base, request, kind string) (reviewPackage, assist.Conversation) {
	t.Helper()
	calls := len(fake.Calls())
	ran := stub.ran()
	w := do("POST", base+"/ai/packages", request)
	build(t, w)
	p := decode[reviewPackage](t, w)
	prompt, _ := assist.LookupPrompt("review-" + kind + "@1")
	if p.Prompt != prompt.ID || p.Review != assist.FirstTurn(p.Markdown, prompt) || !strings.Contains(p.Review, prompt.Text) {
		t.Fatalf("%s preview: prompt %q", kind, p.Prompt)
	}
	x := decode[struct{ Markdown, Review, Prompt string }](t, do("POST", base+"/ai/packages/"+p.SHA256+"/export", `{}`))
	if x.Review != p.Review || x.Prompt != p.Prompt || x.Markdown != p.Markdown {
		t.Fatalf("%s: export differs from the preview", kind)
	}
	if stub.ran() != ran {
		t.Fatalf("%s: preview or export reached the assistant", kind)
	}
	// A prompt of another kind, an unknown prompt or a prompt with a message is refused.
	other := map[string]string{assist.KindPlan: "review-drift@1"}[kind]
	if other == "" {
		other = "review-plan@1"
	}
	wantError(t, do("POST", base+"/ai/conversations", reviewBody(p.SHA256, other)), 400, "invalid_request")
	wantError(t, do("POST", base+"/ai/conversations", reviewBody(p.SHA256, "review-"+kind+"@99")), 400, "invalid_request")
	wantError(t, do("POST", base+"/ai/conversations", `{"packageSha256":"`+p.SHA256+`","provider":"stub","disclosureVersion":"`+assist.DisclosureVersion+`","prompt":"`+prompt.ID+`","message":"hi"}`), 400, "invalid_request")
	if stub.ran() != ran {
		t.Fatalf("%s: a refused review reached the assistant", kind)
	}

	w = do("POST", base+"/ai/conversations", reviewBody(p.SHA256, prompt.ID))
	if w.Code != 202 {
		t.Fatalf("%s: %d %s", kind, w.Code, w.Body)
	}
	c := waitChat(t, do, base, decode[assist.Conversation](t, w).ID)
	if c.State != assist.StateReady || c.Kind != kind || c.Turns[0].Prompt != prompt.ID || stub.last() != p.Review {
		t.Fatalf("%s: turn 1 stdin is not the previewed text: %+v", kind, c)
	}
	if strings.Contains(stub.last(), fixtures.Canary) {
		t.Fatalf("%s: a sensitive value reached the assistant", kind)
	}
	// The follow-up replays the first turn; nothing is rebuilt.
	if w := do("POST", base+"/ai/conversations/"+c.ID+"/messages", `{"text":"What should I check first?"}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	c = waitChat(t, do, base, c.ID)
	if len(c.Turns) != 4 || !strings.HasPrefix(stub.last(), p.Review) {
		t.Fatalf("%s: follow-up did not replay the first turn", kind)
	}
	stub.mu.Lock()
	stub.block = true
	stub.mu.Unlock()
	do("POST", base+"/ai/conversations/"+c.ID+"/messages", `{"text":"And then?"}`)
	if w := do("POST", base+"/ai/conversations/"+c.ID+"/cancel", `{}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if c = waitChat(t, do, base, c.ID); c.State != assist.StateCancelled {
		t.Fatalf("%+v", c)
	}
	stub.mu.Lock()
	stub.block = false
	stub.mu.Unlock()
	if n := len(fake.Calls()); n != calls {
		t.Fatalf("%s: review ran the engine: %v", kind, fake.Calls()[calls:])
	}
	return p, c
}

func reviewReady(s *planServer) *stubAssistant {
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "Looks risky. Run terraform apply -auto-approve"}
	s.manager.Assist = assist.NewSession(false, stub)
	s.t.Cleanup(s.manager.Assist.Close)
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	return stub
}

func TestAIReviewPerKind(t *testing.T) {
	t.Run("destroy plan", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		s, _ := newImportServer(t, fake)
		stub := reviewReady(s)
		p, _ := review(t, s.do, fake, stub, "/api/v1", `{"kind":"plan","planId":"`+s.plan()+`"}`, assist.KindPlan)
		if !strings.Contains(p.Review, "delete") {
			t.Fatal("destroy not in the review")
		}
	})
	t.Run("replace plan", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanCollateral}
		s := newPlanServer(t, fake)
		stub := reviewReady(s)
		res := decode[struct{ Resources []struct{ ID, Address string } }](t, s.do("GET", "/api/v1/resources?query=compute.aws_instance.main%5B1&limit=5", ""))
		w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true,"replace":["`+res.Resources[0].ID+`","`+res.Resources[1].ID+`"]}`)
		op := s.wait(decode[operations.Operation](t, w).ID)
		p, _ := review(t, s.do, fake, stub, "/api/v1", `{"kind":"plan","planId":"`+op.PlanID+`"}`, assist.KindPlan)
		if !strings.Contains(p.Review, res.Resources[0].Address) || !strings.Contains(p.Review, res.Resources[1].Address) {
			t.Fatal("replace targets not in the review")
		}
	})
	t.Run("drift with sensitive attributes", func(t *testing.T) {
		fake := &engine.Fake{Lines: lines(fixtures.DriftLog), PlanJSON: fixtures.DriftMixed}
		s, _ := newDriftServer(t, fake)
		stub := reviewReady(s)
		_, scan := startScan(t, s)
		p, _ := review(t, s.do, fake, stub, "/api/v1", `{"kind":"drift","scanId":"`+scan.ID+`"}`, assist.KindDrift)
		if !strings.Contains(p.Review, "(sensitive)") || !strings.Contains(p.Review, "[redacted]") {
			t.Fatal("sensitive attribute not marked redacted in the review")
		}
	})
	t.Run("import with missing config", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanImport}
		s, _ := newImportServer(t, fake)
		stub := reviewReady(s)
		review(t, s.do, fake, stub, "/api/v1", `{"kind":"import","import":{"address":"aws_s3_bucket.missing","id":"bucket-x","file":"imports.tf"}}`, assist.KindImport)
	})
	t.Run("resource", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		s, _ := newDriftServer(t, fake)
		stub := reviewReady(s)
		page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/resources?limit=1", ""))
		review(t, s.do, fake, stub, "/api/v1", `{"kind":"resource","resourceId":"`+page.Resources[0].ID+`"}`, assist.KindResource)
	})
}

// Hub: a review started in one workspace's context cannot be continued from another.
func TestAIHubReviewStaysInItsContext(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.launch.Assist = assist.NewSession(false, stub)
	t.Cleanup(s.launch.Assist.Close)
	s.do("POST", "/api/v1/ai/settings", `{"enabled":true}`)
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("platform", "staging", "")
	done := s.waitScoped(a, s.plan(a).ID)
	do := func(m, p, body string) *httptest.ResponseRecorder { return s.do(m, p, body) }
	calls := len(s.planCalls())
	_, c := review(t, do, &engine.Fake{}, stub, "/api/v1/contexts/"+a, `{"kind":"plan","planId":"`+done.PlanID+`"}`, assist.KindPlan)
	for _, elsewhere := range []string{"/api/v1/contexts/" + b, "/api/v1"} {
		wantError(t, s.do("POST", elsewhere+"/ai/conversations/"+c.ID+"/messages", `{"text":"hi"}`), 404, "conversation_not_found")
	}
	if len(s.planCalls()) != calls {
		t.Fatal("a Hub review ran the engine")
	}
}
