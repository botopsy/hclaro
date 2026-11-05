package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

type Capabilities struct {
	CanReadState bool   `json:"canReadState"`
	CanPlan      bool   `json:"canPlan"`
	PlanReason   string `json:"planReason,omitempty"`
	CanApply     bool   `json:"canApplySavedPlan"`
	ApplyReason  string `json:"applyReason,omitempty"`
	// Import blocks and configuration generation depend on the engine and version.
	SupportsImportBlocks    bool   `json:"supportsImportBlocks"`
	SupportsGeneratedConfig bool   `json:"supportsGeneratedConfig"`
	CanImport               bool   `json:"canImport"`
	ImportReason            string `json:"importReason,omitempty"`
}
type Info struct {
	Project       string       `json:"project"`
	Engine        string       `json:"engine"`
	Version       string       `json:"version"`
	Workspace     string       `json:"workspace"`
	Backend       string       `json:"backend"`
	ExecutionMode string       `json:"executionMode"`
	Initialized   bool         `json:"initialized"`
	ProviderLock  bool         `json:"providerLock"`
	Capabilities  Capabilities `json:"capabilities"`
	Warnings      []string     `json:"warnings"`
}
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Snapshot struct {
	Status     string      `json:"status"`
	Info       Info        `json:"info"`
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
	CapturedAt string      `json:"capturedAt,omitempty"`
	Index      state.Index `json:"-"`
}
type Store struct {
	mu       sync.RWMutex
	snapshot Snapshot
	// scope is set for Hub contexts; see NewScoped.
	scope *Scope
}

// Scope pins a Hub context: the engine must report Workspace (proving the per-child
// TF_WORKSPACE was honored) before anything is published, and plan, apply and import
// capabilities stay off with ReadOnlyReason.
type Scope struct {
	Workspace      string
	ReadOnlyReason string
	// Check runs after the version and initialization checks and before the workspace is
	// read, for example to confirm the workspace exists. Verify runs just before a snapshot
	// is published, for example to confirm the project directory did not change.
	Check  func(context.Context) *Diagnostic
	Verify func() *Diagnostic
}

func New(info Info) *Store {
	return &Store{snapshot: Snapshot{Status: "loading", Info: info, Index: state.Empty()}}
}

// NoProject is the launch store when Hclaro starts with only the Workspace Hub.
func NoProject() *Store {
	return &Store{snapshot: Snapshot{Status: "error", Info: Info{Backend: "unknown", ExecutionMode: "unsupported", Warnings: []string{}}, Index: state.Empty(), Diagnostic: &Diagnostic{"no_launch_project", "Hclaro was started without a project. Open one from the Workspace Hub."}}}
}

// NewScoped creates a read-only store for one Hub project and workspace.
func NewScoped(info Info, scope Scope) *Store {
	s := New(info)
	s.scope = &scope
	return s
}
func (s *Store) Get() Snapshot         { s.mu.RLock(); defer s.mu.RUnlock(); return s.snapshot }
func (s *Store) set(snapshot Snapshot) { s.mu.Lock(); defer s.mu.Unlock(); s.snapshot = snapshot }

