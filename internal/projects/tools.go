package projects

import (
	"context"
	"errors"
	"hclaro/internal/scanners"
	"hclaro/internal/workspace"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/tfpolicy"
)

// ToolsFactory resolves the engine for a project utility with a pinned environment. selected
// is the context's workspace; the adapter passes it as TF_WORKSPACE only to the tools that
// read state. It returns the executable path so its identity can be checked.
type ToolsFactory func(root, preference string, environ []string, selected string) (name, executable string, tools engine.Tools, err error)

// DefaultTools uses the official CLI found on PATH. Inherited TF_WORKSPACE is never passed.
func DefaultTools(root, preference string, environ []string, selected string) (string, string, engine.Tools, error) {
	cli, err := detectCLI(root, preference, selected, environ)
	if err != nil {
		return "", "", nil, err
	}
	return cli.Name, cli.Executable, cli, nil
}

// ToolRefusal explains why a project utility cannot start. Nothing was run.
type ToolRefusal struct{ Code, Message string }

func (e *ToolRefusal) Error() string { return e.Message }

// ErrInvalidTool rejects an unknown utility name.
var ErrInvalidTool = errors.New("invalid project tool")

// toolNotices disclose inherited settings without their values.
func toolNotices(environ []string, tool engine.Tool) []string {
	notices := []string{}
	for _, item := range environ {
		if key, _, _ := strings.Cut(item, "="); key == "TF_WORKSPACE" {
			if engine.WorkspaceDependent(tool) {
				notices = append(notices, "Inherited TF_WORKSPACE is replaced by this tab's workspace for this command.")
			} else {
				notices = append(notices, "Inherited TF_WORKSPACE is not passed; this command reads configuration only.")
			}
			break
		}
	}
	return notices
}

const (
	maxFormatFiles   = 5000
	maxFormatFolders = 1000
	maxFormatDepth   = 32
)

// ignoredName mirrors the engines' rule for configuration directory entries: hidden names,
// editor backups (~) and lock files (#…#) are skipped.
func ignoredName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") || strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#")
}

// formatFile reports the configuration files fmt reads. OpenTofu also formats its own
// .tofu and .tofutest.hcl files.
func formatFile(name string, tofu bool) bool {
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".tftest.hcl") ||
		tofu && (strings.HasSuffix(name, ".tofu") || strings.HasSuffix(name, ".tofutest.hcl"))
}

// FormatScope enumerates the configuration files fmt -check reads: the root's .tf, .tfvars
// and .tftest.hcl files (plus .tofu and .tofutest.hcl for OpenTofu), and with recursive those of every non-hidden subdirectory.
// Symbolic links to directories are never traversed: in a recursive check one refuses the
// check, otherwise it is listed in skipped. A linked file that resolves outside the root
// refuses the check.
func FormatScope(root, engineName string, recursive bool) (files map[string]bool, folders int, skipped []string, refusal *ToolRefusal) {
	files, skipped = map[string]bool{}, []string{}
	tofu := engineName == "tofu"
	var walk func(dir, rel string, depth int) *ToolRefusal
	walk = func(dir, rel string, depth int) *ToolRefusal {
		folders++
		if folders > maxFormatFolders || depth > maxFormatDepth {
			return &ToolRefusal{"too_many_files", "The project has too many folders for a recursive check. Run fmt -check -recursive in a terminal instead."}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return &ToolRefusal{"unreadable", "A configuration folder could not be read, so Hclaro cannot list what fmt would check."}
		}
		for _, e := range entries {
			name := e.Name()
			if ignoredName(name) {
				continue
			}
			full, relName := filepath.Join(dir, name), path.Join(rel, name)
			if e.Type()&fs.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(full)
				info, statErr := os.Stat(full)
				switch {
				case err != nil || statErr != nil:
					skipped = append(skipped, relName)
				case info.IsDir() && recursive:
					return &ToolRefusal{"symlinked_directory", "The folder " + relName + " is a symbolic link. Recursive checks never follow linked folders; remove the link or run a non-recursive check."}
				case info.IsDir():
					skipped = append(skipped, relName)
				case formatFile(name, tofu) && !inside(root, target):
					return &ToolRefusal{"symlink_outside_root", "The configuration file " + relName + " links to a file outside the project folder. Hclaro does not check files outside the project."}
				case formatFile(name, tofu):
					files[relName] = true
				}
				continue
			}
			if e.IsDir() {
				if recursive {
					if r := walk(full, relName, depth+1); r != nil {
						return r
					}
				}
				continue
			}
			if e.Type().IsRegular() && formatFile(name, tofu) {
				files[relName] = true
				if len(files) > maxFormatFiles {
					return &ToolRefusal{"too_many_files", "The project has too many configuration files for this check. Run fmt -check in a terminal instead."}
				}
			}
		}
		return nil
	}
	if r := walk(root, "", 0); r != nil {
		return nil, 0, nil, r
	}
	if len(skipped) > 100 {
		skipped = skipped[:100]
	}
	return files, folders, skipped, nil
}

