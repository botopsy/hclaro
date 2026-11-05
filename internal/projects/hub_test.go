package projects

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

// fakeEngines builds the synthetic CLI (scripts/fake-engine) as terraform and tofu and
// returns a factory that uses them with the real CLI adapter.
func fakeEngines(t *testing.T) (EngineFactory, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the synthetic engine")
	}
	bin := t.TempDir()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	for _, name := range []string{"terraform", "tofu"} {
		build := exec.Command("go", "build", "-o", filepath.Join(bin, name+exe), "hclaro/scripts/fake-engine")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	spy := filepath.Join(t.TempDir(), "argv")
	t.Setenv("HCLARO_FAKE_SPY", spy)
	t.Setenv("HCLARO_FAKE_SPY_ENV", "1")
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_show", "TF_CLI_ARGS_workspace", "TF_DATA_DIR"} {
		t.Setenv(key, "")
	}
	return func(root, preference, selected string) (string, string, Adapter, error) {
		name := preference
		if name == "" {
			name = "terraform"
		}
		cli := &engine.CLI{Name: name, Executable: filepath.Join(bin, name+exe), Root: root, SelectedWorkspace: selected, IgnoreInheritedWorkspace: true}
		return name, cli.Executable, cli, nil
	}, spy
}

func hubFixture(t *testing.T) (*Hub, map[string]string, string) {
	t.Helper()
	engines, spy := fakeEngines(t)
	dir := t.TempDir()
	if err := fixtures.WriteHubProjects(dir); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(Open(t.TempDir(), now), "")
	hub.Engines = engines
	t.Cleanup(hub.Close)
	ids := map[string]string{}
	for _, p := range fixtures.HubProjects {
		s, err := hub.Add(filepath.Join(dir, p.Dir), "Same alias", p.Engine)
		if err != nil {
			t.Fatal(err)
		}
		ids[p.Dir] = s.ID
	}
	return hub, ids, spy
}

func waitLoaded(t *testing.T, c *Context) workspace.Snapshot {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if s := c.Snapshot(); s.Status != "loading" {
			return s
		}
	}
	t.Fatal("context did not load")
	return workspace.Snapshot{}
}

func addresses(s workspace.Snapshot) []string {
	out := []string{}
	for _, r := range s.Index.Resources {
		out = append(out, r.Address)
	}
	return out
}

