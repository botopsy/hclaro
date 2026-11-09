package api

import (
	"errors"
	"net/http"

	"hclaro/internal/discovery"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

func importRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager) {
	mux.HandleFunc("GET /api/v1/imports/options", func(w http.ResponseWriter, r *http.Request) {
		snap := store.Get()
		caps := snap.Info.Capabilities
		writeJSON(w, 200, struct {
			CanImport        bool     `json:"canImport"`
			Reason           string   `json:"reason,omitempty"`
			CanApply         bool     `json:"canApply"`
			ApplyReason      string   `json:"applyReason,omitempty"`
			SupportsBlocks   bool     `json:"supportsImportBlocks"`
			SupportsGenerate bool     `json:"supportsGeneratedConfig"`
			Engine           string   `json:"engine"`
			Version          string   `json:"version"`
			Workspace        string   `json:"workspace"`
			Backend          string   `json:"backend"`
			VarFiles         []string `json:"varFiles"`
		}{caps.CanImport, caps.ImportReason, caps.CanApply, caps.ApplyReason, caps.SupportsImportBlocks, caps.SupportsGeneratedConfig, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, snap.Info.Backend, manager.VarFiles()})
	})
	// Preview is side-effect free: validation, collision checks and the exact file content.
	mux.HandleFunc("POST /api/v1/imports/preview", func(w http.ResponseWriter, r *http.Request) {
		var body imports.Request
		if !decodeBody(w, r, &body) {
			return
		}
		writeJSON(w, 200, manager.PreviewImport(body))
	})
	mux.HandleFunc("POST /api/v1/imports", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			imports.Request
			VarFiles      []string `json:"varFiles"`
			PreviewSHA256 string   `json:"previewSha256"`
			// Both acknowledgements are required: the file write and the backend access.
			AcknowledgeWrite   *bool `json:"acknowledgeWrite"`
			AcknowledgeBackend *bool `json:"acknowledgeBackendAccess"`
			// ProfileID and ContextFingerprint bind the import plan to a reviewed profile.
			ProfileID          string `json:"profileId"`
			ContextFingerprint string `json:"contextFingerprint"`
			// DiscoveryToken, when set, links the import to a candidate prepared from an AWS
			// discovery scan (M16); the server checks it still matches before planning.
			DiscoveryToken string `json:"discoveryToken"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.AcknowledgeWrite == nil || !*body.AcknowledgeWrite || body.AcknowledgeBackend == nil || !*body.AcknowledgeBackend {
			errorJSON(w, 400, "acknowledgement_required")
			return
		}
		if !validProfileParam(body.ProfileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		if body.DiscoveryToken != "" && !checkDiscoveryImport(w, manager, body.DiscoveryToken, body.ProfileID, body.ContextFingerprint, func(pin discovery.Pin) error {
			return manager.Discovery.CheckImport(body.DiscoveryToken, pin, body.Request)
		}) {
			return
		}
		op, err := manager.StartImport(operations.ImportRequest{Request: body.Request, PreviewSHA256: body.PreviewSHA256, VarFiles: body.VarFiles, ProfileID: body.ProfileID, ContextFingerprint: body.ContextFingerprint})
		var rejected *operations.RejectError
		switch {
		case errors.As(err, &rejected):
			writeJSON(w, 409, map[string]string{"error": rejected.Code, "reason": rejected.Reason})
		case errors.Is(err, operations.ErrBusy):
			errorJSON(w, 409, "operation_running")
		case errors.Is(err, operations.ErrLoading):
			errorJSON(w, 409, "workspace_loading")
		case errors.Is(err, operations.ErrVarFiles):
			errorJSON(w, 400, "invalid_var_files")
		case errors.Is(err, operations.ErrClosed):
			errorJSON(w, 503, "shutting_down")
		case err != nil:
			errorJSON(w, 500, "import_not_started")
		default:
			if body.DiscoveryToken != "" {
				manager.Discovery.Consume(body.DiscoveryToken)
			}
			writeJSON(w, 202, op)
		}
	})
}
