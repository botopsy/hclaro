package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
)

const unflagged = "plain-unflagged-secret-7f3a"

type toolHarness struct {
	runner   *Inits
	root     string
	released atomic.Int64
}

func newToolHarness(t *testing.T) *toolHarness {
	t.Helper()
	h := &toolHarness{runner: NewInits(), root: t.TempDir()}
	t.Cleanup(func() { h.runner.Close(5 * time.Second) })
	return h
}

func (h *toolHarness) request(tool engine.Tool, fake *engine.Fake) ToolRequest {
	return ToolRequest{
		ProjectID: "p1", ContextID: "c1", Root: h.root, Engine: "terraform", Workspace: "dev", Tool: tool, Tools: fake,
		Environ:    []string{"AWS_SECRET_ACCESS_KEY=AKIAFAKESECRETVALUE123"},
		Files:      map[string]bool{"main.tf": true, "modules/db/main.tf": true},
		Folders:    3,
		Revalidate: func() string { return "" },
		Release:    func() { h.released.Add(1) },
	}
}

func (h *toolHarness) run(t *testing.T, req ToolRequest) (Operation, string) {
	t.Helper()
	op, err := h.runner.StartTool(req)
	if err != nil {
		t.Fatal(err)
	}
	done, events := waitInit(t, h.runner, op.ID)
	text := joined(events)
	// Nothing an operation exposes may carry a secret: the snapshot, the list and the log.
	b, _ := json.Marshal(struct {
		Op   Operation
		List []Operation
	}{done, h.runner.List("p1")})
	for _, secret := range []string{fixtures.Canary, unflagged, "AKIAFAKESECRETVALUE123"} {
		if strings.Contains(string(b)+text, secret) {
			t.Fatalf("%q leaked:\n%s\n%s", secret, b, text)
		}
	}
	if h.released.Load() == 0 {
		t.Fatal("root claim not released")
	}
	return done, text
}

func capture(stdout []byte, exit int, stderr string) engine.Capture {
	return engine.Capture{Stdout: stdout, Stderr: []byte(stderr), ExitCode: exit}
}

