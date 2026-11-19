package api

import (
	"errors"
	"net/http"

	"hclaro/internal/discovery"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// Scoped import routes for Hub workspaces (M24.08). They mirror the launch import routes,
// bound to one open read context: the preview checks that workspace's state and root, and
// the start writes the reviewed block into that root only, then runs a full plan in the
// scope. The import is recorded only by applying that saved plan through the scoped
// preflight, approval and apply. Requests carry no path, profile or workspace. A discovery
// link (M24.09) must name a candidate prepared in this same context.
func scopedImportRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/contexts/{cid}/imports/options", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
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
		s, err := hub.Preview(r.PathValue("cid"))
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
	}))
	importExecRoutes(mux, "/api/v1/contexts/{cid}", hub, func() *discovery.Service { return hub.Discovery }, hubSourceOf(hub))
}
