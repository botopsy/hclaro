package api

import (
	"errors"
	"net/http"

	"hclaro/internal/discovery"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

func importRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, inits *operations.Inits) {
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
	importExecRoutes(mux, "/api/v1", nil, func() *discovery.Service { return manager.Discovery }, launchSourceOf(manager, inits))
}

// importExecRoutes registers the import preview and start under prefix, as execRoutes. The
// preview writes nothing and runs no engine command. The start writes the reviewed block
// into the source's root only, then runs a full plan there; the import is recorded only by
// applying that saved plan through preflight, approval and apply. A discovery link must name
// a candidate prepared in the same context.
func importExecRoutes(mux *http.ServeMux, prefix string, hub *projects.Hub, svc func() *discovery.Service, source sourceOf) {
	handle := routesUnder(mux, prefix, hub)
	handle("POST /imports/preview", func(w http.ResponseWriter, r *http.Request) {
		var body imports.Request
		if !decodeBody(w, r, &body) {
			return
		}
		src, ok := source(w, r, true)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		preview, err := src.PreviewImport(ctx, body)
		if err != nil {
			scopeError(w, r, hub, err)
			return
		}
		writeJSON(w, 200, struct {
			imports.Preview
			Scope *projects.ScopeView `json:"scope,omitempty"`
		}{preview, src.View()})
	})
	handle("POST /imports", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			imports.Request
			VarFiles      []string `json:"varFiles"`
			PreviewSHA256 string   `json:"previewSha256"`
			// Both acknowledgements are required: the file write and the backend access.
			AcknowledgeWrite   *bool `json:"acknowledgeWrite"`
			AcknowledgeBackend *bool `json:"acknowledgeBackendAccess"`
			// ProfileID (launch only) and ContextFingerprint bind the import plan to the
			// reviewed context.
			ProfileID          *string `json:"profileId"`
			ContextFingerprint string  `json:"contextFingerprint"`
			// DiscoveryToken, when set, links the import to a candidate prepared from an AWS
			// discovery scan; the server checks it still matches before planning.
			DiscoveryToken string `json:"discoveryToken"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.AcknowledgeWrite == nil || !*body.AcknowledgeWrite || body.AcknowledgeBackend == nil || !*body.AcknowledgeBackend {
			reasonJSON(w, 400, "acknowledgement_required", "Importing writes a new file to this project and runs a plan that may contact providers and the backend. Confirm both first.")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		if len(body.ContextFingerprint) > 128 || len(body.PreviewSHA256) > 128 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		src, ok := source(w, r, true)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		discoveries := svc()
		if body.DiscoveryToken != "" {
			stale := discoveries == nil || !idPattern.MatchString(body.DiscoveryToken)
			if !stale {
				pin, _, err := src.DiscoveryPin(ctx, false, profileID, body.ContextFingerprint)
				var rejected *operations.RejectError
				if errors.As(err, &rejected) {
					reasonJSON(w, 409, rejected.Code, rejected.Reason)
					return
				}
				stale = err != nil || discoveries.CheckImport(body.DiscoveryToken, pin, body.Request) != nil
			}
			if stale {
				reasonJSON(w, 409, "discovery_stale", "The import no longer matches the discovered object or its context (another account, workspace, project, profile, import ID or resource type, or the preparation expired). Prepare it again from Cloud discovery, or clear the discovery link to import manually.")
				return
			}
		}
		op, err := src.StartImport(ctx, operations.ImportRequest{Request: body.Request, PreviewSHA256: body.PreviewSHA256, VarFiles: body.VarFiles, ProfileID: profileID, ContextFingerprint: body.ContextFingerprint})
		switch {
		case err == nil:
			if body.DiscoveryToken != "" {
				discoveries.Consume(body.DiscoveryToken)
			}
			writeJSON(w, 202, scopedOperation{op, src.View()})
		case mapOpError(w, r, hub, src.Holder(), err):
		default:
			errorJSON(w, 500, "import_not_started")
		}
	})
}