func inside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && filepath.IsLocal(rel)
}

// toolPrep is everything a tool run needs, resolved and pinned: the context's project, root,
// engine executable, environment and a shared claim on the root (release must be called).
type toolPrep struct {
	p                 Record
	c                 *Context
	environ, notices  []string
	files             map[string]bool
	folders           int
	skipped           []string
	recursive         bool
	name, executable  string
	tools             engine.Tools
	rootInfo, exeInfo os.FileInfo
	release           func()
}

// revalidate reports "path_changed" when the root or the engine executable is not the one
// pinned when the run was prepared.
func (t *toolPrep) revalidate() string {
	now, err := os.Stat(t.p.RootPath)
	resolved, evalErr := filepath.EvalSymlinks(t.p.RootPath)
	exe, exeErr := os.Stat(t.executable)
	if err != nil || evalErr != nil || exeErr != nil || resolved != t.p.RootPath || !os.SameFile(t.rootInfo, now) || !os.SameFile(t.exeInfo, exe) {
		return "path_changed"
	}
	return ""
}

func (h *Hub) prepareTool(id, contextID string, tool engine.Tool, recursive bool) (*toolPrep, error) {
	if !engine.ValidTool(tool) {
		return nil, ErrInvalidTool
	}
	p, err := h.Registry.Get(id)
	if err != nil {
		return nil, err
	}
	c, _, err := h.Context(contextID)
	if err != nil || c.ProjectID != p.ID || c.RootPath != p.RootPath {
		return nil, ErrContextNotFound
	}
	refuse := func(code, message string) (*toolPrep, error) {
		return nil, &ToolRefusal{code, message}
	}
	if status, problem := check(p.RootPath); status != StatusReady {
		return refuse(status, problem)
	}
	if h.Tools == nil || h.Inits == nil {
		return refuse("unavailable", "Project tools are not available in this session.")
	}
	if _, shared := dataDir(); shared {
		return refuse("shared_data_dir", "TF_DATA_DIR is an absolute path, so every project would share one data directory. Restart Hclaro without it, or with a relative TF_DATA_DIR.")
	}
	environ := os.Environ()
	if tfpolicy.UnsafeArgs(environ, engine.ToolArgs(tool, true, false)[0], "version") {
		return refuse("unsafe_cli_args", "Inherited TF_CLI_ARGS or a command-specific TF_CLI_ARGS_* setting adds flags other than -no-color, which would change this command. Restart Hclaro without them.")
	}
	preference := p.EnginePreference
	notices := toolNotices(environ, tool)
	if c.Execution != nil {
		// The profile is resolved again: an edited or removed profile no longer describes
		// this context, so the tool does not run with it.
		pref, composed, now, err := h.execution(p, c.profileID)
		if err != nil || now == nil || now.Fingerprint != c.Execution.Fingerprint {
			return nil, ErrContextChanged
		}
		preference, environ, notices = pref, composed, append(notices, c.Execution.Notices...)
	}
	var files map[string]bool
	var folders int
	var skipped []string
	if tool == engine.ToolFormat {
		var refusal *ToolRefusal
		if files, folders, skipped, refusal = FormatScope(p.RootPath, c.Engine, recursive); refusal != nil {
			return nil, refusal
		}
	} else {
		recursive = false
	}
	name, executable, tools, err := h.Tools(p.RootPath, preference, environ, c.Workspace)
	if err != nil || c.Executable == "" {
		return refuse("missing_cli", "Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine.")
	}
	if name != c.Engine || executable != c.Executable {
		return nil, ErrContextChanged
	}
	rootInfo, statErr := statIdentity(p.RootPath)
	exeInfo, exeErr := statIdentity(executable)
	if statErr != nil || exeErr != nil {
		return nil, ErrContextChanged
	}
	release, ok := h.Locks.AcquireShared(p.RootPath)
	if !ok {
		return nil, ErrBusy
	}
	return &toolPrep{p, c, environ, notices, files, folders, skipped, recursive, name, executable, tools, rootInfo, exeInfo, release}, nil
}

// StartTool runs one read-only project utility in an open Hub context. The context pins the
// project, root, engine executable and workspace; the environment is pinned now. Utilities
// take a shared claim on the root, so several can run together but never alongside init, a
// workspace command or a plan.
func (h *Hub) StartTool(id, contextID string, tool engine.Tool, recursive bool) (operations.Operation, error) {
	t, err := h.prepareTool(id, contextID, tool, recursive)
	if err != nil {
		return operations.Operation{}, err
	}
	return h.Inits.StartTool(operations.ToolRequest{
		ProjectID: t.p.ID, ContextID: t.c.ID, Root: t.p.RootPath, Engine: t.name, EngineVersion: t.c.Snapshot().Info.Version, Workspace: t.c.Workspace, Tool: tool, Recursive: t.recursive,
		Tools: t.tools, Environ: t.environ, Notices: t.notices, Files: t.files, Folders: t.folders, Skipped: t.skipped, Context: t.c.Execution.Clone(),
		Revalidate: t.revalidate,
		Release:    t.release,
	})
}

