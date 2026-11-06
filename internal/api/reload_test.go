package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// A launch project read before it was initialized becomes plannable after "Read again".
func TestLaunchReloadReinspectsAndRereads(t *testing.T) {
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State}
	store := workspace.New(workspace.Info{Project: "demo", Backend: "unknown", ExecutionMode: "unsupported"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, t.TempDir())
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	s := &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}
	if w := s.do("POST", "/api/v1/workspace/reload", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "reload_unavailable") {
		t.Fatalf("without a launch reload: %d %s", w.Code, w.Body)
	}
	manager.Reload = func(ctx context.Context) { store.Load(ctx, fake, "terraform") }
	manager.Reinspect = func() workspace.Info {
		return workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"}
	}
	// The route is a guarded mutation: session, CSRF and a strict empty body.
	r := httptest.NewRequest("POST", "http://"+host+"/api/v1/workspace/reload", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+session)
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("without CSRF: %d", w.Code)
	}
	if w := s.do("POST", "/api/v1/workspace/reload", `{"init":true}`); w.Code != 400 {
		t.Fatalf("extra field: %d", w.Code)
	}
	if w := s.do("GET", "/api/v1/workspace/reload", ""); w.Code == 200 || w.Code == 202 {
		t.Fatalf("GET: %d", w.Code)
	}
	before := len(fake.Calls())
	if w := s.do("POST", "/api/v1/workspace/reload", `{}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if store.Get().Status != "loading" {
			break
		}
	}
	snap := decode[struct {
		Status string
		Info   workspace.Info
	}](t, s.do("GET", "/api/v1/workspace", ""))
	if snap.Status != "ready" || !snap.Info.Initialized || !snap.Info.Capabilities.CanPlan {
		t.Fatalf("%+v", snap)
	}
	for _, call := range fake.Calls()[before:] {
		if call != "version" && call != "workspace" && call != "show" {
			t.Fatalf("reload ran %q", call)
		}
	}
}
