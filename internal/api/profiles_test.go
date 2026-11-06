package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/profiles"
	"hclaro/internal/workspace"
)

func profileServer(t *testing.T) *hubServer {
	t.Helper()
	s := newHubServer(t)
	s.hub.Profiles = profiles.Open(t.TempDir(), time.Now())
	t.Setenv("AWS_SECRET_ACCESS_KEY", fixtures.Canary)
	t.Setenv("TF_VAR_db_password", fixtures.Canary)
	return s
}

func TestProfileCRUDOverHTTP(t *testing.T) {
	s := profileServer(t)
	network := s.add("network").ID
	base := "/api/v1/projects/" + network + "/execution-profiles"
	w := s.do("POST", base, `{"name":"Production","workspace":"prod","awsProfile":"prod-sso","awsRegion":"eu-west-1","parallelism":4,"envNames":["TF_VAR_db_password"]}`)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	created := decode[profileView](t, w)
	if created.Revision != 1 || created.AWSProfile != "prod-sso" {
		t.Fatalf("%+v", created)
	}
	item := base + "/" + created.ID
	// Every change needs the session, the exact origin and the CSRF header.
	for _, tc := range []struct{ method, path, body string }{
		{"POST", base, `{"name":"x"}`},
		{"PATCH", item, `{"name":"x","expectedRevision":1}`},
		{"DELETE", item, `{"expectedRevision":1}`},
		{"POST", item + "/clone", `{"name":"x"}`},
		{"POST", base + "/default", `{"profileId":""}`},
	} {
		if w := s.do(tc.method, tc.path, tc.body, without("X-Hclaro-CSRF")); w.Code != 403 {
			t.Fatalf("%s %s without CSRF: %d", tc.method, tc.path, w.Code)
		}
		if w := s.do(tc.method, tc.path, tc.body, with("Origin", "https://evil.example")); w.Code != 403 {
			t.Fatalf("%s %s cross-origin: %d", tc.method, tc.path, w.Code)
		}
	}
	if w := s.do("PATCH", item, `{"name":"Production","workspace":"prod","awsProfile":"prod-readonly","expectedRevision":1}`); w.Code != 200 || decode[profileView](t, w).Revision != 2 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if w := s.do("PATCH", item, `{"name":"Production","expectedRevision":1}`); w.Code != 409 || !strings.Contains(w.Body.String(), "revision_conflict") {
		t.Fatalf("stale update: %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", item+"/clone", `{"name":"Production copy"}`); w.Code != 201 {
		t.Fatalf("clone: %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", base+"/default", fmt.Sprintf(`{"profileId":%q}`, created.ID)); w.Code != 200 {
		t.Fatalf("default: %d %s", w.Code, w.Body)
	}
	list := decode[struct {
		Profiles  []profileView `json:"profiles"`
		DefaultID string        `json:"defaultId"`
	}](t, s.do("GET", base, ""))
	if len(list.Profiles) != 2 || list.DefaultID != created.ID || !list.Profiles[0].Default {
		t.Fatalf("list %+v", list)
	}
	if w := s.do("DELETE", item, `{"expectedRevision":2}`); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := s.do("DELETE", item, `{"expectedRevision":2}`); w.Code != 404 {
		t.Fatalf("second delete: %d", w.Code)
	}
}

// Requests cannot carry values: unknown fields are refused by strict decoding and
// credential-shaped values by validation. Responses never echo them.
func TestProfileRequestsCannotStoreSecrets(t *testing.T) {
	s := profileServer(t)
	network := s.add("network").ID
	base := "/api/v1/projects/" + network + "/execution-profiles"
	for _, body := range []string{
		`{"name":"x","awsSecretAccessKey":"` + fixtures.Canary + `"}`,
		`{"name":"x","env":{"AWS_SECRET_ACCESS_KEY":"` + fixtures.Canary + `"}}`,
		`{"name":"x","varFileContents":"db_password = 1"}`,
	} {
		if w := s.do("POST", base, body); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{
		`{"name":"` + fixtures.Canary + `"}`,
		`{"name":"x","awsProfile":"AKIAIOSFODNN7EXAMPLE"}`,
		`{"name":"x","envNames":["AWS_SECRET_ACCESS_KEY=` + fixtures.Canary + `"]}`,
		`{"name":"x","envNames":["TF_CLI_ARGS_plan"]}`,
		`{"name":"x","varFiles":["../../etc/passwd.tfvars"]}`,
	} {
		if w := s.do("POST", base, body); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_profile") {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if list := decode[struct {
		Profiles []profileView `json:"profiles"`
	}](t, s.do("GET", base, "")); len(list.Profiles) != 0 {
		t.Fatalf("stored: %+v", list.Profiles)
	}
}

func TestProfilesAreScopedToTheirProject(t *testing.T) {
	s := profileServer(t)
	network, platform := s.add("network").ID, s.add("platform").ID
	w := s.do("POST", "/api/v1/projects/"+network+"/execution-profiles", `{"name":"shared label"}`)
	a := decode[profileView](t, w)
	if w := s.do("POST", "/api/v1/projects/"+platform+"/execution-profiles", `{"name":"shared label"}`); w.Code != 201 {
		t.Fatalf("same label in another project: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/projects/" + platform + "/execution-profiles/" + a.ID, `{"name":"x","expectedRevision":1}`},
		{"DELETE", "/api/v1/projects/" + platform + "/execution-profiles/" + a.ID, `{"expectedRevision":1}`},
		{"POST", "/api/v1/projects/" + platform + "/execution-profiles/" + a.ID + "/clone", `{"name":"y"}`},
		{"POST", "/api/v1/projects/" + platform + "/execution-profiles/default", fmt.Sprintf(`{"profileId":%q}`, a.ID)},
		{"GET", "/api/v1/projects/" + platform + "/execution-context?profileId=" + a.ID, ""},
		{"POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"default","profileId":%q}`, platform, a.ID)},
	} {
		if w := s.do(tc.method, tc.path, tc.body); w.Code != 404 {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

func TestExecutionContextSummaryAndVarFiles(t *testing.T) {
	s := profileServer(t)
	summary := s.add("network")
	root := summary.RootPath
	for name, content := range map[string]string{"env/prod.tfvars": `db_password = "` + fixtures.Canary + `"`, "prod.tfvars": "x = 1", ".secret/hidden.tfvars": "x = 1", "terraform.tfvars": "x = 1"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := decode[struct {
		Files []profiles.VarFile `json:"files"`
	}](t, s.do("GET", "/api/v1/projects/"+summary.ID+"/var-files", ""))
	if len(files.Files) != 2 || files.Files[0].Ref != "env/prod.tfvars" || files.Files[1].Ref != "prod.tfvars" {
		t.Fatalf("candidates %+v", files.Files)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "stolen.tfvars"), []byte("x = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "stolen.tfvars"), filepath.Join(root, "escape.tfvars")); err != nil {
		t.Fatal(err)
	}
	for ref, code := range map[string]string{"escape.tfvars": "var_file_outside_root", "env/missing.tfvars": "var_file_missing", ".secret/hidden.tfvars": ""} {
		w := s.do("POST", "/api/v1/projects/"+summary.ID+"/execution-profiles", `{"name":"bad","varFiles":["`+ref+`"]}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("%s saved: %d %s", ref, w.Code, w.Body)
		}
	}
	w := s.do("POST", "/api/v1/projects/"+summary.ID+"/execution-profiles", `{"name":"prod","workspace":"prod","varFiles":["env/prod.tfvars"],"awsProfile":"prod-sso","envNames":["TF_VAR_db_password","VAULT_TOKEN"]}`)
	p := decode[profileView](t, w)
	before := s.calls.Load()
	ctx := decode[executionSummary](t, s.do("GET", "/api/v1/projects/"+summary.ID+"/execution-context?profileId="+p.ID+"&workspace=dev", ""))
	if s.calls.Load() != before {
		t.Fatal("the summary ran an engine command")
	}
	if ctx.Context.Identity != profiles.IdentityNotVerified || ctx.Context.Fingerprint == "" || ctx.Workspace != "prod" || ctx.WorkspaceSource != "profile" || len(ctx.Context.VarFiles) != 1 || ctx.RootPath != root {
		t.Fatalf("summary %+v", ctx)
	}
	codes := []string{}
	for _, issue := range ctx.Issues {
		codes = append(codes, issue.Code)
	}
	if strings.Join(codes, ",") != "env_missing,profile_workspace" {
		t.Fatalf("issues %v", codes)
	}
	// A context cannot be opened with a profile for another workspace.
	if w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"dev","profileId":%q}`, summary.ID, p.ID)); w.Code != 409 {
		t.Fatalf("open with mismatched profile: %d %s", w.Code, w.Body)
	}
}

// The launch project's plan binds the reviewed profile; an edit afterwards makes the stored
// plan unapplicable over HTTP as well.
func TestLaunchPlanWithProfileOverHTTP(t *testing.T) {
	s := profileServer(t)
	network := s.add("network")
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "dev", StateJSON: fixtures.State, PlanJSON: fixtures.PlanAllActions}
	store := workspace.New(workspace.Info{Project: "network", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, network.RootPath)
	manager.TempDir = t.TempDir()
	manager.Locks = s.hub.Locks
	manager.Binder = s.hub.LaunchBinder()
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	s.hub.Launch = network.RootPath
	s.handler = Handler(store, manager, s.hub, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	p := decode[profileView](t, s.do("POST", "/api/v1/projects/"+network.ID+"/execution-profiles", `{"name":"dev","awsProfile":"dev-sso","parallelism":3}`))
	options := decode[struct {
		ProjectID string            `json:"projectId"`
		Context   *executionSummary `json:"context"`
	}](t, s.do("GET", "/api/v1/plans/options?profileId="+p.ID, ""))
	if options.ProjectID != network.ID || options.Context == nil || options.Context.Workspace != "dev" || options.Context.WorkspaceSource != "launch" || len(options.Context.Issues) != 0 {
		t.Fatalf("options %+v", options)
	}
	fingerprint := options.Context.Context.Fingerprint
	if w := s.do("POST", "/api/v1/plans", fmt.Sprintf(`{"acknowledgeBackendAccess":true,"profileId":%q,"contextFingerprint":"stale"}`, p.ID)); w.Code != 409 || !strings.Contains(w.Body.String(), "context_changed") {
		t.Fatalf("stale fingerprint: %d %s", w.Code, w.Body)
	}
	w := s.do("POST", "/api/v1/plans", fmt.Sprintf(`{"acknowledgeBackendAccess":true,"profileId":%q,"contextFingerprint":%q}`, p.ID, fingerprint))
	if w.Code != 202 {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	op := decode[operations.Operation](t, w)
	if op.Context == nil || op.Context.ProfileName != "dev" || !strings.Contains(strings.Join(op.Command, " "), "-parallelism=3") {
		t.Fatalf("operation %+v", op)
	}
	var done operations.Operation
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if done, _, _ = manager.Operation(op.ID); done.Status != operations.Running {
			break
		}
	}
	if done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	type planStatus struct {
		ContextChanged bool                   `json:"contextChanged"`
		Apply          operations.ApplyStatus `json:"apply"`
	}
	if status := decode[planStatus](t, s.do("GET", "/api/v1/plans/"+done.PlanID, "")); status.ContextChanged || !status.Apply.Available {
		t.Fatalf("fresh plan %+v", status)
	}
	if w := s.do("PATCH", "/api/v1/projects/"+network.ID+"/execution-profiles/"+p.ID, `{"name":"dev","awsProfile":"prod-admin","expectedRevision":1}`); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if status := decode[planStatus](t, s.do("GET", "/api/v1/plans/"+done.PlanID, "")); !status.ContextChanged || status.Apply.Available {
		t.Fatalf("after edit %+v", status)
	}
	if w := s.do("POST", "/api/v1/plans/"+done.PlanID+"/approval", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "context_changed") {
		t.Fatalf("approval after edit: %d %s", w.Code, w.Body)
	}
}