func TestValidateValidAndInvalid(t *testing.T) {
	h := newToolHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", ValidateOut: capture(fixtures.ValidateValid, 0, "")}
	op, text := h.run(t, h.request(engine.ToolValidate, fake))
	r := op.Tool
	if op.Kind != "tool" || op.Status != Succeeded || r.Outcome != "passed" || r.Valid == nil || !*r.Valid || r.Output != "json" || r.Warnings != 1 || r.Errors != 0 || *op.ExitCode != 0 {
		t.Fatalf("%+v %+v", op, r)
	}
	if r.Findings[0].File != "main.tf" || r.Findings[0].Line != 4 || r.Findings[0].Column != 3 || r.UsesWorkspace {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(text, "does not detect drift") || !slices.Equal(op.Command, []string{"terraform", "validate", "-json"}) {
		t.Fatalf("%s %v", text, op.Command)
	}

	fake.ValidateOut = capture(fixtures.ValidateInvalid, 1, "")
	op, _ = h.run(t, h.request(engine.ToolValidate, fake))
	r = op.Tool
	if op.Status != Failed || op.Diagnostic.Code != "validation_errors" || r.Outcome != "findings" || r.Valid == nil || *r.Valid || r.Errors != 3 || r.Warnings != 1 || *op.ExitCode != 1 {
		t.Fatalf("%+v %+v", op, r)
	}
	if r.Findings[1].File != "modules/db/main.tf" || r.Findings[1].Line != 12 || !strings.Contains(r.Findings[1].Detail, "withheld") {
		t.Fatalf("%+v", r.Findings[1])
	}
	if r.Findings[2].File != "" || r.Findings[2].Line != 0 {
		t.Fatalf("a path outside the root was shown: %+v", r.Findings[2])
	}

	// Plain text before the document is skipped.
	fake.ValidateOut = capture(append([]byte("Plain text printed first.\n"), fixtures.ValidateValid...), 0, "")
	if op, _ = h.run(t, h.request(engine.ToolValidate, fake)); op.Status != Succeeded || !*op.Tool.Valid {
		t.Fatalf("%+v", op)
	}
}

func TestValidateThatCannotRunIsNotValidated(t *testing.T) {
	h := newToolHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", ValidateOut: capture(fixtures.ValidateNoInit, 1, "")}
	op, _ := h.run(t, h.request(engine.ToolValidate, fake))
	if op.Status != Failed || op.Diagnostic.Code != "plugins_missing" || op.Tool.Outcome != "not_checked" || op.Tool.Valid != nil {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
	// Plain-text failure without JSON, classified from stderr.
	fake.ValidateOut = capture(nil, 1, "Error: Module not installed\npassword="+fixtures.Canary)
	op, text := h.run(t, h.request(engine.ToolValidate, fake))
	if op.Diagnostic.Code != "init_required" || op.Tool.Valid != nil || !strings.Contains(text, "withheld") {
		t.Fatalf("%+v %s", op, text)
	}
	if calls := fake.Calls(); slices.Contains(calls, "validate -no-color") {
		t.Fatalf("classified failure must not fall back: %v", calls)
	}
}

func TestValidateFallsBackToHumanOutput(t *testing.T) {
	h := newToolHarness(t)
	fake := &engine.Fake{
		VersionValue:     "1.9.8",
		ValidateOut:      capture(nil, 1, "flag provided but not defined: -json"),
		ValidateHumanOut: capture(nil, 1, "╷\n│ Error: Unsupported argument\n│\n│   on main.tf line 3, in resource \"aws_s3_bucket\" \"logs\":\n│    3:   bucket_name = \""+unflagged+"\"\n│\n│ An argument named \"bucket_name\" is not expected here.\n╵"),
	}
	op, _ := h.run(t, h.request(engine.ToolValidate, fake))
	r := op.Tool
	if r.Output != "human" || !slices.Equal(op.Command, []string{"terraform", "validate", "-no-color"}) || op.Diagnostic.Code != "validation_errors" || *r.Valid || r.Errors != 1 || r.Findings[0].File != "main.tf" || r.Findings[0].Line != 3 {
		t.Fatalf("%+v %+v", op, r)
	}
	fake.ValidateHumanOut = capture([]byte("Success! The configuration is valid.\n"), 0, "")
	if op, _ = h.run(t, h.request(engine.ToolValidate, fake)); op.Status != Succeeded || !*op.Tool.Valid {
		t.Fatalf("%+v", op)
	}
	// Unreadable in both formats: no conclusion.
	fake.ValidateHumanOut = capture([]byte("garbage"), 1, "")
	if op, _ = h.run(t, h.request(engine.ToolValidate, fake)); op.Diagnostic.Code != "output_unreadable" || op.Tool.Valid != nil || op.Tool.Outcome != "not_checked" {
		t.Fatalf("%+v", op)
	}
}

func TestFormatCheckListsOnlyEnumeratedFiles(t *testing.T) {
	h := newToolHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", FormatOut: capture([]byte("main.tf\n../outside.tf\nunknown.tf\n"), 3, "")}
	req := h.request(engine.ToolFormat, fake)
	req.Recursive, req.Skipped = true, []string{"linked"}
	op, text := h.run(t, req)
	f := op.Tool.Format
	if op.Diagnostic.Code != "unformatted" || op.Tool.Outcome != "findings" || !slices.Equal(f.Unformatted, []string{"main.tf"}) || f.Hidden != 2 || f.Checked != 2 || !f.Recursive || f.Skipped[0] != "linked" || *op.ExitCode != 3 {
		t.Fatalf("%+v %+v", op, f)
	}
	if !slices.Equal(op.Command, []string{"terraform", "fmt", "-check", "-no-color", "-recursive"}) || !strings.Contains(text, "Not in canonical format: main.tf") || !strings.Contains(text, "never rewrites") {
		t.Fatalf("%v %s", op.Command, text)
	}
	fake.FormatOut = capture(nil, 0, "")
	if op, _ = h.run(t, h.request(engine.ToolFormat, fake)); op.Status != Succeeded || op.Tool.Outcome != "passed" || op.Diagnostic != nil {
		t.Fatalf("%+v", op)
	}
	fake.FormatOut = capture(nil, 2, "╷\n│ Error: Invalid character\n│\n│   on main.tf line 1:\n╵")
	if op, _ = h.run(t, h.request(engine.ToolFormat, fake)); op.Diagnostic.Code != "format_error" || op.Tool.Outcome != "not_checked" || op.Tool.Errors != 1 {
		t.Fatalf("%+v", op)
	}
}

func TestProvidersTreeAndFailures(t *testing.T) {
	res, other := ParseProviders(fixtures.Providers)
	want := []ProviderRequirement{
		{Source: "registry.terraform.io/hashicorp/aws", Constraint: "~> 5.0"},
		{Source: "registry.terraform.io/hashicorp/random"},
		{Source: "registry.terraform.io/hashicorp/aws", Constraint: ">= 4.0", Module: "module.network"},
		{Source: "registry.terraform.io/hashicorp/aws", Module: "module.network.module.subnets"},
	}
	if !slices.Equal(res.Configuration, want) || len(res.State) != 1 || res.State[0].Module != "" || len(other) != 0 || res.Hidden != 0 {
		t.Fatalf("%+v %q", res, other)
	}
	res, _ = ParseProviders([]byte("Providers required by configuration:\n.\n├── provider[evil host/x/y]\n├── provider[registry.terraform.io/a/b] password=" + fixtures.Canary + "\n"))
	if len(res.Configuration) != 0 || res.Hidden == 0 {
		t.Fatalf("%+v", res)
	}

	h := newToolHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", ProvidersOut: capture(fixtures.Providers, 0, "")}
	op, _ := h.run(t, h.request(engine.ToolProviders, fake))
	if op.Status != Succeeded || len(op.Tool.Providers.Configuration) != 4 || !op.Tool.UsesWorkspace || op.Tool.Workspace != "dev" {
		t.Fatalf("%+v", op.Tool)
	}
	fake.ProvidersOut = capture([]byte("\nProviders required by configuration:\n.\n└── provider[registry.terraform.io/hashicorp/aws] ~> 5.0\n"), 1, "Error: Backend initialization required, please run \"terraform init\"")
	op, _ = h.run(t, h.request(engine.ToolProviders, fake))
	if op.Status != Failed || op.Diagnostic.Code != "init_required" || !op.Tool.Partial || len(op.Tool.Providers.Configuration) != 1 {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
	fake.ProvidersOut = capture(nil, 1, "Error: Failed to load plugin schemas")
	if op, _ = h.run(t, h.request(engine.ToolProviders, fake)); op.Diagnostic.Code != "plugins_missing" || op.Tool.Partial {
		t.Fatalf("%+v", op)
	}
}

func TestOutputsMetadataNeverLeaksValues(t *testing.T) {
	h := newToolHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", OutputsOut: capture(fixtures.Outputs, 0, "")}
	op, text := h.run(t, h.request(engine.ToolOutputs, fake))
	o := op.Tool.Outputs
	if op.Status != Succeeded || len(o.Items) != 4 || o.Hidden != 1 || !o.Items[2].Sensitive || !strings.Contains(text, "Values were discarded") {
		t.Fatalf("%+v %s", o, text)
	}
	fake.OutputsOut = capture(fixtures.Outputs, 1, "Error: Backend initialization required, please run \"terraform init\"\npassword="+fixtures.Canary)
	if op, _ = h.run(t, h.request(engine.ToolOutputs, fake)); op.Diagnostic.Code != "init_required" || op.Tool.Outputs != nil || *op.ExitCode != 1 {
		t.Fatalf("%+v", op)
	}
	fake.OutputsOut = capture([]byte("{"+fixtures.Canary), 0, "")
	if op, _ = h.run(t, h.request(engine.ToolOutputs, fake)); op.Diagnostic.Code != "output_unreadable" || op.Tool.Outputs != nil {
		t.Fatalf("%+v", op)
	}
}

func TestToolRefusalsCancellationAndConflicts(t *testing.T) {
	h := newToolHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", ToolErr: engine.ErrUnsafeArgs}
	if op, _ := h.run(t, h.request(engine.ToolValidate, fake)); op.Diagnostic.Code != "unsafe_cli_args" || op.ExitCode != nil {
		t.Fatalf("%+v", op)
	}
	req := h.request(engine.ToolValidate, &engine.Fake{VersionValue: "1.9.8"})
	req.Revalidate = func() string { return "path_changed" }
	if op, _ := h.run(t, req); op.Diagnostic.Code != "path_changed" {
		t.Fatalf("%+v", op)
	}
	if calls := req.Tools.(*engine.Fake).Calls(); slices.Contains(calls, "validate -json") {
		t.Fatalf("ran after a failed revalidation: %v", calls)
	}

	blocked := &engine.Fake{VersionValue: "1.9.8", BlockTool: true}
	op, err := h.runner.StartTool(h.request(engine.ToolProviders, blocked))
	if err != nil {
		t.Fatal(err)
	}
	released := h.released.Load()
	if _, err := h.runner.StartTool(h.request(engine.ToolProviders, blocked)); !errors.Is(err, ErrToolRunning) || h.released.Load() != released+1 {
		t.Fatalf("duplicate run: %v", err)
	}
	other := h.request(engine.ToolProviders, &engine.Fake{VersionValue: "1.9.8", ProvidersOut: capture(fixtures.Providers, 0, "")})
	other.ContextID, other.Workspace = "c2", "prod"
	if done, _ := h.run(t, other); done.Status != Succeeded || done.Tool.Workspace != "prod" {
		t.Fatalf("another context must run: %+v", done)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if cur, _, _ := h.runner.Operation(op.ID); cur.Stage == "Running providers" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.runner.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	done, _ := waitInit(t, h.runner, op.ID)
	if done.Status != Cancelled || done.Diagnostic.Code != "cancelled" || done.Tool.Outcome != "not_checked" || done.Tool.Providers != nil || done.Tool.Workspace != "dev" {
		t.Fatalf("%+v %+v", done, done.Tool)
	}
	if list := h.runner.List("p1"); list[0].ID != op.ID && list[1].ID != op.ID || len(h.runner.List("p2")) != 0 {
		t.Fatalf("history is per project, newest first: %d", len(list))
	}
}

func TestHistoryNeverEvictsRunningOperations(t *testing.T) {
	h := newToolHarness(t)
	blocked, err := h.runner.StartTool(h.request(engine.ToolProviders, &engine.Fake{VersionValue: "1.9.8", BlockTool: true}))
	if err != nil {
		t.Fatal(err)
	}
	quick := &engine.Fake{VersionValue: "1.9.8", ValidateOut: capture(fixtures.ValidateValid, 0, "")}
	for i := 0; i < maxInits+5; i++ {
		req := h.request(engine.ToolValidate, quick)
		req.ContextID = fmt.Sprintf("c%d", i)
		h.run(t, req)
	}
	if _, _, ok := h.runner.Operation(blocked.ID); !ok {
		t.Fatal("a running operation was evicted from the history")
	}
	if n := len(h.runner.List("p1")); n > maxInits {
		t.Fatalf("history holds %d operations", n)
	}
	if _, err := h.runner.Cancel(blocked.ID); err != nil {
		t.Fatal(err)
	}
	waitInit(t, h.runner, blocked.ID)
}