func spyLines(t *testing.T, spy string) []string {
	b, err := os.ReadFile(spy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestWorkspaceContextsAreIsolatedWithoutSelectingWorkspaces(t *testing.T) {
	hub, ids, spy := hubFixture(t)
	// An inherited selection must not leak into Hub reads, nor be changed in Hclaro.
	t.Setenv("TF_WORKSPACE", "inherited")
	network := ids["network"]
	s, err := hub.Inspect(context.Background(), network)
	if err != nil || s.Inspection == nil || s.Inspection.Diagnostic != nil {
		t.Fatalf("%v %+v", err, s.Inspection)
	}
	if !slices.Equal(s.Inspection.Workspaces, []string{"default", "dev", "prod"}) || s.Inspection.CLIDefault != "dev" || s.Inspection.Engine != "terraform" || s.Inspection.Version != "1.9.8" {
		t.Fatalf("%+v", s.Inspection)
	}
	var dev, prod *Context
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); dev, _, _ = hub.Open(network, "dev", false) }()
	go func() { defer wg.Done(); prod, _, _ = hub.Open(network, "prod", false) }()
	wg.Wait()
	if dev == nil || prod == nil || dev.ID == prod.ID {
		t.Fatal("contexts not created")
	}
	devSnap, prodSnap := waitLoaded(t, dev), waitLoaded(t, prod)
	if devSnap.Status != "ready" || prodSnap.Status != "ready" {
		t.Fatalf("%+v %+v", devSnap.Diagnostic, prodSnap.Diagnostic)
	}
	if got := addresses(devSnap); !slices.Equal(got, []string{"aws_db_instance.dev_main", "aws_s3_bucket.dev_logs"}) {
		t.Fatalf("dev: %v", got)
	}
	if got := addresses(prodSnap); !slices.Contains(got, "aws_iam_role.prod_only") || slices.Contains(got, "aws_s3_bucket.dev_logs") {
		t.Fatalf("prod: %v", got)
	}
	if devSnap.Info.Workspace != "dev" || prodSnap.Info.Workspace != "prod" {
		t.Fatal("snapshot workspace mismatch")
	}
	caps := devSnap.Info.Capabilities
	if caps.CanPlan || caps.CanApply || caps.CanImport || caps.PlanReason != ReadOnlyReason {
		t.Fatalf("hub context can mutate: %+v", caps)
	}
	if os.Getenv("TF_WORKSPACE") != "inherited" {
		t.Fatal("Hclaro's own environment changed")
	}
	for _, line := range spyLines(t, spy) {
		fields := strings.Split(line, "\x00")
		env, argv := fields[0], strings.Join(fields[1:], " ")
		if strings.HasPrefix(argv, "workspace select") || strings.HasPrefix(argv, "workspace new") || strings.HasPrefix(argv, "init") {
			t.Fatalf("ran %q", argv)
		}
		switch argv {
		case "workspace list":
			if env != "TF_WORKSPACE=<unset>" {
				t.Fatalf("workspace list saw %s", env)
			}
		case "workspace show", "show -json":
			if env != "TF_WORKSPACE=dev" && env != "TF_WORKSPACE=prod" {
				t.Fatalf("%s saw %s", argv, env)
			}
		case "version -json":
			if env == "TF_WORKSPACE=inherited" {
				t.Fatal("inspection inherited TF_WORKSPACE")
			}
		default:
			t.Fatalf("unexpected command %q", argv)
		}
	}
	// The resource JSON clients receive never contains the planted secret.
	for _, snap := range []workspace.Snapshot{devSnap, prodSnap} {
		b, _ := json.Marshal(snap.Index.Resources)
		if strings.Contains(string(b), fixtures.Canary) {
			t.Fatal("canary leaked")
		}
	}
	again, reused, err := hub.Open(network, "dev", false)
	if err != nil || !reused || again.ID != dev.ID {
		t.Fatal("same context not reused")
	}
	fresh, reused, err := hub.Open(network, "dev", true)
	if err != nil || reused || fresh.ID == dev.ID {
		t.Fatal("reload did not create a new context")
	}
	if _, reason, err := hub.Context(dev.ID); !errors.Is(err, ErrContextNotFound) || reason != "reloaded" {
		t.Fatalf("old context still open: %s", reason)
	}
	waitLoaded(t, fresh)
}

func TestOpenTofuProjectAndUninitializedProject(t *testing.T) {
	hub, ids, _ := hubFixture(t)
	s, _ := hub.Inspect(context.Background(), ids["platform"])
	if s.Inspection.Engine != "tofu" || s.Inspection.Version != "1.8.3" || !slices.Equal(s.Inspection.Workspaces, []string{"default", "staging"}) {
		t.Fatalf("%+v", s.Inspection)
	}
	c, _, err := hub.Open(ids["platform"], "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, c); snap.Status != "ready" || !slices.Contains(addresses(snap), "aws_s3_bucket.staging_logs") {
		t.Fatalf("%+v", snap.Diagnostic)
	}
	s, _ = hub.Inspect(context.Background(), ids["legacy"])
	if s.Initialized || s.Inspection.Diagnostic == nil || s.Inspection.Diagnostic.Code != "uninitialized" || len(s.Inspection.Workspaces) != 0 {
		t.Fatalf("%+v", s.Inspection)
	}
	c, _, err = hub.Open(ids["legacy"], "default", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, c); snap.Diagnostic == nil || snap.Diagnostic.Code != "uninitialized" || len(snap.Index.Resources) != 0 {
		t.Fatalf("%+v", snap)
	}
}

