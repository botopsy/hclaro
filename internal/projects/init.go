package projects

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

var (
	ErrBusy           = errors.New("another operation is running in this project")
	ErrContextChanged = errors.New("project changed since it was reviewed")
	ErrInitRefused    = errors.New("initialization is unavailable")
)

// InitRefusal explains why initialization cannot start. Nothing was run.
type InitRefusal struct{ Code, Message string }

func (e *InitRefusal) Error() string { return e.Message }

// IniterFactory resolves the engine for an initialization with a pinned environment. It
// returns the executable path so its identity can be re-checked before the engine starts.
type IniterFactory func(root, preference string, environ []string) (name, executable string, initer engine.Initer, err error)

// DefaultIniters uses the official CLI found on PATH. TF_WORKSPACE is never passed: init
// works on the CLI-default workspace and must not depend on how Hclaro was started.
func DefaultIniters(root, preference string, environ []string) (string, string, engine.Initer, error) {
	cli, err := engine.Detect(root, preference)
	if err != nil {
		return "", "", nil, err
	}
	cli.IgnoreInheritedWorkspace, cli.Environ = true, environ
	return cli.Name, cli.Executable, cli, nil
}

// InitStatus is the read-only preflight for one project. It runs no engine command; the
// engine version is shown only when an earlier inspection in this session read it.
type InitStatus struct {
	ProjectID     string `json:"projectId"`
	Alias         string `json:"alias"`
	RootPath      string `json:"rootPath"`
	Status        string `json:"status"`
	Problem       string `json:"problem,omitempty"`
	Initialized   bool   `json:"initialized"`
	Backend       string `json:"backend"`
	ExecutionMode string `json:"executionMode"`
	// BackendDeclared reports a backend or cloud block in the root module (textual check).
	BackendDeclared bool     `json:"backendDeclared"`
	Engine          string   `json:"engine,omitempty"`
	EngineVersion   string   `json:"engineVersion,omitempty"`
	DataDir         string   `json:"dataDir"`
	Lockfile        bool     `json:"lockfile"`
	CLIDefault      string   `json:"cliDefault,omitempty"`
	Command         []string `json:"command"`
	Notices         []string `json:"notices"`
	CanInit         bool     `json:"canInit"`
	Reason          string   `json:"reason,omitempty"`
	ReasonCode      string   `json:"reasonCode,omitempty"`
	// ContextID identifies everything reviewed here; starting init requires it unchanged.
	ContextID string                `json:"contextId,omitempty"`
	Busy      string                `json:"busy,omitempty"`
	Operation *operations.Operation `json:"operation,omitempty"`
}

// initEnvKey reports environment variables that can change what init does. Their values are
// hashed into the context ID and never returned.
func initEnvKey(key string) bool {
	return strings.HasPrefix(key, "TF_") && !strings.HasPrefix(key, "TF_LOG") && !strings.HasPrefix(key, "TF_VAR_")
}

// unsafeInitArgs reports inherited TF_CLI_ARGS or TF_CLI_ARGS_init flags other than -no-color.
func unsafeInitArgs(environ []string) bool {
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if key == "TF_CLI_ARGS" || key == "TF_CLI_ARGS_init" {
			for _, arg := range strings.Fields(value) {
				if arg != "-no-color" {
					return true
				}
			}
		}
	}
	return false
}

// initNotices discloses inherited settings without their values.
func initNotices(environ []string) []string {
	notices := []string{}
	set := map[string]bool{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		set[key] = true
	}
	if set["TF_WORKSPACE"] {
		notices = append(notices, "Inherited TF_WORKSPACE is not passed to init; init uses this project's CLI-default workspace.")
	}
	if set["TF_CLI_CONFIG_FILE"] || set["TERRAFORM_CONFIG"] || set["TOFU_CLI_CONFIG_FILE"] {
		notices = append(notices, "A CLI configuration file is set in the environment; its registry, mirror and credential settings apply to init.")
	}
	if set["TF_PLUGIN_CACHE_DIR"] {
		notices = append(notices, "TF_PLUGIN_CACHE_DIR is set; providers may be read from or written to that shared cache.")
	}
	return notices
}

// fileIdentity summarizes a file for change detection: size, modification time and mode.
func fileIdentity(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%s\x00%s\x00%d", fi.ModTime().UTC().Format(time.RFC3339Nano), fi.Mode(), fi.Size())
}

