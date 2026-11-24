package api

import (
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/operations"
)

func TestLaunchDiscardRoute(t *testing.T) {
	s := newPlanServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	op := s.wait(decode[operations.Operation](t, s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)).ID)
	pid := op.PlanID
	if w := s.do("POST", "/api/v1/plans/"+pid+"/discard", `{"x":1}`); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if w := s.do("POST", "/api/v1/plans/"+strings.Repeat("0", 32)+"/discard", `{}`); w.Code != 404 {
		t.Fatalf("unknown plan: %d", w.Code)
	}
	w := s.do("POST", "/api/v1/plans/"+pid+"/discard", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "You discarded this plan.") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/plans/"+pid+"/discard", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "not_applicable") {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
	detail := decode[struct{ Apply operations.ApplyStatus }](t, s.do("GET", "/api/v1/plans/"+pid, ""))
	if detail.Apply.Available || detail.Apply.ArtifactRetained {
		t.Fatalf("%+v", detail.Apply)
	}
}
