package api

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/internal/operations"
)

// B03: launch and Hub routes share one set of handlers, so the same request answers the
// same status, error code and body shape on both, including the apply length limits that
// only the Hub route used to enforce.
func TestLaunchAndHubRoutesAnswerAlike(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	hubPlan := s.planned(cid)
	hubOps := decode[struct{ Operations []scopedOp }](t, s.do("GET", "/api/v1/contexts/"+cid+"/operations", "")).Operations
	if len(hubOps) == 0 {
		t.Fatal("no Hub operation")
	}
	hubOp := hubOps[0]

	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	if w.Code != 202 {
		t.Fatalf("launch plan: %d %s", w.Code, w.Body)
	}
	launchOp := decode[operations.Operation](t, w)
	for deadline := time.Now().Add(30 * time.Second); launchOp.Status == operations.Running || launchOp.Status == operations.Queued; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("launch plan did not finish")
		}
		launchOp = decode[operations.Operation](t, s.do("GET", "/api/v1/operations/"+launchOp.ID, ""))
	}
	if launchOp.PlanID == "" {
		t.Fatalf("launch plan: %+v", launchOp)
	}

	missing, long := strings.Repeat("0", 32), strings.Repeat("a", 129)
	cases := []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "/plans", `{}`, 400, "acknowledgement_required"},
		{"POST", "/plans", `{"acknowledgeBackendAccess":true,"target":["aws_s3_bucket.logs"]}`, 400, "invalid_request"},
		{"POST", "/plans", `{"acknowledgeBackendAccess":true,"contextFingerprint":"` + long + `"}`, 400, "invalid_request"},
		{"POST", "/plans", `{"acknowledgeBackendAccess":true,"varFiles":["missing.tfvars"]}`, 400, "invalid_var_files"},
		{"GET", "/plans/" + missing, "", 404, "plan_not_found"},
		{"GET", "/plans/not-an-id", "", 404, "plan_not_found"},
		{"GET", "/plans/{pid}/changes/" + missing, "", 404, "change_not_found"},
		{"GET", "/plans/" + missing + "/changes/" + missing, "", 404, "plan_not_found"},
		{"POST", "/plans/{pid}/preflight", `{"verifyIdentity":false,"profileId":"x"}`, 400, "invalid_request"},
		{"POST", "/plans/" + missing + "/preflight", `{}`, 404, "plan_not_found"},
		{"POST", "/plans/{pid}/approval", `{"preflightBinding":"` + long + `","acknowledged":[]}`, 400, "invalid_request"},
		{"POST", "/plans/{pid}/approval", `{"preflightBinding":"x","acknowledged":["` + long + `"]}`, 400, "invalid_request"},
		{"POST", "/plans/{pid}/apply", `{"confirmationToken":"` + long + `","confirmation":"apply"}`, 400, "invalid_request"},
		{"POST", "/plans/{pid}/apply", `{"confirmationToken":"x","confirmation":"` + strings.Repeat("a", 257) + `"}`, 400, "invalid_request"},
		{"POST", "/plans/{pid}/apply", `{"confirmationToken":"x","confirmation":"apply","planFile":"/tmp/plan"}`, 400, "invalid_request"},
		{"POST", "/plans/" + missing + "/apply", `{"confirmationToken":"x","confirmation":"apply"}`, 404, "plan_not_found"},
		{"GET", "/operations/" + missing, "", 404, "operation_not_found"},
		{"GET", "/operations/" + missing + "/events", "", 404, "operation_not_found"},
		{"POST", "/operations/" + missing + "/cancel", `{}`, 404, "operation_not_found"},
		{"POST", "/operations/{oid}/cancel", `{}`, 409, "operation_not_running"},
		{"POST", "/operations/{oid}/cancel", `{"force":true}`, 400, "invalid_request"},
		{"POST", "/imports", `{}`, 400, "acknowledgement_required"},
		{"POST", "/imports", `{"acknowledgeWrite":true,"acknowledgeBackendAccess":true,"previewSha256":"` + long + `"}`, 400, "invalid_request"},
		{"POST", "/discovery/identity", `{"contextFingerprint":"` + long + `"}`, 400, "invalid_request"},
		{"POST", "/discovery/identity", `{}`, 409, "discovery_unavailable"},
		{"POST", "/discovery/scans", `{}`, 400, "acknowledgement_required"},
		{"GET", "/discovery/scans/not-an-id", "", 404, "scan_not_found"},
	}
	type answer struct {
		status int
		code   string
		keys   []string
	}
	ask := func(base, pid, oid string, c struct {
		method, path, body string
		status             int
		code               string
	}) answer {
		t.Helper()
		path := strings.NewReplacer("{pid}", pid, "{oid}", oid).Replace(c.path)
		w := s.do(c.method, base+path, c.body)
		body := decode[map[string]any](t, w)
		code, _ := body["error"].(string)
		return answer{w.Code, code, slices.Sorted(maps.Keys(body))}
	}
	for _, c := range cases {
		launch := ask("/api/v1", launchOp.PlanID, launchOp.ID, c)
		hub := ask("/api/v1/contexts/"+cid, hubPlan, hubOp.ID, c)
		if launch.status != c.status || launch.code != c.code {
			t.Errorf("launch %s %s: want %d %s, got %+v", c.method, c.path, c.status, c.code, launch)
		}
		if !slices.Equal(launch.keys, hub.keys) || launch.status != hub.status || launch.code != hub.code {
			t.Errorf("%s %s: launch %+v, Hub %+v", c.method, c.path, launch, hub)
		}
	}
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}
}