// contextID hashes the reviewed init context: project, root, engine choice and executable,
// data directory, init-relevant environment and the configuration fingerprint (which covers
// the lock file and initialized metadata). Empty means it cannot be computed safely.
func contextID(p Record, name, executable string, environ []string) string {
	fingerprint := workspace.Fingerprint(p.RootPath, environ, "init")
	if fingerprint == "" {
		return ""
	}
	env := []string{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if initEnvKey(key) {
			env = append(env, item)
		}
	}
	sort.Strings(env)
	h := sha256.New()
	for _, field := range append([]string{p.ID, p.RootPath, p.EnginePreference, name, executable, fileIdentity(executable), fingerprint}, env...) {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// InitStatus returns the preflight for a project. No engine command runs.
func (h *Hub) InitStatus(id string) (InitStatus, error) {
	p, err := h.Registry.Get(id)
	if err != nil {
		return InitStatus{}, err
	}
	environ := os.Environ()
	data, shared := dataDir()
	s := InitStatus{ProjectID: p.ID, Alias: p.Alias, RootPath: p.RootPath, Backend: "unknown", ExecutionMode: "unsupported", DataDir: data, Notices: initNotices(environ)}
	s.Status, s.Problem = check(p.RootPath)
	// Resolving the engine looks up the executable; it does not run it.
	name, executable := p.EnginePreference, ""
	if h.Initers != nil {
		if n, exe, _, err := h.Initers(p.RootPath, p.EnginePreference, environ); err == nil {
			name, executable = n, exe
		}
	}
	s.Engine = name
	s.Command = operations.InitCommand(name, false)
	if s.Engine == "" {
		s.Command = operations.InitCommand("terraform", false)
	}
	h.mu.Lock()
	if in, ok := h.inspections[p.ID]; ok && in.Engine == name {
		s.EngineVersion, s.CLIDefault = in.Version, in.CLIDefault
	}
	h.mu.Unlock()
	if h.Inits != nil {
		if op, ok := h.Inits.Latest(p.ID); ok {
			s.Operation = &op
		}
	}
	s.Busy, _ = h.Locks.Holder(p.RootPath)
	refuse := func(code, message string) (InitStatus, error) {
		s.ReasonCode, s.Reason = code, message
		return s, nil
	}
	if s.Status != StatusReady {
		return refuse(s.Status, s.Problem)
	}
	info := workspace.Inspect(p.RootPath)
	s.Initialized, s.Backend, s.ExecutionMode, s.Lockfile = info.Initialized, info.Backend, info.ExecutionMode, info.ProviderLock
	s.BackendDeclared = workspace.DeclaresBackend(p.RootPath)
	switch {
	case h.Inits == nil || h.Initers == nil:
		return refuse("unavailable", "Initialization is not available in this session.")
	case shared:
		return refuse("shared_data_dir", "TF_DATA_DIR is an absolute path, so every project would share one data directory and backend initialization. Restart Hclaro without it, or with a relative TF_DATA_DIR.")
	case executable == "":
		return refuse("missing_cli", "Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine.")
	case unsafeInitArgs(environ):
		return refuse("unsafe_cli_args", "Inherited TF_CLI_ARGS or TF_CLI_ARGS_init adds flags other than -no-color, such as -upgrade or -reconfigure. Hclaro does not run init with hidden flags. Restart Hclaro without them.")
	case s.Busy != "":
		return refuse("busy", "Another operation is running in this project. Wait for it to finish.")
	}
	s.ContextID = contextID(p, name, executable, environ)
	if s.ContextID == "" {
		return refuse("too_large", "The project has too many files to fingerprint safely, so Hclaro cannot detect changes made after review. Run init in a terminal.")
	}
	s.CanInit = true
	return s, nil
}

// StartInit starts a standard init after the user reviewed the preflight identified by
// expected. The root is claimed exclusively until the operation ends; the engine, its
// executable, the root, the data directory and the environment are pinned now and checked
// again just before the engine starts.
func (h *Hub) StartInit(id, expected string) (operations.Operation, error) {
	status, err := h.InitStatus(id)
	if err != nil {
		return operations.Operation{}, err
	}
	if status.ReasonCode == "busy" {
		return operations.Operation{}, ErrBusy
	}
	if !status.CanInit {
		return operations.Operation{}, &InitRefusal{status.ReasonCode, status.Reason}
	}
	if expected == "" || expected != status.ContextID {
		return operations.Operation{}, ErrContextChanged
	}
	p, err := h.Registry.Get(id)
	if err != nil {
		return operations.Operation{}, err
	}
	release, ok := h.Locks.Acquire(p.RootPath, "init")
	if !ok {
		return operations.Operation{}, ErrBusy
	}
	environ := os.Environ()
	name, executable, initer, err := h.Initers(p.RootPath, p.EnginePreference, environ)
	if err != nil {
		release()
		return operations.Operation{}, &InitRefusal{"missing_cli", "Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine."}
	}
	rootInfo, statErr := os.Stat(p.RootPath)
	exeInfo, exeErr := os.Stat(executable)
	if statErr != nil || exeErr != nil || contextID(p, name, executable, environ) != expected {
		release()
		return operations.Operation{}, ErrContextChanged
	}
	data, _ := dataDir()
	if !filepath.IsAbs(data) {
		data = filepath.Join(p.RootPath, data)
	}
	return h.Inits.Start(operations.InitRequest{
		ProjectID: p.ID, Root: p.RootPath, Engine: name, Initer: initer, Environ: environ, Notices: status.Notices,
		Revalidate: func() string {
			now, err := os.Stat(p.RootPath)
			resolved, evalErr := filepath.EvalSymlinks(p.RootPath)
			exe, exeErr := os.Stat(executable)
			if err != nil || evalErr != nil || exeErr != nil || resolved != p.RootPath || !os.SameFile(rootInfo, now) || !os.SameFile(exeInfo, exe) || contextID(p, name, executable, environ) != expected {
				return "path_changed"
			}
			return ""
		},
		Initialized: func() bool {
			fi, err := os.Stat(data)
			return err == nil && fi.IsDir()
		},
		Lockfile: func() []byte {
			f, err := os.Open(filepath.Join(p.RootPath, ".terraform.lock.hcl"))
			if err != nil {
				return nil
			}
			defer f.Close()
			b, _ := io.ReadAll(io.LimitReader(f, 4<<20))
			return b
		},
		// Inspections and open snapshots describe the project before init; drop them.
		Finished: func() { h.forget(p.ID, "initialized") },
		Release:  release,
	})
}
