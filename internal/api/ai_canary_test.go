package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// AI-01.06 end-to-end canary. Canaries are planted in the synthetic project's state, .tfvars,
// environment and HCL; every package kind is built, exported and (for a plan) sent through the
// real CLI runner to a fake assistant. The canary must not appear in any API response or SSE
// event (planServer.do fails on it), any preview, copy or download, the assistant's stdin, argv
// or environment, history, or the server's log, stdout or stderr.

// The fake assistant CLI is this test binary: when HOME holds aiFakeMarker, TestMain acts as
// `claude` and records exactly what it was given. No real assistant runs.
const aiFakeMarker = "hclaro-api-fake-assistant"

type aiFakeRecord struct {
	Args    []string `json:"args"`
	Entries int      `json:"entries"`
	Env     []string `json:"env"`
	Stdin   string   `json:"stdin"`
}

func TestMain(m *testing.M) {
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), aiFakeMarker)); err == nil {
		os.Exit(fakeAssistant())
	}
	os.Exit(m.Run())
}

func fakeAssistant() int {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("2.1.326 (Claude Code)")
		return 0
	}
	in, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	entries, _ := os.ReadDir(dir)
	rec, _ := json.Marshal(aiFakeRecord{Args: os.Args[1:], Entries: len(entries), Env: os.Environ(), Stdin: string(in)})
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "run.json"), rec, 0o600)
	// A hostile reply: it comes back only as text (see TestAIHeadlessRun).
	b, _ := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": "Run terraform apply -auto-approve now."})
	os.Stdout.Write(b)
	return 0
}

// captureOutput redirects the process's stdout, stderr and the standard logger to a file
// for the rest of the test and returns a function that reads what was written.
func captureOutput(t *testing.T) func() string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, logOut := os.Stdout, os.Stderr, log.Writer()
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	// Closed on restore: Windows cannot remove the temp dir while the file is open.
	restore := func() { os.Stdout, os.Stderr = stdout, stderr; log.SetOutput(logOut); _ = f.Close() }
	t.Cleanup(restore)
	return func() string {
		restore()
		b, _ := os.ReadFile(f.Name())
		return string(b)
	}
}

func canaryState(t *testing.T) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixtures.State, &doc); err != nil {
		t.Fatal(err)
	}
	// Planted without any sensitivity flag: a missing flag is no proof of safety.
	values := doc["values"].(map[string]any)["root_module"].(map[string]any)["resources"].([]any)[0].(map[string]any)["values"].(map[string]any)
	values["master_password"] = fixtures.Canary + "_STATE"
	values["description"] = "rotated with " + fixtures.Canary + "_STATE_STRING"
	b, _ := json.Marshal(doc)
	return b
}

