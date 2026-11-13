package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.handler, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 100 * time.Millisecond}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	r, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+"/api/v1/plans/"+op.PlanID+"/preflight", strings.NewReader(`{"verifyIdentity":true}`))
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
	defer resp.Body.Close()
	var report operations.PreflightReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil || resp.StatusCode != 200 || report.Identity.Message != "slow" {
		t.Fatalf("%d %v %+v", resp.StatusCode, err, report)
	}
}