// Root validates just the selected directory. It never walks parents or initializes anything.
func Root(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid project path")
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("project path does not exist or is inaccessible")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", errors.New("project path must be a readable directory")
	}
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".tf") || strings.HasSuffix(e.Name(), ".tf.json") || strings.HasSuffix(e.Name(), ".tofu") || strings.HasSuffix(e.Name(), ".tofu.json") || e.Name() == "terraform.tfstate") {
			return abs, nil
		}
	}
	return "", errors.New("no Terraform/OpenTofu project found; select a root containing .tf, .tofu or state files using --path")
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
func Inspect(root string) Info {
	name := filepath.Base(root)
	if state.Suspicious(name) {
		name = "Local project"
	}
	info := Info{Project: name, Backend: "unknown", ExecutionMode: "unsupported", ProviderLock: fileExists(filepath.Join(root, ".terraform.lock.hcl")), Warnings: []string{"Snapshot of recorded state, not live cloud inventory. State reads may contact the configured backend and load installed provider plugins."}}
	data := os.Getenv("TF_DATA_DIR")
	if data == "" {
		data = ".terraform"
	}
	if !filepath.IsAbs(data) {
		data = filepath.Join(root, data)
	}
	fi, err := os.Stat(data)
	info.Initialized = err == nil && fi.IsDir()
	if fileExists(filepath.Join(root, "terraform.tfstate")) {
		info.Initialized = true
	}
	// Only consume the initialized backend type; never return backend config or credentials.
	metadata := filepath.Join(data, "terraform.tfstate")
	if fileExists(metadata) {
		if f, err := os.Open(metadata); err == nil {
			defer f.Close()
			if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
				var meta struct {
					Backend struct {
						Type string `json:"type"`
					} `json:"backend"`
				}
				if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&meta) == nil {
					for _, known := range []string{"local", "s3", "azurerm", "gcs", "http", "remote", "cloud", "consul", "kubernetes", "oss", "cos", "pg", "oci", "swift"} {
						if meta.Backend.Type == known {
							info.Backend = known
							info.ExecutionMode = "local-cli"
						}
					}
					if info.Backend == "remote" || info.Backend == "cloud" {
						info.ExecutionMode = "remote-exec"
						info.Warnings = append(info.Warnings, "Remote execution backend: only read-only state inspection is available.")
					}
				}
			}
		}
	}
	// Without a backend or cloud block, init records no backend metadata and the engine uses
	// the default local backend. A block that was never initialized makes the engine refuse
	// to plan, so this cannot route a plan to a remote backend.
	if info.Backend == "unknown" && info.Initialized && !fileExists(metadata) && !declaresBackend(root) {
		info.Backend = "local"
		info.ExecutionMode = "local-cli"
	}
	if info.Backend == "unknown" {
		info.Warnings = append(info.Warnings, "Backend type is unavailable from initialized metadata; execution mode is unverified.")
	}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "TF_CLI_ARGS") {
			info.Warnings = append(info.Warnings, "Inherited TF_CLI_ARGS settings affect engine reads. Values are hidden.")
			break
		}
	}
	return info
}

