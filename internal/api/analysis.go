package api

import (
	"net/http"

	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// analysisExecRoutes registers the drift scan and dependency graph start, list and read
// routes under prefix, once for the launch project (hub nil) and once for Hub workspaces,
// as execRoutes. The options routes differ in shape and stay with their callers. Starting
// one is an explicit, CSRF-protected POST; every GET is side-effect free and runs no command.
func analysisExecRoutes(mux *http.ServeMux, prefix string, hub *projects.Hub, source sourceOf) {
	handle := routesUnder(mux, prefix, hub)
	handle("POST /drift-scans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// The preflight must have been shown: a scan queries providers and the backend.
			Acknowledge        *bool    `json:"acknowledgeProviderAccess"`
			VarFiles           []string `json:"varFiles"`
			ProfileID          *string  `json:"profileId"`
			ContextFingerprint string   `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "A drift scan may contact providers and the backend and take the state lock. Confirm provider access first.")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		if len(body.ContextFingerprint) > 128 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		src, ok := source(w, r, true)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		op, scan, err := src.StartDriftScan(ctx, operations.DriftStart{VarFiles: body.VarFiles, ProfileID: profileID, ContextFingerprint: body.ContextFingerprint})
		if err != nil {
			analysisStartError(w, r, hub, src, err, "scan_not_started")
			return
		}
		writeJSON(w, 202, map[string]any{"operation": scopedOperation{op, src.View()}, "scan": scan})
	})
	handle("GET /drift-scans", func(w http.ResponseWriter, r *http.Request) {
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		scans := src.DriftScans()
		if scans == nil {
			scans = []operations.DriftScanView{}
		}
		writeJSON(w, 200, map[string]any{"scans": scans})
	})
	handle("GET /drift-scans/{id}", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := source(w, r, false); ok {
			serveDriftScan(w, r, src)
		}
	})
	handle("GET /drift-scans/{id}/resources/{change}", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := source(w, r, false); ok {
			serveDriftDetail(w, r, src)
		}
	})

	handle("POST /graphs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// The preflight must have been shown: `graph` runs the engine and may read the backend.
			Acknowledge        *bool   `json:"acknowledgeEngineRun"`
			ProfileID          *string `json:"profileId"`
			ContextFingerprint string  `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "Generating a graph runs the engine, which may read the backend. Confirm first.")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		if len(body.ContextFingerprint) > 128 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		src, ok := source(w, r, true)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		op, run, err := src.StartGraph(ctx, operations.GraphStart{ProfileID: profileID, ContextFingerprint: body.ContextFingerprint})
		if err != nil {
			analysisStartError(w, r, hub, src, err, "graph_not_started")
			return
		}
		writeJSON(w, 202, map[string]any{"operation": scopedOperation{op, src.View()}, "graph": run})
	})
	handle("GET /graphs", func(w http.ResponseWriter, r *http.Request) {
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		graphs := src.Graphs()
		if graphs == nil {
			graphs = []operations.GraphView{}
		}
		writeJSON(w, 200, map[string]any{"graphs": graphs})
	})
	handle("GET /graphs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := source(w, r, false); ok {
			serveGraph(w, r, src)
		}
	})
	handle("GET /graphs/{id}/nodes/{node}", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := source(w, r, false); ok {
			serveGraphImpact(w, r, src)
		}
	})
}

// analysisStartError answers a refused drift scan or graph start. Nothing ran.
func analysisStartError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, src execSource, err error, code string) {
	switch {
	case mapOpError(w, r, hub, src.Holder(), err):
	case hub != nil:
		projectError(w, hub, err)
	default:
		errorJSON(w, 500, code)
	}
}
