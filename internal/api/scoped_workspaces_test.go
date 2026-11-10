package api

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
)

// Tests for M24.10: the Hub workspace is a per-command binding of each context, the CLI
// default changes only through a reviewed workspace command, and init and workspace
// commands share the root claim with Hub-scoped plans. Synthetic engine only.

// useFakeMutators points init and workspace commands at the synthetic engine, so they never
// resolve a real CLI from PATH.
func (s *scopedServer) useFakeMutators() {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	resolve := func(root, preference string, environ []string) *engine.CLI {
		if preference == "" {
			preference = "terraform"
		}
		return &engine.CLI{Name: preference, Executable: filepath.Join(s.bin, preference+exe), Root: root, IgnoreInheritedWorkspace: true, Environ: environ}
	}
	s.hub.Initers = func(root, preference string, environ []string) (string, string, engine.Initer, error) {
		c := resolve(root, preference, environ)
		return c.Name, c.Executable, c, nil
	}
	s.hub.WorkspaceManagers = func(root, preference string, environ []string) (string, string, engine.WorkspaceManager, error) {
		c := resolve(root, preference, environ)
		return c.Name, c.Executable, c, nil
	}
}

// commands lists the engine subcommands the synthetic CLI received, with TF_WORKSPACE.
func (s *scopedServer) commands() []string {
	s.t.Helper()
	b, err := os.ReadFile(s.spy)
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		fields, args := strings.Split(line, "\x00"), []string{}
		for _, f := range fields {
			if !strings.Contains(f, "=") || strings.HasPrefix(f, "-") {
				args = append(args, f)
			}
		}
		out = append(out, strings.Join(args, " ")+" @"+spyField(fields, "TF_WORKSPACE="))
	}
	return out
}

