package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

func newImportServer(t *testing.T, fake *engine.Fake) (*planServer, string) {
	t.Helper()
	fake.VersionValue, fake.WorkspaceValue, fake.StateJSON = "1.9.8", "demo", fixtures.State
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_s3_bucket\" \"legacy_logs\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.Root = root
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, root)
	manager.TempDir = t.TempDir()
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	return &planServer{t, Handler(store, manager, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}, root
}

func TestImportFlowOverHTTP(t *testing.T) {
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanImport})
	options := decode[map[string]any](t, s.do("GET", "/api/v1/imports/options", ""))
	if options["canImport"] != true || options["supportsImportBlocks"] != true || options["supportsGeneratedConfig"] != true {
		t.Fatalf("%v", options)
	}
	request := `"address":"aws_s3_bucket.legacy_logs","id":"hclaro-legacy-logs","provider":"","file":"imports.tf","generateConfig":false`
	w := s.do("POST", "/api/v1/imports/preview", "{"+request+"}")
	preview := decode[imports.Preview](t, w)
	if w.Code != 200 || preview.Blocked || preview.Config != "found" || !strings.Contains(preview.Content, "to = aws_s3_bucket.legacy_logs") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// A blocked request is reported as issues, not as an HTTP error.
	blocked := decode[imports.Preview](t, s.do("POST", "/api/v1/imports/preview", `{"address":"aws_s3_bucket.artifacts","id":"x","file":"a.tf"}`))
	if !blocked.Blocked || blocked.Issues[0].Code != "address_in_state" {
		t.Fatalf("%+v", blocked)
	}
	for body, want := range map[string]string{
		`{` + request + `,"previewSha256":"` + preview.SHA256 + `"}`:                                                               "acknowledgement_required",
		`{` + request + `,"previewSha256":"` + preview.SHA256 + `","acknowledgeWrite":true}`:                                       "acknowledgement_required",
		`{` + request + `,"previewSha256":"` + preview.SHA256 + `","acknowledgeBackendAccess":true}`:                               "acknowledgement_required",
		`{` + request + `,"previewSha256":"` + preview.SHA256 + `","acknowledgeWrite":true,"acknowledgeBackendAccess":true,"x":1}`: "invalid_request",
	} {
		if w := s.do("POST", "/api/v1/imports", body); w.Code != 400 || decode[map[string]string](t, w)["error"] != want {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w = s.do("POST", "/api/v1/imports", `{`+request+`,"previewSha256":"`+strings.Repeat("0", 64)+`","acknowledgeWrite":true,"acknowledgeBackendAccess":true}`)
	if w.Code != 409 || decode[map[string]string](t, w)["error"] != "preview_changed" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); err == nil || strings.Contains(strings.Join(s.fake.Calls(), ","), "plan") {
		t.Fatal("a refused import wrote a file or ran the engine")
	}
	w = s.do("POST", "/api/v1/imports", `{`+request+`,"previewSha256":"`+preview.SHA256+`","acknowledgeWrite":true,"acknowledgeBackendAccess":true}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	op := s.wait(decode[operations.Operation](t, w).ID)
	var plan struct {
		Kind    string                   `json:"kind"`
		Imports int                      `json:"imports"`
		Import  *operations.ImportTarget `json:"import"`
		Apply   operations.ApplyStatus   `json:"apply"`
	}
	if err := json.Unmarshal(s.do("GET", "/api/v1/plans/"+op.PlanID, "").Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Kind != "import" || plan.Imports != 1 || plan.Import.Address != "aws_s3_bucket.legacy_logs" || !plan.Apply.Available {
		t.Fatalf("%+v", plan)
	}
	// The same file cannot be written twice.
	w = s.do("POST", "/api/v1/imports", `{`+request+`,"previewSha256":"`+preview.SHA256+`","acknowledgeWrite":true,"acknowledgeBackendAccess":true}`)
	if w.Code != 409 || decode[map[string]string](t, w)["error"] != "import_blocked" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