func TestUnknownWorkspaceIsNotRead(t *testing.T) {
	hub, ids, spy := hubFixture(t)
	c, _, err := hub.Open(ids["network"], "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, c); snap.Diagnostic == nil || snap.Diagnostic.Code != "workspace_missing" {
		t.Fatalf("%+v", snap.Diagnostic)
	}
	for _, line := range spyLines(t, spy) {
		if strings.HasSuffix(line, "show\x00-json") {
			t.Fatal("state read for a missing workspace")
		}
	}
	for _, bad := range []string{"", "../prod", "prod;rm", "-chdir=/", strings.Repeat("a", 91)} {
		if _, _, err := hub.Open(ids["network"], bad, false); !errors.Is(err, ErrInvalidWorkspace) {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
	if _, _, err := hub.Open("00000000000000000000000000000000", "dev", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestAbsoluteDataDirIsRefused(t *testing.T) {
	hub, ids, _ := hubFixture(t)
	t.Setenv("TF_DATA_DIR", t.TempDir())
	if _, _, err := hub.Open(ids["network"], "dev", false); !errors.Is(err, ErrSharedDataDir) {
		t.Fatalf("shared data dir accepted: %v", err)
	}
	s, _ := hub.Inspect(context.Background(), ids["network"])
	if s.Inspection.Diagnostic == nil || s.Inspection.Diagnostic.Code != "shared_data_dir" {
		t.Fatalf("%+v", s.Inspection)
	}
}

// blocking reads until cancelled, to exercise unregister during an active read.
type blocking struct {
	workspace string
	started   chan struct{}
	once      sync.Once
}

func (b *blocking) Version(context.Context) (string, error) { return "1.9.8", nil }
func (b *blocking) Workspace(context.Context) (string, error) {
	return b.workspace, nil
}
func (b *blocking) State(ctx context.Context) ([]byte, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return fixtures.State, nil
}
func (b *blocking) ListWorkspaces(context.Context) (engine.WorkspaceList, error) {
	return engine.WorkspaceList{Names: []string{"default", b.workspace}}, nil
}

func initialized(t *testing.T, name string) string {
	root := project(t, name)
	if err := os.Mkdir(filepath.Join(root, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestUnregisterDuringReadDiscardsSnapshot(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	hub := NewHub(Open(t.TempDir(), now), "")
	t.Cleanup(hub.Close)
	fake := &blocking{workspace: "dev", started: make(chan struct{})}
	hub.Engines = func(string, string, string) (string, string, Adapter, error) { return "terraform", "/fake", fake, nil }
	s, err := hub.Add(initialized(t, "racy"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := hub.Open(s.ID, "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	<-fake.started
	if err := hub.Remove(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := hub.Context(c.ID); !errors.Is(err, ErrContextNotFound) || reason != "project_removed" {
		t.Fatalf("context still served: %s", reason)
	}
	if snap := waitLoaded(t, c); snap.Status == "ready" {
		t.Fatal("cancelled read published a snapshot")
	}
}

// ignoring reports the CLI default no matter what TF_WORKSPACE says.
type ignoring struct{ blocking }

func (i *ignoring) State(context.Context) ([]byte, error) { return fixtures.State, nil }

func TestEngineIgnoringSelectedWorkspaceIsRefused(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	hub := NewHub(Open(t.TempDir(), now), "")
	t.Cleanup(hub.Close)
	fake := &ignoring{blocking{workspace: "default"}}
	s, _ := hub.Add(initialized(t, "stubborn"), "", "")
	// "prod" is listed, but the engine keeps reporting "default".
	hub.Engines = func(string, string, string) (string, string, Adapter, error) {
		return "terraform", "/fake", &listing{fake, []string{"default", "prod"}}, nil
	}
	c, _, err := hub.Open(s.ID, "prod", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, c); snap.Diagnostic == nil || snap.Diagnostic.Code != "workspace_mismatch" || len(snap.Index.Resources) != 0 {
		t.Fatalf("%+v", snap.Diagnostic)
	}
}

type listing struct {
	*ignoring
	names []string
}

func (l *listing) ListWorkspaces(context.Context) (engine.WorkspaceList, error) {
	return engine.WorkspaceList{Names: l.names}, nil
}

func TestContextsAreBoundedAndProjectChangesCloseThem(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	hub := NewHub(Open(t.TempDir(), now), "")
	hub.MaxContexts = 2
	t.Cleanup(hub.Close)
	hub.Engines = func(_, _, selected string) (string, string, Adapter, error) {
		return "terraform", "/fake", &listing{&ignoring{blocking{workspace: selected}}, []string{"a", "b", "c"}}, nil
	}
	s, _ := hub.Add(initialized(t, "bounded"), "", "")
	a, _, _ := hub.Open(s.ID, "a", false)
	b, _, _ := hub.Open(s.ID, "b", false)
	c, _, _ := hub.Open(s.ID, "c", false)
	if _, reason, _ := hub.Context(a.ID); reason != "evicted" {
		t.Fatalf("oldest not evicted: %q", reason)
	}
	waitLoaded(t, b)
	if snap := waitLoaded(t, c); snap.Status != "ready" {
		t.Fatalf("%+v", snap.Diagnostic)
	}
	tofu := "tofu"
	if _, err := hub.Update(s.ID, Change{EnginePreference: &tofu}); err != nil {
		t.Fatal(err)
	}
	if _, reason, _ := hub.Context(c.ID); reason != "project_changed" {
		t.Fatalf("context survived an engine change: %q", reason)
	}
}

func TestBrowseListsOnlyDirectoryNames(t *testing.T) {
	dir := project(t, "browse")
	for _, d := range []string{"envs", ".secret", "node_modules"} {
		_ = os.Mkdir(filepath.Join(dir, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(dir, "credentials.txt"), []byte("x"), 0o600)
	l, err := Browse(dir)
	if err != nil || !l.Project || !slices.Equal(l.Entries, []string{"envs"}) || l.Parent == "" {
		t.Fatalf("%v %+v", err, l)
	}
	for _, bad := range []string{"relative", filepath.Join(dir, "missing")} {
		if _, err := Browse(bad); !errors.Is(err, ErrBrowse) {
			t.Fatalf("%q listed", bad)
		}
	}
}

// unsupported fails the version read, as the CLI adapter does for excluded versions.
type unsupported struct{ ignoring }

func (u *unsupported) Version(context.Context) (string, error) {
	return "", errors.New("unsupported engine version")
}

func TestMissingOrUnsupportedEngineIsReportedHonestly(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	hub := NewHub(Open(t.TempDir(), now), "")
	t.Cleanup(hub.Close)
	s, _ := hub.Add(initialized(t, "engines"), "", "tofu")
	hub.Engines = func(string, string, string) (string, string, Adapter, error) {
		return "", "", nil, errors.New("not on PATH")
	}
	got, _ := hub.Inspect(context.Background(), s.ID)
	if got.Inspection.Diagnostic == nil || got.Inspection.Diagnostic.Code != "missing_cli" || got.Inspection.Engine != "tofu" {
		t.Fatalf("%+v", got.Inspection)
	}
	c, _, err := hub.Open(s.ID, "default", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, c); snap.Diagnostic == nil || snap.Diagnostic.Code != "missing_cli" {
		t.Fatalf("%+v", snap.Diagnostic)
	}
	hub.Engines = func(string, string, string) (string, string, Adapter, error) {
		return "tofu", "/fake", &unsupported{ignoring{blocking{workspace: "default"}}}, nil
	}
	got, _ = hub.Inspect(context.Background(), s.ID)
	if got.Inspection.Diagnostic == nil || got.Inspection.Diagnostic.Code != "engine_version" || got.Inspection.Version != "" {
		t.Fatalf("%+v", got.Inspection)
	}
	c, _, _ = hub.Open(s.ID, "default", true)
	if snap := waitLoaded(t, c); snap.Diagnostic == nil || snap.Diagnostic.Code != "engine_version" || len(snap.Index.Resources) != 0 {
		t.Fatalf("%+v", snap.Diagnostic)
	}
}
