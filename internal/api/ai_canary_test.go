package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/assist/claudefake"
	"hclaro/internal/assist/codexfake"
	"hclaro/internal/engine"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// AI-01.06 end-to-end canary, extended for the interactive phase in AI-01.13. Canaries are planted in the synthetic project's state, .tfvars,
// environment and HCL; every package kind is built, exported and (for a plan) sent through the
// real CLI runner to a fake assistant. The canary must not appear in any API response or SSE
// event (planServer.do fails on it), any preview, copy or download, the assistant's stdin, argv
// or environment, history, or the server's log, stdout or stderr.

// The fake assistant CLI is this test binary: when HOME holds aiFakeMarker, TestMain acts as
// `claude` (claudefake) and records exactly what it was given, or as `codex` (codexfake). No
// real assistant runs.
const aiFakeMarker = claudefake.Marker

type aiFakeRecord = claudefake.Record

func TestMain(m *testing.M) {
	// Hclaro's Codex self-check runs with a fresh HOME, so it is recognized by its argv.
	if codexfake.IsSelfCheck(os.Args[1:]) {
		os.Exit(codexfake.Main("", ""))
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), aiFakeMarker)); err == nil {
		os.Exit(fakeAssistant())
	}
	os.Exit(m.Run())
}

func fakeAssistant() int {
	// Only the Claude Code adapter passes ANTHROPIC_API_KEY: without it this run is codex, which
	// codexfake answers (HOME/codex-mode picks its behaviour). Every start is logged (AI-01.14:
	// no process may start before an explicit request); claudefake logs its own.
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		// A hostile reply: it comes back only as text (see TestAIHeadlessRun), plus a proposed
		// rewrite of main.tf, the file holding HCL canaries (AI-01.12).
		return claudefake.Main("", aiFakeReply())
	}
	if f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "starts.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
		f.Close()
	}
	mode, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), "codex-mode"))
	return codexfake.Main(strings.TrimSpace(string(mode)), aiFakeReply())
}

