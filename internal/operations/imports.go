package operations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"hclaro/internal/imports"
	"hclaro/internal/plan"
	"hclaro/internal/workspace"
)

// ImportTarget records what an import plan was asked to import. The import ID is not kept
// here: it lives in the configuration file the user approved.
type ImportTarget struct {
	Address       string `json:"address"`
	File          string `json:"file"`
	GeneratedFile string `json:"generatedFile,omitempty"`
	Provider      string `json:"provider,omitempty"`
}

// ImportRequest is a reviewed import: the request, the digest of the preview the user saw
// and the variable files for the plan.
type ImportRequest struct {
	imports.Request
	PreviewSHA256 string
	VarFiles      []string
	// ProfileID and ContextFingerprint bind the import plan to an execution profile.
	ProfileID          string
	ContextFingerprint string
}

func (m *Manager) importEnv(snap workspace.Snapshot) imports.Env {
	data := os.Getenv("TF_DATA_DIR")
	if data == "" {
		data = ".terraform"
	}
	if !filepath.IsAbs(data) {
		data = filepath.Join(m.root, data)
	}
	status := snap.Status
	// No recorded state is not an error for importing: nothing can collide.
	if snap.Diagnostic != nil && snap.Diagnostic.Code == "state_absent" {
		status = "empty"
	}
	return imports.Env{Root: m.root, DataDir: data, Engine: snap.Info.Engine, Snapshot: status, Index: snap.Index, SupportsGenerate: snap.Info.Capabilities.SupportsGeneratedConfig}
}

// PreviewImport checks a request against the current snapshot and configuration. It
// writes nothing and runs no engine command.
func (m *Manager) PreviewImport(req imports.Request) imports.Preview {
	snap := m.store.Get()
	p := imports.Check(m.importEnv(snap), req)
	if snap.Status == "loading" {
		p.Issues = append(p.Issues, imports.Issue{Code: "workspace_loading", Severity: "error", Message: "Hclaro is still reading the workspace."})
		p.Blocked = true
	}
	return p
}

// StartImport re-checks the request, verifies it still renders exactly what the user
// reviewed, writes the import block to a new file and starts a full plan. Nothing is
// written when any check fails.
func (m *Manager) StartImport(req ImportRequest) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Operation{}, ErrClosed
	}
	if m.running != nil {
		return Operation{}, ErrBusy
	}
	snap := m.store.Get()
	if snap.Status == "loading" {
		return Operation{}, ErrLoading
	}
	if m.planner == nil || !snap.Info.Capabilities.CanImport {
		return Operation{}, reject("import_unavailable", snap.Info.Capabilities.ImportReason)
	}
	bound, environ, err := m.bind(snap, req.ProfileID, req.ContextFingerprint)
	if err != nil {
		return Operation{}, err
	}
	selected, err := m.selectVarFiles(req.VarFiles, bound)
	if err != nil {
		return Operation{}, err
	}
	preview := imports.Check(m.importEnv(snap), req.Request)
	if preview.Blocked {
		for _, issue := range preview.Issues {
			if issue.Severity == "error" {
				return Operation{}, reject("import_blocked", issue.Message)
			}
		}
	}
	if preview.SHA256 == "" || preview.SHA256 != req.PreviewSHA256 {
		return Operation{}, reject("preview_changed", "The import block or destination changed since you reviewed it. Review the preview again.")
	}
	release, ok := m.Locks.Acquire(m.root, "import")
	if !ok {
		return Operation{}, ErrBusy
	}
	switch err := imports.WriteNew(m.root, preview.File, []byte(preview.Content)); {
	case errors.Is(err, imports.ErrFileExists):
		release()
		return Operation{}, reject("file_exists", fmt.Sprintf("%s already exists. Nothing was written.", preview.File))
	case err != nil:
		release()
		return Operation{}, reject("write_failed", fmt.Sprintf("%s could not be written safely. Nothing was planned.", preview.File))
	}
	target := &ImportTarget{Address: preview.Address, File: preview.File, GeneratedFile: preview.GeneratedFile, Provider: req.Provider}
	return m.startPlanLocked(snap, Operation{VarFiles: selected, Replace: []string{}, Import: target, Context: bound}, release, environ), nil
}

// importWarnings flags an import plan that does not import the requested address.
func importWarnings(review plan.Review, address string) []string {
	for _, c := range review.Changes {
		if c.Address == address && c.Importing {
			return nil
		}
	}
	return []string{fmt.Sprintf("The plan does not import %s. Check the import block and the plan diagnostics before applying anything.", address)}
}
