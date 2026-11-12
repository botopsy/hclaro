package projects

import (
	"context"
	"errors"
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

// workspaceFixture is hubFixture with workspace commands running the synthetic CLI through
// the real adapter, so each command is a real subprocess.
func workspaceFixture(t *testing.T) (*Hub, map[string]string, string) {
	t.Helper()
	hub, ids, spy := hubFixture(t)
	for _, key := range []string{"TF_CLI_ARGS_workspace", "TF_WORKSPACE", "HCLARO_FAKE_WORKSPACE", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	engines := hub.Engines
	hub.WorkspaceManagers = func(root, preference string, environ []string) (string, string, engine.WorkspaceManager, error) {
		name, executable, adapter, err := engines(root, preference, "", environ)
		if err != nil {
			return "", "", nil, err
		}
		cli := adapter.(*engine.CLI)
		cli.Environ = environ
		return name, executable, cli, nil
	}
	return hub, ids, spy
}

// mutations returns the spy lines of workspace new/select/delete commands.
func mutations(t *testing.T, spy string) []string {
	out := []string{}
	for _, line := range spyLines(t, spy) {
		for _, a := range []string{"new", "select", "delete"} {
			if strings.Contains(line, "\x00workspace\x00"+a) {
				out = append(out, strings.ReplaceAll(line, "\x00", " "))
			}
		}
		if strings.Contains(line, "-force") {
			t.Fatalf("-force ran: %q", line)
		}
	}
	return out
}

func review(t *testing.T, hub *Hub, id string, action engine.WorkspaceAction, name string) WorkspaceReview {
	t.Helper()
	r, err := hub.ReviewWorkspace(context.Background(), id, action, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func run(t *testing.T, hub *Hub, id string, action engine.WorkspaceAction, name string) (operations.Operation, string) {
	t.Helper()
	r := review(t, hub, id, action, name)
	if !r.CanRun {
		t.Fatalf("%s %s refused: %s %s", action, name, r.Code, r.Reason)
	}
	op, err := hub.StartWorkspace(id, action, name, r.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	return waitInit(t, hub, op.ID)
}

func root(t *testing.T, hub *Hub, id string) string {
	p, err := hub.Registry.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.RootPath
}

func TestUISwitchingNeverChangesTheCLIDefault(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	network := ids["network"]
	dir := root(t, hub, network)
	before, _ := os.ReadFile(filepath.Join(dir, ".fake-workspaces"))
	dev, _, err := hub.Open(network, "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	prod, _, err := hub.Open(network, "prod", false)
	if err != nil {
		t.Fatal(err)
	}
	if d, p := waitLoaded(t, dev), waitLoaded(t, prod); d.Info.Workspace != "dev" || p.Info.Workspace != "prod" || d.Status != "ready" || p.Status != "ready" {
		t.Fatal("contexts not isolated")
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".fake-workspaces"))
	if string(before) != string(after) {
		t.Fatal("CLI default changed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".terraform", "environment")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".terraform/environment written by UI switching")
	}
	if m := mutations(t, spy); len(m) != 0 {
		t.Fatalf("%v", m)
	}
}

func TestCreateRunsExactlyWorkspaceNewWithoutInheritedWorkspace(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	t.Setenv("TF_WORKSPACE", "dev")
	t.Setenv("HCLARO_FAKE_WORKSPACE", "leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fakeAwsSecretValue0123456789abcdefXYZ")
	network := ids["network"]
	dev, _, _ := hub.Open(network, "dev", false)
	waitLoaded(t, dev)
	r := review(t, hub, network, engine.WorkspaceNew, "staging")
	if !r.CanRun || r.CLIDefault != "dev" || !slices.Equal(r.Command, []string{"terraform", "workspace", "new", "staging"}) || r.Backend != "local" || r.EngineVersion != "1.9.8" {
		t.Fatalf("%+v", r)
	}
	if len(r.Notices) == 0 || !strings.Contains(r.Notices[0], "TF_WORKSPACE") || strings.Contains(strings.Join(r.Notices, " "), "dev") {
		t.Fatalf("notices %v", r.Notices)
	}
	if m := mutations(t, spy); len(m) != 0 {
		t.Fatalf("review ran %v", m)
	}
	op, err := hub.StartWorkspace(network, engine.WorkspaceNew, "staging", r.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if busy, _ := hub.Locks.Holder(root(t, hub, network)); busy != "workspace" {
		// The claim may already be released if the command was very fast; it must not be another holder.
		if busy != "" {
			t.Fatal(busy)
		}
	}
	op, log := waitInit(t, hub, op.ID)
	if op.Status != operations.Succeeded || op.ExitCode == nil || *op.ExitCode != 0 || op.Workspace == nil || !op.Workspace.Verified || op.Workspace.CLIDefaultBefore != "dev" || op.Workspace.CLIDefaultAfter != "staging" || !slices.Contains(op.Workspace.Workspaces, "staging") {
		t.Fatalf("%+v %+v\n%s", op, op.Workspace, log)
	}
	if strings.Contains(log, fixtures.Canary) || strings.Contains(log, "fakeAwsSecretValue0123456789abcdefXYZ") {
		t.Fatalf("leak:\n%s", log)
	}
	if m := mutations(t, spy); !slices.Equal(m, []string{"TF_WORKSPACE=<unset> workspace new staging"}) {
		t.Fatalf("%q", m)
	}
	s, _ := hub.Summary(network)
	if s.Inspection == nil || s.Inspection.CLIDefault != "staging" || !slices.Contains(s.Inspection.Workspaces, "staging") || s.Busy != "" {
		t.Fatalf("%+v", s.Inspection)
	}
	// The open dev tab is unaffected; the new workspace opens with empty state.
	if _, _, err := hub.Context(dev.ID); err != nil {
		t.Fatal("dev context closed")
	}
	staging, _, err := hub.Open(network, "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, staging); snap.Info.Workspace != "staging" || len(snap.Index.Resources) != 0 {
		t.Fatalf("%+v", snap.Diagnostic)
	}
	if os.Getenv("TF_WORKSPACE") != "dev" {
		t.Fatal("Hclaro's environment changed")
	}
	// A review is single use.
	if _, err := hub.StartWorkspace(network, engine.WorkspaceNew, "staging", r.ContextID); !errors.Is(err, ErrContextChanged) {
		t.Fatal(err)
	}
}

func TestSelectThenDeleteEmptyWorkspace(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	platform := ids["platform"]
	dir := root(t, hub, platform)
	if op, log := run(t, hub, platform, engine.WorkspaceNew, "qa"); op.Status != operations.Succeeded {
		t.Fatalf("%s", log)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".terraform", "environment")); string(b) != "qa" {
		t.Fatalf("environment %q", b)
	}
	// qa is the CLI default now, so deleting it is refused before anything runs.
	if r := review(t, hub, platform, engine.WorkspaceDelete, "qa"); r.CanRun || r.Code != "workspace_active" {
		t.Fatalf("%+v", r)
	}
	op, log := run(t, hub, platform, engine.WorkspaceSelect, "staging")
	if op.Status != operations.Succeeded || op.Workspace.CLIDefaultBefore != "qa" || op.Workspace.CLIDefaultAfter != "staging" || !slices.Equal(op.Command, []string{"tofu", "workspace", "select", "staging"}) {
		t.Fatalf("%+v %+v\n%s", op, op.Workspace, log)
	}
	qa, _, _ := hub.Open(platform, "qa", false)
	waitLoaded(t, qa)
	r := review(t, hub, platform, engine.WorkspaceDelete, "qa")
	if !r.CanRun || r.Empty == nil || !*r.Empty {
		t.Fatalf("%+v", r)
	}
	op, log = waitInit(t, hub, mustStart(t, hub, platform, engine.WorkspaceDelete, "qa", r.ContextID))
	if op.Status != operations.Succeeded || slices.Contains(op.Workspace.Workspaces, "qa") || op.Workspace.CLIDefaultAfter != "staging" {
		t.Fatalf("%+v\n%s", op.Workspace, log)
	}
	if _, reason, err := hub.Context(qa.ID); err == nil || reason != "workspace_deleted" {
		t.Fatalf("qa context %q", reason)
	}
	if m := mutations(t, spy); !slices.Equal(m, []string{"TF_WORKSPACE=<unset> workspace new qa", "TF_WORKSPACE=<unset> workspace select staging", "TF_WORKSPACE=<unset> workspace delete qa"}) {
		t.Fatalf("%q", m)
	}
	// The emptiness read used a child TF_WORKSPACE, never a selection.
	found := false
	for _, line := range spyLines(t, spy) {
		found = found || line == "TF_WORKSPACE=qa\x00show\x00-json"
	}
	if !found {
		t.Fatal("emptiness was not read for qa")
	}
}

func mustStart(t *testing.T, hub *Hub, id string, action engine.WorkspaceAction, name, contextID string) string {
	t.Helper()
	op, err := hub.StartWorkspace(id, action, name, contextID)
	if err != nil {
		t.Fatal(err)
	}
	return op.ID
}

func TestWorkspaceRefusalsRunNothing(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	network, legacy := ids["network"], ids["legacy"]
	for _, name := range []string{"-force", "a b", "../prod", "", "dev;x", strings.Repeat("x", 91)} {
		if _, err := hub.ReviewWorkspace(context.Background(), network, engine.WorkspaceNew, name); !errors.Is(err, ErrInvalidWorkspace) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := hub.ReviewWorkspace(context.Background(), network, "show", "dev"); !errors.Is(err, ErrInvalidAction) {
		t.Fatal(err)
	}
	if _, err := hub.ReviewWorkspace(context.Background(), "0123456789abcdef0123456789abcdef", engine.WorkspaceNew, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, c := range []struct {
		action engine.WorkspaceAction
		name   string
		code   string
	}{
		{engine.WorkspaceNew, "dev", "workspace_exists"},
		{engine.WorkspaceSelect, "qa", "workspace_missing"},
		{engine.WorkspaceSelect, "dev", "already_default"},
		{engine.WorkspaceDelete, "default", "workspace_default"},
		{engine.WorkspaceDelete, "dev", "workspace_active"},
		{engine.WorkspaceDelete, "prod", "workspace_not_empty"},
		{engine.WorkspaceDelete, "-dev", "unsafe_name"},
	} {
		if r := review(t, hub, network, c.action, c.name); r.CanRun || r.Code != c.code || r.ContextID != "" {
			t.Errorf("%s %s: %s %s", c.action, c.name, r.Code, r.Reason)
		}
	}
	if r := review(t, hub, legacy, engine.WorkspaceNew, "qa"); r.CanRun || r.Code != "uninitialized" {
		t.Fatalf("%+v", r)
	}
	t.Setenv("TF_CLI_ARGS_workspace", "-force")
	if r := review(t, hub, network, engine.WorkspaceNew, "qa"); r.Code != "unsafe_cli_args" {
		t.Fatalf("%+v", r)
	}
	os.Unsetenv("TF_CLI_ARGS_workspace")
	// The engines read the joined subcommand's variable for multi-word commands.
	for _, key := range []string{"TF_CLI_ARGS_workspace_new", "TF_CLI_ARGS_workspace_list", "tf_cli_args_workspace_new"} {
		t.Setenv(key, "-force")
		if r := review(t, hub, network, engine.WorkspaceNew, "qa"); r.Code != "unsafe_cli_args" {
			t.Fatalf("%s: %+v", key, r)
		}
		os.Unsetenv(key)
	}
	t.Setenv("TF_WORKSPACE", "prod")
	if r := review(t, hub, network, engine.WorkspaceDelete, "prod"); r.Code != "workspace_active" {
		t.Fatalf("%+v", r)
	}
	os.Unsetenv("TF_WORKSPACE")
	t.Setenv("TF_DATA_DIR", t.TempDir())
	if r := review(t, hub, network, engine.WorkspaceNew, "qa"); r.Code != "shared_data_dir" {
		t.Fatalf("%+v", r)
	}
	os.Unsetenv("TF_DATA_DIR")
	// Backends: remote-exec, unlisted and unreadable (artifactory → unknown) backends refuse every change; s3 refuses deletion.
	dir := root(t, hub, network)
	for _, c := range [][2]string{{"remote", "workspaces_unsupported"}, {"cloud", "workspaces_unsupported"}, {"http", "workspaces_unsupported"}, {"artifactory", "workspaces_unsupported"}, {"s3", "delete_unsupported"}} {
		backend, code := c[0], c[1]
		meta := `{"version":3,"backend":{"type":"` + backend + `","config":{}}}`
		if err := os.WriteFile(filepath.Join(dir, ".terraform", "terraform.tfstate"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
		action := engine.WorkspaceNew
		if backend == "s3" {
			action = engine.WorkspaceDelete
		}
		if r := review(t, hub, network, action, "qa"); r.CanRun || r.Code != code {
			t.Errorf("%s: %+v", backend, r)
		}
	}
	if r := review(t, hub, network, engine.WorkspaceNew, "qa"); !r.CanRun {
		t.Fatalf("s3 create refused: %+v", r)
	}
	os.Remove(filepath.Join(dir, ".terraform", "terraform.tfstate"))
	// Without a usable engine nothing is listed and nothing runs.
	hub.WorkspaceManagers = func(string, string, []string) (string, string, engine.WorkspaceManager, error) {
		return "", "", nil, errors.New("not found")
	}
	if r := review(t, hub, network, engine.WorkspaceNew, "qa"); r.Code != "missing_cli" {
		t.Fatalf("%+v", r)
	}
	if m := mutations(t, spy); len(m) != 0 {
		t.Fatalf("%v", m)
	}
}

func TestStartNeedsAMatchingFreshReview(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	network := ids["network"]
	r := review(t, hub, network, engine.WorkspaceNew, "qa")
	for _, c := range []struct {
		action engine.WorkspaceAction
		name   string
		id     string
	}{
		{engine.WorkspaceNew, "qa", strings.Repeat("0", 64)},
		{engine.WorkspaceNew, "other", r.ContextID},
		{engine.WorkspaceSelect, "qa", r.ContextID},
	} {
		if _, err := hub.StartWorkspace(network, c.action, c.name, c.id); !errors.Is(err, ErrContextChanged) {
			t.Fatalf("%v", err)
		}
	}
	if _, err := hub.StartWorkspace(ids["platform"], engine.WorkspaceNew, "qa", r.ContextID); !errors.Is(err, ErrContextChanged) {
		t.Fatal("review used for another project")
	}
	// An edited configuration changes the context.
	r = review(t, hub, network, engine.WorkspaceNew, "qa")
	dir := root(t, hub, network)
	if err := os.WriteFile(filepath.Join(dir, "extra.tf"), []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.StartWorkspace(network, engine.WorkspaceNew, "qa", r.ContextID); !errors.Is(err, ErrContextChanged) {
		t.Fatal(err)
	}
	// A terminal changes the CLI default between review and start: the runner sees a different
	// listing and runs nothing.
	r = review(t, hub, network, engine.WorkspaceNew, "qa")
	if err := os.WriteFile(filepath.Join(dir, ".fake-workspaces"), []byte("* default\n  dev\n  prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	op, log := waitInit(t, hub, mustStart(t, hub, network, engine.WorkspaceNew, "qa", r.ContextID))
	if op.Status != operations.Failed || op.Diagnostic.Code != "workspaces_changed" || op.Workspace.Ran {
		t.Fatalf("%+v\n%s", op, log)
	}
	// Expired reviews are refused.
	r = review(t, hub, network, engine.WorkspaceNew, "qa")
	now := hub.Now
	hub.Now = func() time.Time { return now().Add(reviewTTL + time.Minute) }
	if _, err := hub.StartWorkspace(network, engine.WorkspaceNew, "qa", r.ContextID); !errors.Is(err, ErrContextChanged) {
		t.Fatal(err)
	}
	if m := mutations(t, spy); len(m) != 0 {
		t.Fatalf("%v", m)
	}
}

func TestWorkspaceFailuresAndPartialSuccess(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	network := ids["network"]
	for mode, code := range map[string]string{"unsupported": "workspaces_unsupported", "locked": "state_locked", "credentials": "credentials"} {
		t.Setenv("HCLARO_FAKE_WORKSPACE", mode)
		op, log := run(t, hub, network, engine.WorkspaceNew, "qa")
		if op.Status != operations.Failed || op.Diagnostic == nil || op.Diagnostic.Code != code || op.ExitCode == nil || *op.ExitCode != 1 || !op.Workspace.Ran || op.Workspace.Verified {
			t.Fatalf("%s: %+v\n%s", mode, op, log)
		}
		if op.Workspace.CLIDefaultAfter != "dev" {
			t.Fatalf("%s: default after %q", mode, op.Workspace.CLIDefaultAfter)
		}
	}
	// Success followed by a failed listing is reported as unconfirmed, never as success, and
	// nothing is retried.
	t.Setenv("HCLARO_FAKE_WORKSPACE", "list-fails-after")
	op, log := run(t, hub, network, engine.WorkspaceNew, "qa")
	if op.Status != operations.Unknown || op.Diagnostic.Code != "unverified" || op.Workspace.Verified || op.Workspace.CLIDefaultAfter != "" {
		t.Fatalf("%+v\n%s", op, log)
	}
	if s, _ := hub.Summary(network); s.Inspection != nil {
		t.Fatal("stale inspection kept after an unconfirmed change")
	}
	// Workspaces cannot be listed now: reviews fail closed.
	if r := review(t, hub, network, engine.WorkspaceSelect, "prod"); r.CanRun || r.Code != "workspace_list" {
		t.Fatalf("%+v", r)
	}
	if m := mutations(t, spy); len(m) != 4 {
		t.Fatalf("%q", m)
	}
}

func TestWorkspaceCommandClaimsRootAndCancels(t *testing.T) {
	hub, ids, spy := workspaceFixture(t)
	network := ids["network"]
	other := review(t, hub, network, engine.WorkspaceSelect, "prod")
	t.Setenv("HCLARO_FAKE_WORKSPACE", "slow")
	r := review(t, hub, network, engine.WorkspaceNew, "qa")
	op, err := hub.StartWorkspace(network, engine.WorkspaceNew, "qa", r.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		current, _, _ := hub.Inits.Operation(op.ID)
		if current.Stage == "Running workspace new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := hub.StartWorkspace(network, engine.WorkspaceSelect, "prod", other.ContextID); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent select: %v", err)
	}
	if r := review(t, hub, network, engine.WorkspaceSelect, "prod"); r.Code != "busy" {
		t.Fatalf("%+v", r)
	}
	if _, err := hub.Inspect(context.Background(), network); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if s, _ := hub.Summary(network); s.Busy != "workspace" {
		t.Fatal(s.Busy)
	}
	// Another project is independent. The running command keeps its pinned environment.
	os.Unsetenv("HCLARO_FAKE_WORKSPACE")
	if op, log := run(t, hub, ids["platform"], engine.WorkspaceNew, "qa"); op.Status != operations.Succeeded {
		t.Fatalf("%s", log)
	}
	if _, err := hub.Inits.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	done, log := waitInit(t, hub, op.ID)
	if done.Status != operations.Cancelled || done.Diagnostic.Code != "cancelled" || !strings.Contains(done.Diagnostic.Message, "may or may not") {
		t.Fatalf("%+v\n%s", done, log)
	}
	if holder, _ := hub.Locks.Holder(root(t, hub, network)); holder != "" {
		t.Fatal("claim not released")
	}
	if n := len(mutations(t, spy)); n != 2 {
		t.Fatalf("%d", n)
	}
}