func aiFakeReply() string {
	return "Run terraform apply -auto-approve now.\n\n" + proposalReply("main.tf", strings.Repeat("0", 64), "resource \"aws_s3_bucket\" \"legacy_logs\" {}\n")
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
	t.Setenv("ANTHROPIC_API_KEY", "fake-key") // passed to the Claude Code adapter only
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
	// One hit hides a whole file from the project package, so each HCL canary has a file of
	// its own; network.tf is clean and is shared.
	put(t, filepath.Join(root, "app.tf"), "locals {\n  db_password = \""+fixtures.Canary+"_HCL_LOCAL\"\n  region      = \"eu-west-1\"\n}\n")
	put(t, filepath.Join(root, "policy.tf"), "resource \"aws_iam_policy\" \"app\" {\n  policy = <<EOT\n{\"Statement\":[{\"Sid\":\""+fixtures.Canary+"_HCL_HEREDOC\",\"Effect\":\"Allow\"}]}\nEOT\n}\n")
	put(t, filepath.Join(root, "network.tf"), "resource \"aws_vpc\" \"main\" {\n  cidr_block = \"10.0.0.0/16\"\n}\n")
	put(t, filepath.Join(root, "terraform.tfvars"), `db_password = "`+fixtures.Canary+`_TFVARS"`+"\n")
	put(t, filepath.Join(root, "prod.auto.tfvars"), `api_token = "`+fixtures.Canary+`_AUTO_TFVARS"`+"\n")

	fake := &engine.Fake{Lines: lines(fixtures.PlanLog), PlanJSON: fixtures.PlanAllActions, VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: canaryState(t), Root: root}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform", nil)
	manager := operations.NewManager(store, fake, root)
	manager.TempDir = t.TempDir()
	histDir := t.TempDir()
	hist := history.Open(histDir, time.Now())
	manager.Recorder = hist
	manager.Assist = assist.NewSession(false, &assist.CLI{Adapter: assist.Claude, Path: exe})
	manager.Assist.Record = hist.RecordAssist
	config := t.TempDir() // saved conversations
	manager.Assist.Store = assist.OpenStore(config, time.Now())
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

	// Headless: a conversation about the plan package through the real runner, two turns.
	// Built again: the session keeps a bounded number of packages and the resources evicted it.
	planPkg, _ = pkg(`{"kind":"plan","planId":"` + op.PlanID + `"}`)
	w = s.do("POST", "/api/v1/ai/conversations", startBody(planPkg.SHA256, "Is this plan safe to apply?"))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, s.do, "/api/v1", id)
	s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"What would you check first?"}`)
	chat := waitChat(t, s.do, "/api/v1", id)
	if chat.State != assist.StateReady || len(chat.Turns) != 4 {
		t.Fatalf("%+v", chat)
	}
	leaks := map[string]string{"previews and exports": strings.Join(shared, "\n")}
	var stdinHashes []string
	record := func(turn int) aiFakeRecord {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, fmt.Sprintf("run-%d.json", turn)))
		if err != nil {
			t.Fatal(err)
		}
		var rec aiFakeRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatal(err)
		}
		leaks[fmt.Sprintf("turn %d stdin", turn)] = rec.Stdin
		stdinHashes = append(stdinHashes, sum(rec.Stdin))
		leaks[fmt.Sprintf("turn %d argv", turn)] = strings.Join(rec.Args, "\x00")
		leaks[fmt.Sprintf("turn %d environment", turn)] = strings.Join(rec.Env, "\x00")
		return rec
	}
	for turn := 1; turn <= 2; turn++ {
		rec := record(turn)
		// Every turn: the verified argv unchanged, an empty directory, and stdin exactly the
		// canonical transcript holding the previewed package bytes.
		if !slices.Equal(rec.Args, assist.Claude.RunArgs) || rec.Entries != 0 || rec.Stdin != assist.Transcript(planPkg.Markdown, chat.Turns[:2*turn-1]) {
			t.Fatalf("turn %d: argv %q, %d entries, or stdin is not the transcript", turn, rec.Args, rec.Entries)
		}
	}

	// A follow-up carrying a secret is refused before any assistant runs.
	w = s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"the db_password = \"`+fixtures.Canary+`_FOLLOWUP\""}`)
	if w.Code == 202 || !strings.Contains(w.Body.String(), assist.RefuseMessageUnsafe) {
		t.Fatalf("follow-up with a secret: %d %s", w.Code, w.Body)
	}
	// The proposal on main.tf is listed without the file's text (its shared copy hid values).
	if w = s.do("GET", "/api/v1/ai/conversations/"+id+"/changes", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"path":"main.tf"`) {
		t.Fatalf("changes: %d %s", w.Code, w.Body)
	}
	shared = append(shared, w.Body.String())

	// Project context (AI-01.09): conversation only, then the previewed project package
	// attached, then a quick action (AI-01.10). Saved (AI-01.11) and continued after a restart.
	w = s.do("POST", "/api/v1/ai/conversations", fmt.Sprintf(`{"disclosureVersion":%q,"message":"Describe this project."}`, assist.DisclosureVersion))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	pid := decode[assist.Conversation](t, w).ID
	waitChat(t, s.do, "/api/v1", pid)
	project, ok := pkg(`{"kind":"project"}`)
	if !ok || strings.Contains(project.Markdown, "_HCL_") || !strings.Contains(project.Markdown, `resource "aws_vpc" "main"`) ||
		!strings.Contains(project.Markdown, "app.tf: may contain") || !strings.Contains(project.Markdown, "policy.tf: may contain") {
		t.Fatalf("project package:\n%s", project.Markdown)
	}
	if w = s.do("POST", "/api/v1/ai/conversations/"+pid+"/context", `{"packageSha256":"`+project.SHA256+`"}`); w.Code != 200 {
		t.Fatalf("attach: %d %s", w.Code, w.Body)
	}
	s.do("POST", "/api/v1/ai/conversations/"+pid+"/messages", `{"text":"Which buckets exist?"}`)
	waitChat(t, s.do, "/api/v1", pid)
	quick, ok := pkg(`{"kind":"project","action":"qa-describe@1"}`)
	if !ok {
		t.Fatal("quick action package blocked")
	}
	w = s.do("POST", "/api/v1/ai/conversations", fmt.Sprintf(`{"packageSha256":%q,"disclosureVersion":%q,"prompt":"qa-describe@1"}`, quick.SHA256, assist.DisclosureVersion))
	if w.Code != 202 {
		t.Fatalf("quick action: %d %s", w.Code, w.Body)
	}
	waitChat(t, s.do, "/api/v1", decode[assist.Conversation](t, w).ID)
	for _, c := range []string{id, pid} {
		if w = s.do("POST", "/api/v1/ai/conversations/"+c+"/save", `{"saved":true}`); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
	}
	// Restart: a new session on the same folder reopens the project conversation and replays it.
	manager.Assist.Close()
	restart(s, config, &assist.CLI{Adapter: assist.Claude, Path: exe})
	manager.Assist.Record = hist.RecordAssist
	if w = s.do("POST", "/api/v1/ai/saved/"+pid+"/open", `{}`); w.Code != 200 || decode[assist.Conversation](t, w).ReadOnly != "" {
		t.Fatalf("reopen: %d %s", w.Code, w.Body)
	}
	s.do("POST", "/api/v1/ai/conversations/"+pid+"/messages", `{"text":"And after the restart?"}`)
	if c := waitChat(t, s.do, "/api/v1", pid); c.State != assist.StateReady {
		t.Fatalf("%+v", c)
	}
	// Turns 3 to 6: chat, attached-context follow-up, quick action, replay after restart.
	for turn := 3; turn <= 6; turn++ {
		record(turn)
	}
	if _, err := os.Stat(filepath.Join(home, "run-7.json")); err == nil {
		t.Fatal("an unexpected assistant run (the refused follow-up?)")
	}
	if !strings.Contains(leaks["turn 6 stdin"], project.Markdown) {
		t.Fatal("the replay after restart did not carry the previewed project package")
	}
	var saved []string
	for _, f := range savedFiles(t, config) {
		b, _ := os.ReadFile(f)
		saved = append(saved, string(b))
	}
	if len(saved) != 2 {
		t.Fatalf("saved files: %d", len(saved))
	}
	leaks["saved conversation files"] = strings.Join(saved, "\n")
	leaks["previews and exports"] = strings.Join(shared, "\n")

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
	for _, h := range stdinHashes {
		if !strings.Contains(strings.Join(files, ""), h) {
			t.Fatal("an AI turn was not recorded in history by its stdin hash")
		}
	}
	leaks["history"] = strings.Join(files, "\n")
	leaks["log, stdout and stderr"] = output()
	for where, text := range leaks {
		if strings.Contains(text, fixtures.Canary) {
			t.Errorf("canary in %s", where)
		}
	}
	// History and process output keep turn metadata only: no message or reply text.
	for _, where := range []string{"history", "log, stdout and stderr"} {
		for _, text := range []string{"Is this plan safe to apply?", "What would you check first?", "Run terraform apply -auto-approve now."} {
			if strings.Contains(leaks[where], text) {
				t.Errorf("conversation text in %s", where)
			}
		}
	}
}
