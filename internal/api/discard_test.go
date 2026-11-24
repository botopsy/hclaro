package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/gitctx"
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

func TestPlanChangeSourceRoute(t *testing.T) {
	s := newPlanServer(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions})
	op := s.wait(decode[operations.Operation](t, s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)).ID)
	plan := decode[struct {
		Changes []struct{ ID, Address string }
	}](t, s.do("GET", "/api/v1/plans/"+op.PlanID, ""))
	var create string
	for _, c := range plan.Changes {
		if c.Address == "aws_s3_bucket.logs" {
			create = c.ID
		}
	}
	path := "/api/v1/plans/" + op.PlanID + "/changes/" + create + "/source"
	// No Git service in this session.
	if w := s.do("GET", path, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "git_unavailable") {
		t.Fatalf("no git: %d %s", w.Code, w.Body)
	}
	s.manager.Git = gitctx.New()
	if err := os.WriteFile(filepath.Join(s.manager.Root(), "main.tf"), []byte("\nresource \"aws_s3_bucket\" \"logs\" {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := decode[gitctx.SourceReference](t, s.do("GET", path, ""))
	if ref.State != "found" || ref.Path != "main.tf" || ref.Line != 2 || ref.FileID == "" {
		t.Fatalf("%+v", ref)
	}
	for url, want := range map[string]int{
		"/api/v1/plans/" + op.PlanID + "/changes/" + strings.Repeat("0", 32) + "/source": 404,
		"/api/v1/plans/" + op.PlanID + "/changes/zz/source":                              404,
		"/api/v1/plans/" + strings.Repeat("0", 32) + "/changes/" + create + "/source":    404,
	} {
		if w := s.do("GET", url, ""); w.Code != want {
			t.Errorf("%s: %d", url, w.Code)
		}
	}
}
