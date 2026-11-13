package projects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/profiles"
)

// profileFixture is toolsFixture plus initialization and a profile store, with the fake
// engine logging TF_WORKSPACE, AWS_PROFILE, AWS_REGION and AWS_SECRET_ACCESS_KEY per call.
func profileFixture(t *testing.T) (*Hub, map[string]string, string) {
	t.Helper()
	hub, ids, spy := toolsFixture(t)
	for _, key := range []string{"TF_CLI_ARGS_init", "HCLARO_FAKE_INIT", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	engines := hub.Engines
	hub.Initers = func(root, preference string, environ []string) (string, string, engine.Initer, error) {
		name, executable, adapter, err := engines(root, preference, "", environ)
		if err != nil {
			return "", "", nil, err
		}
		return name, executable, adapter.(*engine.CLI), nil
	}
	t.Setenv("HCLARO_FAKE_SPY_ENV", "AWS_PROFILE,AWS_REGION,AWS_SECRET_ACCESS_KEY")
	hub.Profiles = profiles.Open(t.TempDir(), time.Now())
	return hub, ids, spy
}

func spyContains(t *testing.T, spy string, parts ...string) bool {
	t.Helper()
	for _, line := range spyLines(t, spy) {
		ok := true
		for _, part := range parts {
			ok = ok && strings.Contains(line, part)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestTwoTabsOfOneProjectUseDifferentProfiles(t *testing.T) {
	hub, ids, spy := profileFixture(t)
	t.Setenv("AWS_SECRET_ACCESS_KEY", fixtures.Canary)
	network := ids["network"]
	dev, err := hub.Profiles.Create(network, profiles.Input{Name: "dev", AWSProfile: "dev-sso", AWSRegion: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := hub.Profiles.Create(network, profiles.Input{Name: "ops", AWSProfile: "ops-sso"})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := hub.OpenWith(network, "dev", dev.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := hub.OpenWith(network, "dev", ops.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := hub.OpenWith(network, "dev", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.ID == plain.ID {
		t.Fatal("tabs with different profiles share a context")
	}
	for _, c := range []*Context{a, b, plain} {
		if s := waitLoaded(t, c); s.Status != "ready" {
			t.Fatalf("%s: %+v", c.ID, s.Diagnostic)
		}
	}
	if a.Execution == nil || a.Execution.AWSProfile != "dev-sso" || a.Execution.Workspace != "dev" || b.Execution.AWSProfile != "ops-sso" || plain.Execution != nil {
		t.Fatalf("contexts %+v %+v %+v", a.Execution, b.Execution, plain.Execution)
	}
	if !spyContains(t, spy, "AWS_PROFILE=dev-sso", "AWS_REGION=eu-west-1", "AWS_SECRET_ACCESS_KEY=<unset>", "show") || !spyContains(t, spy, "AWS_PROFILE=ops-sso", "AWS_SECRET_ACCESS_KEY=<unset>", "show") {
		t.Fatalf("profile environments not used:\n%s", strings.Join(spyLines(t, spy), "\n"))
	}
	// Without a profile the inherited credential chain is untouched.
	if !spyContains(t, spy, "AWS_PROFILE=<unset>", "AWS_SECRET_ACCESS_KEY="+fixtures.Canary) {
		t.Fatal("the unprofiled context changed the inherited environment")
	}
	body, _ := json.Marshal([]*Context{a, b, plain})
	if strings.Contains(string(body), fixtures.Canary) {
		t.Fatalf("context response leaks: %s", body)
	}
	// Tools in each tab use that tab's profile.
	runTool(t, hub, network, b, engine.ToolProviders, false)
	if !spyContains(t, spy, "TF_WORKSPACE=dev", "AWS_PROFILE=ops-sso", "providers") {
		t.Fatal("tool did not use the tab's profile")
	}
	op, _, _ := hub.Inits.Operation(hub.Inits.List(network)[0].ID)
	if op.Context == nil || op.Context.ProfileID != ops.ID {
		t.Fatalf("tool operation context %+v", op.Context)
	}
}

func TestProfileWorkspaceAndProjectAreEnforced(t *testing.T) {
	hub, ids, _ := profileFixture(t)
	network, platform := ids["network"], ids["platform"]
	prod, _ := hub.Profiles.Create(network, profiles.Input{Name: "prod", Workspace: "prod"})
	var refusal *ProfileRefusal
	if _, _, err := hub.OpenWith(network, "dev", prod.ID, false); !errors.As(err, &refusal) || refusal.Code != "profile_workspace" {
		t.Fatalf("workspace mismatch: %v", err)
	}
	if c, _, err := hub.OpenWith(network, "prod", prod.ID, false); err != nil || c.Execution.Workspace != "prod" {
		t.Fatalf("matching workspace: %v", err)
	}
	// A profile ID from another project is not found, even with the same label.
	if _, _, err := hub.OpenWith(platform, "default", prod.ID, false); !errors.As(err, &refusal) || refusal.Code != "profile_not_found" {
		t.Fatalf("cross-project profile: %v", err)
	}
	t.Setenv("TF_CLI_ARGS_show", "-no-color -lock=false")
	if _, _, err := hub.OpenWith(network, "prod", prod.ID, true); !errors.As(err, &refusal) || refusal.Code != "unsafe_cli_args" {
		t.Fatalf("unsafe inherited args: %v", err)
	}
}

func TestEditedProfileClosesContextsAndRefusesTools(t *testing.T) {
	hub, ids, _ := profileFixture(t)
	network := ids["network"]
	p, _ := hub.Profiles.Create(network, profiles.Input{Name: "dev", AWSProfile: "dev-sso"})
	c, _, err := hub.OpenWith(network, "dev", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, c)
	// Another Hclaro window edits the file directly: the open context no longer matches.
	if _, err := hub.Profiles.Update(network, p.ID, 1, profiles.Input{Name: "dev", AWSProfile: "prod-admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.StartTool(network, c.ID, engine.ToolProviders, false); !errors.Is(err, ErrContextChanged) {
		t.Fatalf("tool with an edited profile: %v", err)
	}
	// The API's edit path also closes the context with a reason.
	hub.ProfileChanged(network, p.ID, "profile_changed")
	if _, reason, err := hub.Context(c.ID); err == nil || reason != "profile_changed" {
		t.Fatalf("context still open: %q %v", reason, err)
	}
}

func TestInitBindsTheReviewedProfile(t *testing.T) {
	hub, ids, spy := profileFixture(t)
	legacy := ids["legacy"]
	p, _ := hub.Profiles.Create(legacy, profiles.Input{Name: "backend", AWSProfile: "state-admin", AWSRegion: "eu-central-1"})
	plain, err := hub.InitStatus(legacy)
	if err != nil || !plain.CanInit {
		t.Fatalf("%+v %v", plain, err)
	}
	bound, err := hub.InitStatusFor(legacy, p.ID)
	if err != nil || !bound.CanInit || bound.Execution == nil || bound.Execution.AWSProfile != "state-admin" {
		t.Fatalf("%+v %v", bound, err)
	}
	if bound.ContextID == plain.ContextID {
		t.Fatal("the profile is not part of the init context ID")
	}
	// The reviewed context ID cannot be used with another profile or none.
	if _, err := hub.StartInitWith(legacy, bound.ContextID, ""); !errors.Is(err, ErrContextChanged) {
		t.Fatalf("profile dropped after review: %v", err)
	}
	op, err := hub.StartInitWith(legacy, bound.ContextID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	done, _ := waitInit(t, hub, op.ID)
	if done.Status != operations.Succeeded || done.Context == nil || done.Context.ProfileName != "backend" {
		t.Fatalf("%+v", done)
	}
	if !spyContains(t, spy, "AWS_PROFILE=state-admin", "AWS_REGION=eu-central-1", "init") {
		t.Fatalf("init environment:\n%s", strings.Join(spyLines(t, spy), "\n"))
	}
}

func TestLaunchBinder(t *testing.T) {
	hub, ids, _ := profileFixture(t)
	network := ids["network"]
	summary, _ := hub.Summary(network)
	p, _ := hub.Profiles.Create(network, profiles.Input{Name: "dev", AWSProfile: "dev-sso"})
	binder := hub.LaunchBinder()
	if _, err := binder(p.ID); err == nil {
		t.Fatal("bound without a registered launch project")
	}
	hub.Launch = summary.RootPath
	b, err := binder(p.ID)
	if err != nil || b.Context.ProjectID != network || b.Context.ProfileID != p.ID {
		t.Fatalf("%+v %v", b, err)
	}
	found := false
	for _, item := range b.Environ {
		found = found || item == "AWS_PROFILE=dev-sso"
	}
	if !found {
		t.Fatalf("environment %v", b.Environ)
	}
	var rejected *operations.RejectError
	if _, err := binder("ffffffffffffffffffffffffffffffff"); !errors.As(err, &rejected) || rejected.Code != "profile_not_found" {
		t.Fatalf("unknown profile: %v", err)
	}
	// Unregistering the project removes its profiles.
	if err := hub.Remove(network); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := hub.Profiles.List(network); len(list) != 0 {
		t.Fatalf("profiles of a removed project: %+v", list)
	}
}

// otherSpelling returns another path of dir: its upper-case name on a case-insensitive file
// system, otherwise a symbolic link. Hclaro may be launched from either.
func otherSpelling(t *testing.T, dir string) string {
	t.Helper()
	upper := filepath.Join(filepath.Dir(dir), strings.ToUpper(filepath.Base(dir)))
	if upper != dir && sameDir(upper, dir) {
		return upper
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip("no symbolic links and a case-sensitive file system")
	}
	return link
}

func TestLaunchUnderAnotherSpellingIsTheRegisteredProject(t *testing.T) {
	hub, ids, _ := profileFixture(t)
	network := ids["network"]
	summary, _ := hub.Summary(network)
	hub.Launch = otherSpelling(t, summary.RootPath)
	if got := hub.LaunchID(); got != network {
		t.Fatalf("launch ID %q, want %q", got, network)
	}
	if s, _ := hub.Summary(network); !s.Launch {
		t.Fatal("registered project not flagged as the launch project")
	}
	store := &history.Store{}
	hub.UseHistory(store)
	if id, _, ok := store.Project(hub.Launch); !ok || id != network {
		t.Fatalf("history project %q %v", id, ok)
	}
	// A launch plan's claim makes the Hub report the project busy and refuse its init.
	release, _ := hub.Locks.Acquire(hub.Launch, "plan")
	defer release()
	if s, _ := hub.Summary(network); s.Busy != "plan" {
		t.Fatalf("busy %q", s.Busy)
	}
	if _, ok := hub.Locks.Acquire(summary.RootPath, "init"); ok {
		t.Fatal("Hub init claimed the root of a running launch plan")
	}
}
