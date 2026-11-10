package projects

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
)

// ToolsFactory resolves the engine for a project utility with a pinned environment. selected
// is the context's workspace; the adapter passes it as TF_WORKSPACE only to the tools that
// read state. It returns the executable path so its identity can be checked.
type ToolsFactory func(root, preference string, environ []string, selected string) (name, executable string, tools engine.Tools, err error)

// DefaultTools uses the official CLI found on PATH. Inherited TF_WORKSPACE is never passed.
func DefaultTools(root, preference string, environ []string, selected string) (string, string, engine.Tools, error) {
	cli, err := engine.Detect(root, preference)
	if err != nil {
		return "", "", nil, err
	}
	cli.IgnoreInheritedWorkspace, cli.Environ, cli.SelectedWorkspace = true, environ, selected
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

// unsafeToolArgs reports inherited TF_CLI_ARGS or TF_CLI_ARGS_<command> flags other than
// -no-color. The engine adapter refuses them too; checking here refuses before any process.
func unsafeToolArgs(environ []string, tool engine.Tool) bool {
	command := engine.ToolArgs(tool, true, false)[0]
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if key == "TF_CLI_ARGS" || key == "TF_CLI_ARGS_"+command || key == "TF_CLI_ARGS_version" {
			for _, arg := range strings.Fields(value) {
				if arg != "-no-color" {
					return true
				}
			}
		}
	}
	return false
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

// StartTool runs one read-only project utility in an open Hub context. The context pins the
// project, root, engine executable and workspace; the environment is pinned now. Utilities
// take a shared claim on the root, so several can run together but never alongside init, a
// workspace command or a plan.
func (h *Hub) StartTool(id, contextID string, tool engine.Tool, recursive bool) (operations.Operation, error) {
	if !engine.ValidTool(tool) {
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
	refuse := func(code, message string) (operations.Operation, error) {
		return operations.Operation{}, &ToolRefusal{code, message}
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
	if unsafeToolArgs(environ, tool) {
		return refuse("unsafe_cli_args", "Inherited TF_CLI_ARGS or a command-specific TF_CLI_ARGS_* setting adds flags other than -no-color, which would change this command. Restart Hclaro without them.")
	}
	preference := p.EnginePreference
	notices := toolNotices(environ, tool)
	if c.Execution != nil {
		// The profile is resolved again: an edited or removed profile no longer describes
		// this context, so the tool does not run with it.
		pref, composed, now, err := h.execution(p, c.profileID)
		if err != nil || now == nil || now.Fingerprint != c.Execution.Fingerprint {
			return operations.Operation{}, ErrContextChanged
		}
		preference, environ, notices = pref, composed, append(notices, c.Execution.Notices...)
	}
	var files map[string]bool
	var folders int
	var skipped []string
	if tool == engine.ToolFormat {
		var refusal *ToolRefusal
		if files, folders, skipped, refusal = FormatScope(p.RootPath, c.Engine, recursive); refusal != nil {
			return operations.Operation{}, refusal
		}
	} else {
		recursive = false
	}
	name, executable, tools, err := h.Tools(p.RootPath, preference, environ, c.Workspace)
	if err != nil || c.Executable == "" {
		return refuse("missing_cli", "Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine.")
	}
	if name != c.Engine || executable != c.Executable {
		return operations.Operation{}, ErrContextChanged
	}
	rootInfo, statErr := statIdentity(p.RootPath)
	exeInfo, exeErr := statIdentity(executable)
	if statErr != nil || exeErr != nil {
		return operations.Operation{}, ErrContextChanged
	}
	release, ok := h.Locks.AcquireShared(p.RootPath)
	if !ok {
		return operations.Operation{}, ErrBusy
	}
	return h.Inits.StartTool(operations.ToolRequest{
		ProjectID: p.ID, ContextID: c.ID, Root: p.RootPath, Engine: name, EngineVersion: c.Snapshot().Info.Version, Workspace: c.Workspace, Tool: tool, Recursive: recursive,
		Tools: tools, Environ: environ, Notices: notices, Files: files, Folders: folders, Skipped: skipped, Context: c.Execution.Clone(),
		Revalidate: func() string {
			now, err := os.Stat(p.RootPath)
			resolved, evalErr := filepath.EvalSymlinks(p.RootPath)
			exe, exeErr := os.Stat(executable)
			if err != nil || evalErr != nil || exeErr != nil || resolved != p.RootPath || !os.SameFile(rootInfo, now) || !os.SameFile(exeInfo, exe) {
				return "path_changed"
			}
			return ""
		},
		Release: release,
	})
}

// Operations returns the project's retained Hub operations, newest first.
func (h *Hub) Operations(id string) ([]operations.Operation, error) {
	if _, err := h.Registry.Get(id); err != nil {
		return nil, err
	}
	return h.Inits.List(id), nil
}
