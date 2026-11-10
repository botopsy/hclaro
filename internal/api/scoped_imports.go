package api

import (
	"context"
	"errors"
	"net/http"

	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// Scoped import routes for Hub workspaces (M24.08). They mirror the launch import routes,
// bound to one open read context: the preview checks that workspace's state and root, and
// the start writes the reviewed block into that root only, then runs a full plan in the
// scope. The import is recorded only by applying that saved plan through the scoped
// preflight, approval and apply. Requests carry no path, profile, workspace or discovery link.
func scopedImportRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/contexts/{cid}/imports/options", func(w http.ResponseWriter, r *http.Request) {
		if !scopedContext(w, r, hub) {
			return
		}
		type options struct {
			CanImport          bool                         `json:"canImport"`
			Code               string                       `json:"code,omitempty"`
			Reason             string                       `json:"reason,omitempty"`
			CanApply           bool                         `json:"canApply"`
			ApplyReason        string                       `json:"applyReason,omitempty"`
			SupportsBlocks     bool                         `json:"supportsImportBlocks"`
			SupportsGenerate   bool                         `json:"supportsGeneratedConfig"`
			Engine             string                       `json:"engine"`
			Version            string                       `json:"version"`
			Workspace          string                       `json:"workspace"`
			Backend            string                       `json:"backend"`
			VarFiles           []string                     `json:"varFiles"`
			Scope              *projects.ScopeView          `json:"scope"`
			ExecutionContext   *operations.ExecutionContext `json:"executionContext"`
			ContextFingerprint string                       `json:"contextFingerprint"`
		}
		s, err := hub.Execution(r.PathValue("cid"))
		var unsupported *projects.ScopeError
		if errors.As(err, &unsupported) && unsupported.Code == projects.CodeExecutionUnsupported {
			writeJSON(w, 200, options{Code: unsupported.Code, Reason: unsupported.Message, VarFiles: []string{}})
			return
		}
		if err != nil {
			scopeError(w, r, hub, err)
			return
		}
		snap := s.Snapshot()
		caps := snap.Info.Capabilities
		view := s.View()
		writeJSON(w, 200, options{
			CanImport: caps.CanImport, Reason: caps.ImportReason, CanApply: caps.CanApply, ApplyReason: caps.ApplyReason,
			SupportsBlocks: caps.SupportsImportBlocks, SupportsGenerate: caps.SupportsGeneratedConfig,
			Engine: view.Engine, Version: view.Version, Workspace: view.Workspace, Backend: view.Backend,
			VarFiles: s.VarFiles(), Scope: &view, ExecutionContext: s.Execution(), ContextFingerprint: s.ProfileFingerprint(),
		})
	})
	// The preview writes nothing and runs no engine command.
	mux.HandleFunc("POST /api/v1/contexts/{cid}/imports/preview", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body imports.Request
		if !decodeBody(w, r, &body) {
			return
		}
		s, ok := resolveScope(w, r, hub, true)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		preview, err := s.PreviewImport(ctx, body)
		if err != nil {
			scopeError(w, r, hub, err)
			return
		}
		writeJSON(w, 200, struct {
			imports.Preview
			Scope projects.ScopeView `json:"scope"`
		}{preview, s.View()})
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/imports", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body struct {
			imports.Request
			VarFiles      []string `json:"varFiles"`
			PreviewSHA256 string   `json:"previewSha256"`
			// Both acknowledgements are required: the file write and the backend access.
			AcknowledgeWrite   *bool `json:"acknowledgeWrite"`
			AcknowledgeBackend *bool `json:"acknowledgeBackendAccess"`
			// ContextFingerprint is the one the import options returned.
			ContextFingerprint string `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.AcknowledgeWrite == nil || !*body.AcknowledgeWrite || body.AcknowledgeBackend == nil || !*body.AcknowledgeBackend {
			reasonJSON(w, 400, "acknowledgement_required", "Importing writes a new file to this project and runs a plan that may contact providers and the backend. Confirm both first.")
			return
		}
		if len(body.ContextFingerprint) > 128 || len(body.PreviewSHA256) > 128 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		s, ok := resolveScope(w, r, hub, true)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		op, err := s.StartImport(ctx, body.Request, body.PreviewSHA256, body.VarFiles, body.ContextFingerprint)
		var rejected *operations.RejectError
		switch {
		case errors.As(err, &rejected):
			reasonJSON(w, 409, rejected.Code, rejected.Reason)
		case errors.Is(err, operations.ErrBusy):
			reason := "Another operation is running in this project. Wait for it to finish, then try again. Nothing was written."
			if kind := s.Holder(); kind != "" {
				reason = "Another operation (" + kind + ") is running in this project. Wait for it to finish, then try again. Nothing was written."
			}
			reasonJSON(w, 409, "project_busy", reason)
		case errors.Is(err, operations.ErrVarFiles):
			reasonJSON(w, 400, "invalid_var_files", "Choose variable files from the import options of this workspace.")
		case err != nil:
			scopeError(w, r, hub, err)
		default:
			writeJSON(w, 202, scopedOperation{op, s.View()})
		}
	})
}
