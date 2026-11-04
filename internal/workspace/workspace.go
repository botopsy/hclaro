package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

type Capabilities struct {
	CanReadState bool `json:"canReadState"`
	CanPlan      bool `json:"canPlan"`
	CanApply     bool `json:"canApplySavedPlan"`
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
}

func New(info Info) *Store {
	return &Store{snapshot: Snapshot{Status: "loading", Info: info, Index: state.Empty()}}
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
	fail := func(code, message string) {
		snap.Status = "error"
		snap.Index = state.Empty()
		snap.Info.Capabilities = Capabilities{}
		snap.Diagnostic = &Diagnostic{code, message}
		s.set(snap)
	}
	if adapter == nil {
		fail("missing_cli", "Terraform/OpenTofu was not found. Install a supported CLI on PATH and restart, or choose --engine terraform / --engine tofu.")
		return
	}
	version, err := adapter.Version(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			fail("unsafe_cli_args", "Inherited TF_CLI_ARGS settings contain unsupported flags. M1 accepts only -no-color. Remove the injected flags for version/workspace/show and restart. No values or flags were logged.")
			return
		}
		fail("engine_version", "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13 and check the executable on PATH.")
		return
	}
	snap.Info.Version = version
	s.set(snap)
	if !snap.Info.Initialized {
		fail("uninitialized", "Backend initialization was not detected. Run the selected engine’s init command yourself in this project, then restart Hclaro. Nothing was initialized automatically.")
		return
	}
	name, err := adapter.Workspace(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			fail("unsafe_cli_args", "Remove unsupported TF_CLI_ARGS_workspace settings and restart. M1 accepts only -no-color.")
			return
		}
		fail("workspace_error", "Unable to determine the active workspace. Check backend initialization and workspace selection with the CLI, then restart Hclaro.")
		return
	}
	snap.Info.Workspace = name
	s.set(snap)
	input, err := adapter.State(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			fail("unsafe_cli_args", "Remove unsupported TF_CLI_ARGS_show settings and restart. M1 accepts only -no-color; output-file overrides could persist raw secrets.")
			return
		}
		fail("state_unavailable", "State is unavailable. Check backend access, credentials, SSO/VPN, and installed provider schemas with the CLI, then restart. No refresh was run.")
		return
	}
	idx, err := state.Parse(input)
	if errors.Is(err, state.ErrNoState) {
		fail("state_absent", "No state snapshot has been recorded. This is different from an empty snapshot. Hclaro does not create or refresh state; use your normal CLI workflow, then restart.")
		return
	}
	if err != nil {
		fail("state_format", "State output could not be safely decoded. The format may be unsupported, oversized, or contain invalid records. No state data was returned.")
		return
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
