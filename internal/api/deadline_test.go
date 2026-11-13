package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/preflight"
)

// A handler that runs past the server's WriteTimeout still delivers its response: the
// server here allows 100 ms and the identity probe takes 400 ms, as a slow STS call would
// outlast the real 20 s timeout.
func TestSlowPreflightOutlastsWriteTimeout(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	s.manager.GitProbe = func(context.Context, string) preflight.GitResult {
		return preflight.GitResult{State: preflight.GitClean, Paths: []string{}}
	}
	s.manager.IdentityProbe = func(ctx context.Context, _ []string) preflight.Identity {
		select {
		case <-time.After(400 * time.Millisecond):
			return preflight.Identity{State: "unavailable", Message: "slow"}
		case <-ctx.Done():
			return preflight.Identity{State: "unavailable", Message: "cancelled"}
		}
	}
	op := s.wait(decode[operations.Operation](t, s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)).ID)

	resp := postPastWriteTimeout(t, s, "/api/v1/plans/"+op.PlanID+"/preflight", `{"verifyIdentity":true}`)
	defer resp.Body.Close()
	var report operations.PreflightReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil || resp.StatusCode != 200 || report.Identity.Message != "slow" {
		t.Fatalf("%d %v %+v", resp.StatusCode, err, report)
	}
}

// Under an AWS account policy, apply asks STS again; a slow answer must not cut off the 202.
func TestSlowApplyIdentityRecheckOutlastsWriteTimeout(t *testing.T) {
	const account = "123456789012"
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: [][]byte{}})
	s.manager.Reload = func(context.Context) {}
	s.manager.GitProbe = func(context.Context, string) preflight.GitResult {
		return preflight.GitResult{State: preflight.GitClean, Paths: []string{}}
	}
	var probes atomic.Int32
	s.manager.IdentityProbe = func(ctx context.Context, _ []string) preflight.Identity {
		if probes.Add(1) > 1 {
			select {
			case <-time.After(400 * time.Millisecond):
			case <-ctx.Done():
			}
		}
		return preflight.Identity{State: "verified", Account: account, ARN: "arn:aws:iam::" + account + ":user/test"}
	}
	dir := filepath.Join(s.manager.Root(), ".hclaro")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.json"), []byte(`{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["`+account+`"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	op := s.wait(decode[operations.Operation](t, s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)).ID)
	report := decode[operations.PreflightReport](t, s.do("POST", "/api/v1/plans/"+op.PlanID+"/preflight", `{"verifyIdentity":true}`))
	body, _ := json.Marshal(map[string]any{"preflightBinding": report.Binding, "acknowledged": report.Acknowledge})
	approval := decode[operations.Approval](t, s.do("POST", "/api/v1/plans/"+op.PlanID+"/approval", string(body)))
	body, _ = json.Marshal(map[string]string{"confirmationToken": approval.Token, "confirmation": approval.Phrase})
	resp := postPastWriteTimeout(t, s, "/api/v1/plans/"+op.PlanID+"/apply", string(body))
	defer resp.Body.Close()
	var applied operations.Operation
	if err := json.NewDecoder(resp.Body).Decode(&applied); err != nil || resp.StatusCode != 202 || probes.Load() != 2 {
		t.Fatalf("%d %v after %d probes", resp.StatusCode, err, probes.Load())
	}
	if done := s.wait(applied.ID); done.Outcome != "applied" {
		t.Fatalf("%+v", done)
	}
}

// postPastWriteTimeout sends a same-origin POST through a real server that allows 100 ms to
// write a response, and fails the test if the response is cut off.
func postPastWriteTimeout(t *testing.T, s *planServer, path, body string) *http.Response {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.handler, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 100 * time.Millisecond}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	r, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+path, strings.NewReader(body))
	r.Host = host
	r.Header.Set("Authorization", "Bearer "+session)
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Hclaro-CSRF", csrf)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("response cut off: %v", err)
	}
	return resp
}