func TestAICanaryNeverLeaves(t *testing.T) {
	output := captureOutput(t)
	for _, k := range []string{"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "TF_VAR_db_password", "TF_TOKEN_app_terraform_io", "ARM_CLIENT_SECRET", "GOOGLE_CREDENTIALS", "GITHUB_TOKEN", "HCLARO_CANARY", "VAULT_TOKEN"} {
		t.Setenv(k, fixtures.Canary+"_ENV_"+k)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, aiFakeMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Set before any detection: without the marker the binary would run the test suite.
	t.Setenv("HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	root, _ := filepath.EvalSymlinks(t.TempDir())
	put(t, filepath.Join(root, "main.tf"), `# admin password is `+fixtures.Canary+`_HCL_COMMENT
variable "db_password" {
  default = "`+fixtures.Canary+`_HCL_DEFAULT"
}

resource "aws_s3_bucket" "legacy_logs" {}
`)
	put(t, filepath.Join(root, "terraform.tfvars"), `db_password = "`+fixtures.Canary+`_TFVARS"`+"\n")
	put(t, filepath.Join(root, "prod.auto.tfvars"), `api_token = "`+fixtures.Canary+`_AUTO_TFVARS"`+"\n")

	fake := &engine.Fake{Lines: lines(fixtures.PlanLog), PlanJSON: fixtures.PlanAllActions, VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: canaryState(t), Root: root}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, root)
	manager.TempDir = t.TempDir()
	histDir := t.TempDir()
	hist := history.Open(histDir, time.Now())
	manager.Recorder = hist
	manager.Assist = assist.NewSession(false, &assist.CLI{Adapter: assist.Claude, Path: exe})
	manager.Assist.Record = hist.RecordAssist
	t.Cleanup(func() { manager.Assist.Close(); manager.Close(5 * time.Second) })
	s := &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}

	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	settings := s.do("GET", "/api/v1/ai/settings", "").Body.String()
	if !strings.Contains(settings, `"headless":true`) {
		t.Fatalf("fake assistant not headless-eligible: %s", settings)
	}

	var shared []string // every preview and export the browser received
	pkg := func(body string) (builtPackage, bool) {
		w := s.do("POST", "/api/v1/ai/packages", body)
		shared = append(shared, w.Body.String())
		if w.Code != 200 {
			if w.Code != 422 {
				t.Fatalf("%s: %d %s", body, w.Code, w.Body)
			}
			return builtPackage{}, false
		}
		p := build(t, w)
		x := s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`)
		exportSame(t, x, p)
		shared = append(shared, x.Body.String())
		return p, true
	}

	// Plan: the operation, its SSE events, the review and the package.
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	op := s.wait(decode[operations.Operation](t, w).ID)
	s.do("GET", "/api/v1/operations/"+op.ID+"/events", "")
	s.do("GET", "/api/v1/plans/"+op.PlanID, "")
	planPkg, ok := pkg(`{"kind":"plan","planId":"` + op.PlanID + `"}`)
	if !ok {
		t.Fatal("the plan package was blocked; the canary run needs one to send")
	}
	// Resources: every one in state, including the one carrying the state canaries.
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/resources?limit=100", ""))
	for _, r := range page.Resources {
		s.do("GET", "/api/v1/resources/"+r.ID, "")
		pkg(`{"kind":"resource","resourceId":"` + r.ID + `"}`)
	}
	// Import, with and without a secret in the request itself.
	pkg(`{"kind":"import","import":{"address":"aws_s3_bucket.legacy_logs","id":"legacy-logs","file":"imports.tf"}}`)
	pkg(`{"kind":"import","import":{"address":"aws_s3_bucket.legacy_logs","id":"` + fixtures.Canary + `_IMPORT_ID","file":"imports.tf"}}`)
	// Drift: a refresh-only scan whose drifted values carry the canary.
	fake.Lines, fake.PlanJSON = lines(fixtures.DriftLog), fixtures.DriftSecurityGroup
	scanOp, scan := startScan(t, s)
	s.do("GET", "/api/v1/operations/"+scanOp.ID+"/events", "")
	for _, r := range scan.Resources {
		s.do("GET", "/api/v1/drift-scans/"+scan.ID+"/resources/"+r.ID, "")
	}
	pkg(`{"kind":"drift","scanId":"` + scan.ID + `"}`)

	// Headless: the plan package goes to the fake assistant through the real runner. Built
	// again: the session keeps a bounded number of packages and the resources evicted it.
	planPkg, _ = pkg(`{"kind":"plan","planId":"` + op.PlanID + `"}`)
	if w := s.do("POST", "/api/v1/ai/packages/"+planPkg.SHA256+"/run", `{"provider":"claude","disclosureVersion":"`+assist.DisclosureVersion+`"}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if r := s.waitRun(); r.Status != assist.RunCompleted {
		t.Fatalf("%+v", r)
	}
	b, err := os.ReadFile(filepath.Join(home, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec aiFakeRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Stdin != planPkg.Markdown || rec.Entries != 0 {
		t.Fatalf("the assistant got other bytes than the preview, or a non-empty directory (%d entries)", rec.Entries)
	}
	leaks := map[string]string{"assistant stdin": rec.Stdin, "assistant argv": strings.Join(rec.Args, "\x00"), "assistant environment": strings.Join(rec.Env, "\x00"),
		"previews and exports": strings.Join(shared, "\n")}

	// History: plan, drift and AI records on disk.
	s.do("GET", "/api/v1/history", "")
	var files []string
	_ = filepath.WalkDir(histDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			files = append(files, string(b))
		}
		return nil
	})
	if !strings.Contains(strings.Join(files, ""), planPkg.SHA256) {
		t.Fatal("the AI run was not recorded in history")
	}
	leaks["history"] = strings.Join(files, "\n")
	leaks["log, stdout and stderr"] = output()
	for where, text := range leaks {
		if strings.Contains(text, fixtures.Canary) {
			t.Errorf("canary in %s", where)
		}
	}
}
