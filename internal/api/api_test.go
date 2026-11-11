package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

func setup() http.Handler {
	s := workspace.New(workspace.Info{Project: "demo", Initialized: true})
	s.Load(context.Background(), &engine.Mock{VersionValue: "1.9.8", WorkspaceValue: "demo", JSON: fixtures.State}, "terraform")
	return Handler(s, operations.NewManager(s, nil, ""), nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, "127.0.0.1:9273", strings.Repeat("a", 64), strings.Repeat("b", 64), "test")
}
func request(handler http.Handler, path, host, origin, token, method string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:9273"+path, nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestLocalBoundary(t *testing.T) {
	h := setup()
	token := strings.Repeat("a", 64)
	for _, tc := range []struct {
		host, origin, token, method string
		code                        int
	}{
		{"127.0.0.1:9273", "", token, "GET", 200},
		{"127.0.0.1:9273", "http://127.0.0.1:9273", token, "GET", 200},
		{"evil.example:9273", "", token, "GET", 403},
		{"127.0.0.1:9273", "https://evil.example", token, "GET", 403},
		{"127.0.0.1:9273", "null", token, "GET", 403},
		{"127.0.0.1:9273", "", "", "GET", 401},
		{"127.0.0.1:9273", "", "wrong", "GET", 401},
		{"127.0.0.1:9273", "", token, "POST", 403},
	} {
		w := request(h, "/api/v1/workspace", tc.host, tc.origin, tc.token, tc.method)
		if w.Code != tc.code {
			t.Fatalf("%+v got %d", tc, w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("CORS enabled")
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing headers")
		}
	}
}
func TestResourcesAndSecretBoundary(t *testing.T) {
	h := setup()
	token := strings.Repeat("a", 64)
	get := func(path string) *httptest.ResponseRecorder {
		w := request(h, path, "127.0.0.1:9273", "", token, "GET")
		if strings.Contains(w.Body.String(), "DO_NOT_LEAK_HCLARO_TEST_SECRET") {
			t.Fatal("secret in response")
		}
		return w
	}
	for _, path := range []string{"/api/v1/workspace", "/api/v1/health", "/api/v1/resources?limit=200"} {
		if get(path).Code != 200 {
			t.Fatal(path)
		}
	}
	w := get("/api/v1/resources?module=module.compute&query=main%5B1%5D")
	var list struct {
		Resources []struct {
			ID string `json:"id"`
		}
		Total int `json:"total"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Total != 1 {
		t.Fatal(w.Body.String())
	}
	if get("/api/v1/resources/"+list.Resources[0].ID).Code != 200 {
		t.Fatal("detail missing")
	}
	for _, path := range []string{"/api/v1/resources?limit=10000", "/api/v1/resources?offset=-1", "/api/v1/resources?sort=evil", "/api/v1/resources?mode=execute"} {
		if get(path).Code != 400 {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/api/v1/apply", "/api/v1/imports", "/api/v1/execute", "/api/v1/plans/" + strings.Repeat("0", 32), "/api/v1/plans/not-an-id"} {
		if get(path).Code != 404 {
			t.Fatal(path)
		}
	}
	// Every resource detail, not just the first page, must be redacted.
	s := workspace.New(workspace.Info{Initialized: true})
	s.Load(context.Background(), &engine.Mock{VersionValue: "1.9.8", WorkspaceValue: "demo", JSON: fixtures.State}, "terraform")
	for _, r := range s.Get().Index.Resources {
		if get("/api/v1/resources/"+r.ID).Code != 200 {
			t.Fatal(r.ID)
		}
	}
}