// cliDefault reads what the synthetic engine records as the folder's CLI default.
func cliDefault(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".terraform", "environment"))
	if os.IsNotExist(err) {
		return "<none>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHubWorkspaceIsAPerContextBinding(t *testing.T) {
	s := newScopedServer(t)
	s.useFakeMutators()
	root := s.roots["network"]
	before, inherited := cliDefault(t, root), os.Getenv("TF_WORKSPACE")

	// Two tabs of one project read and plan different workspaces.
	dev, prod := s.openLoaded("network", "dev", ""), s.openLoaded("network", "prod", "")
	for cid, ws := range map[string]string{dev: "dev", prod: "prod"} {
		if done := s.waitScoped(cid, s.plan(cid).ID); done.Status != operations.Succeeded {
			t.Fatalf("%s: %+v", ws, done)
		}
		calls := s.planCalls()
		if got := spyField(calls[len(calls)-1], "TF_WORKSPACE="); got != ws {
			t.Fatalf("plan of %s ran with TF_WORKSPACE=%q", ws, got)
		}
	}
	for _, c := range s.commands() {
		if strings.HasPrefix(c, "workspace select") || strings.HasPrefix(c, "workspace new") || strings.HasPrefix(c, "workspace delete") || strings.HasPrefix(c, "init") {
			t.Fatalf("navigation or plan ran %q", c)
		}
	}
	if got := cliDefault(t, root); got != before {
		t.Fatalf("CLI default changed from %q to %q", before, got)
	}
	if os.Getenv("TF_WORKSPACE") != inherited {
		t.Fatal("the process environment changed")
	}

	// Init and workspace commands reviewed before a scoped plan cannot start during it.
	reviewInit := s.initStatus(s.ids["network"])
	code, reviewSelect := s.review(s.ids["network"], "select", "prod")
	if !reviewInit.CanInit || code != 200 || !reviewSelect.CanRun {
		t.Fatalf("%+v %d %+v", reviewInit, code, reviewSelect)
	}
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	running := s.plan(dev)
	wantError(t, s.do("POST", "/api/v1/projects/"+s.ids["network"]+"/operations/init", fmt.Sprintf(`{"mode":"standard","expectedContextId":%q}`, reviewInit.ContextID)), 409, "project_busy")
	wantError(t, s.do("POST", "/api/v1/projects/"+s.ids["network"]+"/operations/workspace", fmt.Sprintf(`{"action":"select","name":"prod","expectedContextId":%q}`, reviewSelect.ContextID)), 409, "project_busy")
	// A review made while it runs explains why it cannot run.
	if st := s.initStatus(s.ids["network"]); st.CanInit || st.ReasonCode != "busy" {
		t.Fatalf("%+v", st)
	}
	if _, r := s.review(s.ids["network"], "new", "qa"); r.CanRun || r.Code != "busy" {
		t.Fatalf("%+v", r)
	}
	// Another project is not blocked.
	if _, r := s.review(s.ids["platform"], "new", "qa"); !r.CanRun {
		t.Fatalf("%+v", r)
	}
	s.do("POST", "/api/v1/contexts/"+dev+"/operations/"+running.ID+"/cancel", `{}`)
	s.waitScoped(dev, running.ID)
	t.Setenv("HCLARO_FAKE_PLAN", "")

	// A running init blocks scoped plans of the same root the other way around.
	t.Setenv("HCLARO_FAKE_INIT", "slow")
	reviewInit = s.initStatus(s.ids["network"])
	w := s.do("POST", "/api/v1/projects/"+s.ids["network"]+"/operations/init", fmt.Sprintf(`{"mode":"standard","expectedContextId":%q}`, reviewInit.ContextID))
	if w.Code != 202 {
		t.Fatalf("init: %d %s", w.Code, w.Body)
	}
	initOp := decode[operations.Operation](t, w)
	wantError(t, s.do("POST", "/api/v1/contexts/"+prod+"/plans", `{"acknowledgeBackendAccess":true}`), 409, "project_busy")
	s.do("POST", "/api/v1/operations/"+initOp.ID+"/cancel", `{}`)
	s.waitOp(initOp.ID)
	t.Setenv("HCLARO_FAKE_INIT", "")
	// An init closes the folder's open tabs; open dev again.
	wantError(t, s.do("GET", "/api/v1/contexts/"+dev, ""), 404, "context_closed")
	dev = s.openLoaded("network", "dev", "")

	// Setting the CLI default is a separate, reviewed command. It changes the folder's CLI
	// default only; each tab keeps planning its own workspace.
	code, reviewSelect = s.review(s.ids["network"], "select", "prod")
	if code != 200 || !reviewSelect.CanRun || reviewSelect.CLIDefault != "dev" {
		t.Fatalf("%d %+v", code, reviewSelect)
	}
	w = s.do("POST", "/api/v1/projects/"+s.ids["network"]+"/operations/workspace", fmt.Sprintf(`{"action":"select","name":"prod","expectedContextId":%q}`, reviewSelect.ContextID))
	if w.Code != 202 {
		t.Fatalf("select: %d %s", w.Code, w.Body)
	}
	if done := s.waitOp(decode[operations.Operation](t, w).ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	if got := cliDefault(t, root); got != "prod" {
		t.Fatalf("CLI default %q", got)
	}
	selects := 0
	for _, c := range s.commands() {
		if strings.HasPrefix(c, "workspace select") {
			selects++
			if c != "workspace select prod @<unset>" {
				t.Fatalf("%q", c)
			}
		}
	}
	if selects != 1 {
		t.Fatalf("%d selects", selects)
	}
	// The dev tab still plans dev; it never follows the CLI default.
	if done := s.waitScoped(dev, s.plan(dev).ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	calls := s.planCalls()
	if got := spyField(calls[len(calls)-1], "TF_WORKSPACE="); got != "dev" {
		t.Fatalf("dev tab planned %q after the CLI default changed", got)
	}
	if os.Getenv("TF_WORKSPACE") != inherited {
		t.Fatal("the process environment changed")
	}
}