// Operations returns the project's retained Hub operations, newest first.
func (h *Hub) Operations(id string) ([]operations.Operation, error) {
	if _, err := h.Registry.Get(id); err != nil {
		return nil, err
	}
	return h.Inits.List(id), nil
}

// ScannerInfo reports which scanners are installed and supported, and which of their own
// configuration files the project has. It only reads `--version`, and only when the Tools page
// asks.
func (h *Hub) ScannerInfo(ctx context.Context, id string) ([]scanners.Info, error) {
	p, err := h.Registry.Get(id)
	if err != nil {
		return nil, err
	}
	if h.Scanners == nil {
		return nil, ErrInvalidTool
	}
	// In parallel: a slow --version (Checkov) must not delay the others.
	infos := make([]scanners.Info, len(scanners.Names))
	var wg sync.WaitGroup
	for i, name := range scanners.Names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos[i] = h.Scanners.Detect(ctx, name, p.RootPath)
		}()
	}
	wg.Wait()
	return infos, nil
}

// StartScan runs one scanner over an open Hub context's project folder. Like StartTool it
// pins the context, takes a shared claim on the root and runs in the background. The scanner
// gets neither the engine nor the execution profile's environment.
func (h *Hub) StartScan(id, contextID, name string, recursive bool) (operations.Operation, error) {
	if !scanners.Valid(name) || h.Scanners == nil || recursive && name != scanners.TFLint {
		return operations.Operation{}, ErrInvalidTool
	}
	p, err := h.Registry.Get(id)
	if err != nil {
		return operations.Operation{}, err
	}
	c, _, err := h.Context(contextID)
	if err != nil || c.ProjectID != p.ID || c.RootPath != p.RootPath {
		return operations.Operation{}, ErrContextNotFound
	}
	if status, problem := check(p.RootPath); status != StatusReady {
		return operations.Operation{}, &ToolRefusal{status, problem}
	}
	if h.Inits == nil {
		return operations.Operation{}, &ToolRefusal{"unavailable", "Project tools are not available in this session."}
	}
	rootInfo, err := statIdentity(p.RootPath)
	if err != nil {
		return operations.Operation{}, ErrContextChanged
	}
	release, ok := h.Locks.AcquireShared(p.RootPath)
	if !ok {
		return operations.Operation{}, ErrBusy
	}
	return h.Inits.StartScan(operations.ToolRequest{
		ProjectID: p.ID, ContextID: c.ID, Root: p.RootPath, Engine: c.Engine, EngineVersion: c.Snapshot().Info.Version, Workspace: c.Workspace,
		Environ: os.Environ(), Scan: &operations.ScanRun{Name: name, Recursive: recursive, Runner: h.Scanners},
		Revalidate: func() string {
			now, err := os.Stat(p.RootPath)
			resolved, evalErr := filepath.EvalSymlinks(p.RootPath)
			if err != nil || evalErr != nil || resolved != p.RootPath || !os.SameFile(rootInfo, now) {
				return "path_changed"
			}
			return ""
		},
		Release: release,
	})
}

// ScanSummary is the latest finished scan of one scanner that still describes the project's
// configuration.
type ScanSummary struct {
	Scanner    string         `json:"scanner"`
	Counts     map[string]int `json:"counts"`
	FinishedAt string         `json:"finishedAt"`
}

// CurrentScans returns, per scanner, the newest finished scan of this context's workspace
// whose configuration fingerprint equals the current one. Nothing runs and nothing blocks.
func (h *Hub) CurrentScans(c *Context) []ScanSummary {
	out := []ScanSummary{}
	if h.Inits == nil {
		return out
	}
	now := workspace.Fingerprint(c.RootPath, nil, "scan")
	if now == "" {
		return out
	}
	seen := map[string]bool{}
	for _, op := range h.Inits.List(c.ProjectID) { // newest first
		t := op.Tool
		if op.Kind != "tool" || t == nil || t.Scan == nil || seen[t.Scan.Scanner] || t.Workspace != c.Workspace || t.Scan.Fingerprint != now || t.Outcome == "not_checked" {
			continue
		}
		seen[t.Scan.Scanner] = true
		out = append(out, ScanSummary{t.Scan.Scanner, maps.Clone(t.Scan.Counts), op.CompletedAt})
	}
	return out
}