func (s *Store) Load(ctx context.Context, adapter engine.Adapter, engineName string) {
	snap := s.Get()
	snap.Info.Engine = engineName
	snap.Status = "loading"
	snap.Diagnostic = nil
	snap.CapturedAt = ""
	snap.Index = state.Empty()
	snap.Info.Capabilities = Capabilities{}
	s.set(snap)
	// The launch project is re-read by restarting; Hub contexts can be read again in the UI.
	again := "restart Hclaro"
	if s.scope != nil {
		again = "read this workspace again"
	}
	fail := func(code, message string) {
		snap.Status = "error"
		snap.Index = state.Empty()
		// A project without recorded state can still be planned; other failures cannot.
		planning := snap.Info.Capabilities
		snap.Info.Capabilities = Capabilities{PlanReason: "Planning needs a readable workspace. Resolve the issue above and restart Hclaro.", ApplyReason: "Applying needs a readable workspace.", ImportReason: "Importing needs a readable workspace. Resolve the issue above and restart Hclaro."}
		if s.scope != nil {
			snap.Info.Capabilities = s.readOnly()
		}
		if code == "state_absent" {
			snap.Info.Capabilities = planning
		}
		snap.Diagnostic = &Diagnostic{code, message}
		s.set(snap)
	}
	if adapter == nil {
		fail("missing_cli", "Terraform/OpenTofu was not found. Install a supported CLI on PATH and "+again+", or choose --engine terraform / --engine tofu.")
		return
	}
	version, err := adapter.Version(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			fail("unsafe_cli_args", "Inherited TF_CLI_ARGS settings contain unsupported flags. Only -no-color is accepted. Remove the injected flags for version/workspace/show and restart Hclaro. No values or flags were logged.")
			return
		}
		fail("engine_version", "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13 and check the executable on PATH.")
		return
	}
	snap.Info.Version = version
	s.set(snap)
	if !snap.Info.Initialized {
		fail("uninitialized", "Backend initialization was not detected. Run the selected engine’s init command yourself in this project, then "+again+". Nothing was initialized automatically.")
		return
	}
	if s.scope != nil && s.scope.Check != nil {
		if d := s.scope.Check(ctx); d != nil {
			fail(d.Code, d.Message)
			return
		}
	}
	name, err := adapter.Workspace(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			fail("unsafe_cli_args", "Remove unsupported TF_CLI_ARGS_workspace settings and restart. Only -no-color is accepted.")
			return
		}
		fail("workspace_error", "Unable to determine the active workspace. Check backend initialization and workspace selection with the CLI, then "+again+".")
		return
	}
	if s.scope != nil && name != s.scope.Workspace {
		fail("workspace_mismatch", "The engine reported a different workspace than the one selected, so nothing was read. TF_WORKSPACE may be ignored by this engine or backend; check the workspace with the CLI.")
		return
	}
	snap.Info.Workspace = name
	switch _, planner := adapter.(engine.Planner); {
	case !planner:
		snap.Info.Capabilities.PlanReason = "This engine adapter is read-only."
	case snap.Info.ExecutionMode == "remote-exec":
		snap.Info.Capabilities.PlanReason = "Remote execution backends run plans remotely. Hclaro supports only local plans."
	case snap.Info.ExecutionMode != "local-cli":
		snap.Info.Capabilities.PlanReason = "The backend type could not be verified from initialization metadata, so local planning is not enabled."
	default:
		snap.Info.Capabilities.CanPlan = true
	}
	// Saved-plan apply is offered only where Hclaro itself produced the plan file locally.
	// Remote execution has no verified contract that the reviewed plan is what gets applied.
	_, applier := adapter.(engine.Applier)
	switch {
	case snap.Info.ExecutionMode == "remote-exec":
		snap.Info.Capabilities.ApplyReason = "Remote execution backends are read-only in Hclaro: there is no verified contract that a plan reviewed here is exactly what the remote run applies."
	case !snap.Info.Capabilities.CanPlan:
		snap.Info.Capabilities.ApplyReason = "Applying requires local planning, which is unavailable for this workspace."
	case !applier:
		snap.Info.Capabilities.ApplyReason = "This engine adapter cannot apply saved plans."
	default:
		snap.Info.Capabilities.CanApply = true
	}
	caps := &snap.Info.Capabilities
	caps.SupportsImportBlocks, caps.SupportsGeneratedConfig = engine.ImportSupport(engineName, version)
	switch {
	case !caps.SupportsImportBlocks:
		caps.ImportReason = "This engine version does not support import blocks. Use the manual recipe."
	case !caps.CanPlan:
		caps.ImportReason = "Importing through Hclaro needs a local plan, which is unavailable for this workspace: " + caps.PlanReason + " Use the manual recipe with your own reviewed workflow."
	default:
		caps.CanImport = true
	}
	if s.scope != nil {
		keep := caps.SupportsImportBlocks
		*caps = s.readOnly()
		caps.SupportsImportBlocks, caps.SupportsGeneratedConfig = keep, keep
	}
	s.set(snap)
	input, err := adapter.State(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			fail("unsafe_cli_args", "Remove unsupported TF_CLI_ARGS_show settings and restart. Only -no-color is accepted; output-file overrides could persist raw secrets.")
			return
		}
		fail("state_unavailable", "State is unavailable. Check backend access, credentials, SSO/VPN, and installed provider schemas with the CLI, then "+again+". No refresh was run.")
		return
	}
	idx, err := state.Parse(input)
	if errors.Is(err, state.ErrNoState) {
		fail("state_absent", "No state snapshot has been recorded. This is different from an empty snapshot. Hclaro does not create or refresh state; use your normal CLI workflow, then "+again+".")
		return
	}
	if err != nil {
		fail("state_format", "State output could not be safely decoded. The format may be unsupported, oversized, or contain invalid records. No state data was returned.")
		return
	}
	if s.scope != nil && s.scope.Verify != nil {
		if d := s.scope.Verify(); d != nil {
			fail(d.Code, d.Message)
			return
		}
	}
	snap.Info.Capabilities.CanReadState = true
	snap.Status = "ready"
	if len(idx.Resources) == 0 {
		snap.Status = "empty"
	}
	snap.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	snap.Index = idx
	s.set(snap)
}

func (s *Store) readOnly() Capabilities {
	r := s.scope.ReadOnlyReason
	return Capabilities{PlanReason: r, ApplyReason: r, ImportReason: r}
}

var (
	backendBlock = regexp.MustCompile(`(^|[\s{])(backend\s+"[^"]*"|cloud)\s*\{`)
	backendJSON  = regexp.MustCompile(`"(backend|cloud)"\s*:`)
)

// declaresBackend reports whether a root module file may declare a backend or cloud block.
// The scan is textual and errs towards true, for example for a commented-out block or a
// file that cannot be read.
func declaresBackend(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return true
	}
	for _, e := range entries {
		name := e.Name()
		hcl := strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tofu")
		js := strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tofu.json")
		if !hcl && !js {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || len(b) > 16<<20 {
			return true
		}
		if hcl && backendBlock.Match(b) || js && backendJSON.Match(b) {
			return true
		}
	}
	return false
}
