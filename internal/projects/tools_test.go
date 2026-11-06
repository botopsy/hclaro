package projects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
)

const unflaggedSecret = "plain-unflagged-secret-7f3a"

// toolsFixture is hubFixture with the project utilities running the synthetic CLI through
// the real adapter, so each tool is a real subprocess.
func toolsFixture(t *testing.T) (*Hub, map[string]string, string) {
	t.Helper()
	hub, ids, spy := hubFixture(t)
	for _, key := range []string{"TF_CLI_ARGS_validate", "TF_CLI_ARGS_fmt", "TF_CLI_ARGS_providers", "TF_CLI_ARGS_output", "TF_WORKSPACE", "HCLARO_FAKE_TOOL", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	engines := hub.Engines
	hub.Tools = func(root, preference string, environ []string, selected string) (string, string, engine.Tools, error) {
		name, executable, adapter, err := engines(root, preference, selected, environ)
		if err != nil {
			return "", "", nil, err
		}
		cli := adapter.(*engine.CLI)
		cli.Environ = environ
		return name, executable, cli, nil
	}
	return hub, ids, spy
}

// tree fingerprints every file below root, so tests can prove a tool wrote nothing.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum := info.Mode().String()
		if d.Type().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h := sha256.Sum256(b)
			sum += hex.EncodeToString(h[:]) + info.ModTime().String()
		}
		out[path] = sum
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func openContext(t *testing.T, hub *Hub, id, ws string) *Context {
	t.Helper()
	c, _, err := hub.Open(id, ws, false)
	if err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, c)
	return c
}

func runTool(t *testing.T, hub *Hub, id string, c *Context, tool engine.Tool, recursive bool) (operations.Operation, string) {
	t.Helper()
	op, err := hub.StartTool(id, c.ID, tool, recursive)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	done, log := waitInit(t, hub, op.ID)
	b, _ := json.Marshal(struct {
		Op   operations.Operation
		List []operations.Operation
	}{done, hub.Inits.List(id)})
	for _, secret := range []string{fixtures.Canary, unflaggedSecret, "AKIAFAKESECRETVALUE123"} {
		if strings.Contains(string(b)+log, secret) {
			t.Fatalf("%q leaked:\n%s\n%s", secret, b, log)
		}
	}
	return done, log
}

