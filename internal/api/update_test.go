package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/internal/operations"
	"hclaro/internal/update"
	"hclaro/internal/workspace"
)

func updateStatus(t *testing.T, checker *update.Checker) (update.Status, *httptest.ResponseRecorder) {
	t.Helper()
	store := workspace.NoProject()
	manager := operations.New(store, nil, "", operations.Deps{})
	t.Cleanup(func() { manager.Close(time.Second) })
	s := &hubServer{t: t, handler: Handler(store, manager, nil, &Services{Update: checker}, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "1.0.0")}
	w := s.do("GET", "/api/v1/update", "")
	var got update.Status
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err, w.Body)
	}
	return got, w
}

func TestUpdateRouteIsDisabledWithoutAChecker(t *testing.T) {
	got, w := updateStatus(t, nil)
	if w.Code != 200 || got.Enabled || got.Available || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %+v %v", w.Code, got, w.Header())
	}
}

func TestUpdateRouteReportsANewerRelease(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"https://github.com/botopsy/hclaro/releases/tag/v1.2.0"}`))
	}))
	defer up.Close()
	got, _ := updateStatus(t, &update.Checker{Current: "1.0.0", URL: up.URL})
	if !got.Enabled || !got.Available || got.Latest != "1.2.0" || got.Current != "1.0.0" {
		t.Fatalf("%+v", got)
	}
}