func TestToolsRunInTheContextWorkspaceAndWriteNothing(t *testing.T) {
	hub, ids, spy := toolsFixture(t)
	id := ids["network"]
	dir := root(t, hub, id)
	for ws, name := range map[string]string{"dev": "dev_only", "prod": "prod_only"} {
		if err := os.MkdirAll(filepath.Join(dir, ".fake-outputs"), 0o755); err != nil {
			t.Fatal(err)
		}
		doc := `{"` + name + `":{"sensitive":true,"type":"string","value":"` + fixtures.Canary + `"}}`
		if err := os.WriteFile(filepath.Join(dir, ".fake-outputs", ws+".json"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "variables.tf"), []byte("# fake:unformatted\nvariable  \"x\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_WORKSPACE", "prod")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AKIAFAKESECRETVALUE123")
	t.Setenv("HCLARO_FAKE_TOOL", "leak")
	dev, prod := openContext(t, hub, id, "dev"), openContext(t, hub, id, "prod")
	before := tree(t, dir)
	spyBefore := len(spyLines(t, spy))

	op, log := runTool(t, hub, id, dev, engine.ToolValidate, false)
	if op.Status != operations.Succeeded || !*op.Tool.Valid || op.Tool.Workspace != "dev" || op.Tool.UsesWorkspace || !strings.Contains(log, "TF_WORKSPACE is not passed") || !strings.Contains(log, "withheld") {
		t.Fatalf("%+v %+v\n%s", op, op.Tool, log)
	}
	op, _ = runTool(t, hub, id, dev, engine.ToolFormat, false)
	if op.Diagnostic.Code != "unformatted" || !slices.Equal(op.Tool.Format.Unformatted, []string{"variables.tf"}) || op.Tool.Format.Checked != 2 {
		t.Fatalf("%+v %+v", op, op.Tool.Format)
	}
	op, _ = runTool(t, hub, id, dev, engine.ToolProviders, false)
	if op.Status != operations.Succeeded || len(op.Tool.Providers.Configuration) != 4 || len(op.Tool.Providers.State) != 1 {
		t.Fatalf("%+v %+v", op, op.Tool.Providers)
	}
	op, _ = runTool(t, hub, id, dev, engine.ToolOutputs, false)
	if op.Status != operations.Succeeded || len(op.Tool.Outputs.Items) != 1 || op.Tool.Outputs.Items[0].Name != "dev_only" || !op.Tool.Outputs.Items[0].Sensitive {
		t.Fatalf("%+v", op.Tool.Outputs)
	}
	op, _ = runTool(t, hub, id, prod, engine.ToolOutputs, false)
	if op.Tool.Outputs.Items[0].Name != "prod_only" || op.Tool.Workspace != "prod" {
		t.Fatalf("%+v", op.Tool.Outputs)
	}

	got := []string{}
	for _, line := range spyLines(t, spy)[spyBefore:] {
		if !strings.Contains(line, "version") {
			got = append(got, strings.ReplaceAll(line, "\x00", " "))
		}
	}
	want := []string{
		"TF_WORKSPACE=<unset> validate -json",
		"TF_WORKSPACE=<unset> fmt -check -no-color",
		"TF_WORKSPACE=dev providers",
		"TF_WORKSPACE=dev output -json",
		"TF_WORKSPACE=prod output -json",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("argv:\n%s", strings.Join(got, "\n"))
	}
	if after := tree(t, dir); len(after) != len(before) {
		t.Fatalf("files were created or removed: %d → %d", len(before), len(after))
	} else {
		for path, sum := range before {
			if after[path] != sum {
				t.Fatalf("%s changed", path)
			}
		}
	}
	if os.Getenv("TF_WORKSPACE") != "prod" {
		t.Fatal("Hclaro's own environment changed")
	}
	if ops, _ := hub.Operations(id); len(ops) != 5 || ops[0].Tool.Tool != engine.ToolOutputs {
		t.Fatalf("history: %d", len(ops))
	}
}

func TestToolsStayBoundAcrossWorkspaceSwitchAndProjects(t *testing.T) {
	hub, ids, spy := toolsFixture(t)
	network, platform := ids["network"], ids["platform"]
	dev := openContext(t, hub, network, "dev")
	t.Setenv("HCLARO_FAKE_TOOL", "slow")
	slow, err := hub.StartTool(network, dev.ID, engine.ToolProviders, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HCLARO_FAKE_TOOL", "")
	// The UI switches the tab to prod and works in another project meanwhile.
	prod := openContext(t, hub, network, "prod")
	op, _ := runTool(t, hub, network, prod, engine.ToolOutputs, false)
	if op.Status != operations.Succeeded || op.Tool.Workspace != "prod" {
		t.Fatalf("%+v", op)
	}
	staging := openContext(t, hub, platform, "staging")
	if op, _ = runTool(t, hub, platform, staging, engine.ToolValidate, false); op.Status != operations.Succeeded || op.Command[0] != "tofu" {
		t.Fatalf("%+v", op)
	}
	// Tools share the root with each other but never with init, workspace commands or plans.
	if _, ok := hub.Locks.Acquire(root(t, hub, network), "init"); ok {
		t.Fatal("exclusive claim granted while a tool runs")
	}
	if _, err := hub.StartTool(network, dev.ID, engine.ToolProviders, false); !errors.Is(err, operations.ErrToolRunning) {
		t.Fatalf("duplicate: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if strings.Contains(strings.Join(spyLines(t, spy), "\n"), "TF_WORKSPACE=dev\x00providers") || time.Now().After(deadline) {
			break
		}
	}
	if _, err := hub.Inits.Cancel(slow.ID); err != nil {
		t.Fatal(err)
	}
	done, log := waitInit(t, hub, slow.ID)
	if done.Status != operations.Cancelled || done.Tool.Workspace != "dev" || done.Tool.Providers != nil || !strings.Contains(log, "partial output was discarded") {
		t.Fatalf("%+v %+v\n%s", done, done.Tool, log)
	}
	release, ok := hub.Locks.Acquire(root(t, hub, network), "init")
	if !ok {
		t.Fatal("claim not released after cancel")
	}
	// While init holds the root, tools are refused.
	if _, err := hub.StartTool(network, prod.ID, engine.ToolValidate, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("tool during init: %v", err)
	}
	release()
}

func TestToolRefusalsRunNothing(t *testing.T) {
	hub, ids, spy := toolsFixture(t)
	network := ids["network"]
	dev := openContext(t, hub, network, "dev")
	staging := openContext(t, hub, ids["platform"], "staging")
	before := len(spyLines(t, spy))
	var refusal *ToolRefusal
	for name, err := range map[string]error{
		"unknown context": func() error {
			_, err := hub.StartTool(network, "0123456789abcdef0123456789abcdef", engine.ToolValidate, false)
			return err
		}(),
		"other project": func() error { _, err := hub.StartTool(network, staging.ID, engine.ToolValidate, false); return err }(),
		"invalid tool":  func() error { _, err := hub.StartTool(network, dev.ID, "fmt", false); return err }(),
		"unknown project": func() error {
			_, err := hub.StartTool("0123456789abcdef0123456789abcdef", dev.ID, engine.ToolValidate, false)
			return err
		}(),
	} {
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	t.Setenv("TF_CLI_ARGS_validate", "-no-color -json=false")
	if _, err := hub.StartTool(network, dev.ID, engine.ToolValidate, false); !errors.As(err, &refusal) || refusal.Code != "unsafe_cli_args" {
		t.Fatalf("%v", err)
	}
	t.Setenv("TF_CLI_ARGS_validate", "")
	t.Setenv("TF_CLI_ARGS", "-chdir=/tmp")
	if _, err := hub.StartTool(network, dev.ID, engine.ToolOutputs, false); !errors.As(err, &refusal) || refusal.Code != "unsafe_cli_args" {
		t.Fatalf("%v", err)
	}
	t.Setenv("TF_CLI_ARGS", "")
	if lines := spyLines(t, spy); len(lines) != before {
		t.Fatalf("refusals ran commands: %q", lines[before:])
	}
	// Uninitialized project: validate cannot run and says so; Init is offered in the UI.
	legacy := openContext(t, hub, ids["legacy"], "default")
	t.Setenv("HCLARO_FAKE_TOOL", "noinit")
	op, _ := runTool(t, hub, ids["legacy"], legacy, engine.ToolValidate, false)
	if op.Diagnostic.Code != "plugins_missing" || op.Tool.Valid != nil || op.Tool.Outcome != "not_checked" {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
	op, _ = runTool(t, hub, ids["legacy"], legacy, engine.ToolOutputs, false)
	if op.Diagnostic.Code != "init_required" || op.Tool.Outputs != nil {
		t.Fatalf("%+v", op)
	}
	op, _ = runTool(t, hub, ids["legacy"], legacy, engine.ToolProviders, false)
	if op.Diagnostic.Code != "init_required" || !op.Tool.Partial || len(op.Tool.Providers.Configuration) != 1 {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
	t.Setenv("HCLARO_FAKE_TOOL", "nojson")
	if op, _ = runTool(t, hub, network, dev, engine.ToolValidate, false); op.Tool.Output != "human" || op.Status != operations.Succeeded {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
}

func TestFormatScopeNeverFollowsLinkedFolders(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the synthetic engine")
	}
	hub, ids, _ := toolsFixture(t)
	id := ids["network"]
	dir := root(t, hub, id)
	outside := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "modules", "db", "main.tf"), "# fake:unformatted\n")
	write(filepath.Join(dir, ".hidden", "x.tf"), "# fake:unformatted\n")
	write(filepath.Join(outside, "evil.tf"), "# fake:unformatted\n")
	c := openContext(t, hub, id, "dev")

	op, _ := runTool(t, hub, id, c, engine.ToolFormat, false)
	if op.Status != operations.Succeeded || op.Tool.Format.Recursive || op.Tool.Format.Checked != 1 {
		t.Fatalf("non-recursive must not descend: %+v %+v", op, op.Tool.Format)
	}
	op, _ = runTool(t, hub, id, c, engine.ToolFormat, true)
	if !slices.Equal(op.Tool.Format.Unformatted, []string{"modules/db/main.tf"}) || op.Tool.Format.Checked != 2 || !op.Tool.Format.Recursive || op.Tool.Format.Folders != 3 {
		t.Fatalf("%+v", op.Tool.Format)
	}
	if runtime := os.Symlink(outside, filepath.Join(dir, "linked")); runtime != nil {
		t.Skip("symbolic links unavailable")
	}
	var refusal *ToolRefusal
	if _, err := hub.StartTool(id, c.ID, engine.ToolFormat, true); !errors.As(err, &refusal) || refusal.Code != "symlinked_directory" || !strings.Contains(refusal.Message, "linked") {
		t.Fatalf("%v", err)
	}
	op, _ = runTool(t, hub, id, c, engine.ToolFormat, false)
	if !slices.Equal(op.Tool.Format.Skipped, []string{"linked"}) {
		t.Fatalf("%+v", op.Tool.Format)
	}
	if err := os.Remove(filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "evil.tf"), filepath.Join(dir, "evil.tf")); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.StartTool(id, c.ID, engine.ToolFormat, false); !errors.As(err, &refusal) || refusal.Code != "symlink_outside_root" {
		t.Fatalf("%v", err)
	}
}

func TestFormatScopeFollowsEngineFileTypes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"main.tf", "main.tofu", "x.tftest.hcl", "x.tofutest.hcl", "vars.tfvars", "notes.md", "main.tf~", "#main.tf#", ".hidden.tf"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tf, _, _, refusal := FormatScope(dir, "terraform", false)
	tofu, folders, _, _ := FormatScope(dir, "tofu", false)
	if refusal != nil || len(tf) != 3 || len(tofu) != 5 || folders != 1 || !tofu["main.tofu"] || tf["main.tofu"] || tf["main.tf~"] || tf[".hidden.tf"] {
		t.Fatalf("terraform %v, tofu %v", tf, tofu)
	}
}
