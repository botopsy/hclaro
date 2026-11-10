package api

import (
	"errors"
	"net/http"

	"hclaro/internal/drift"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// driftRoutes serves drift scans of the launch workspace. Starting one is an explicit,
// CSRF-protected POST; every GET is side-effect free and never runs a command. Progress and
// cancellation use the operation routes (the scan's operationId).
func driftRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/drift-scans/options", func(w http.ResponseWriter, r *http.Request) {
		profileID := r.URL.Query().Get("profileId")
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		snap := store.Get()
		canScan, reason := manager.DriftCapability()
		summary, registered, err := launchContext(hub, snap, profileID)
		var context *executionSummary
		projectID := ""
		if registered && err == nil {
			context, projectID = &summary, summary.ProjectID
		} else if registered {
			projectID = hub.LaunchID()
		}
		profileError := ""
		var refusal *projects.ProfileRefusal
		if errors.As(err, &refusal) {
			profileError = refusal.Message
		}
		writeJSON(w, 200, struct {
			CanScan      bool              `json:"canScan"`
			Reason       string            `json:"reason,omitempty"`
			Engine       string            `json:"engine"`
			Version      string            `json:"version"`
			Workspace    string            `json:"workspace"`
			Backend      string            `json:"backend"`
			Execution    string            `json:"executionMode"`
			VarFiles     []string          `json:"varFiles"`
			ProjectID    string            `json:"projectId,omitempty"`
			Context      *executionSummary `json:"context,omitempty"`
			ProfileError string            `json:"profileError,omitempty"`
		}{canScan, reason, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, snap.Info.Backend, snap.Info.ExecutionMode, manager.VarFiles(), projectID, context, profileError})
	})
	mux.HandleFunc("POST /api/v1/drift-scans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// The preflight must have been shown: a scan queries providers and the backend.
			Acknowledge        *bool    `json:"acknowledgeProviderAccess"`
			VarFiles           []string `json:"varFiles"`
			ProfileID          string   `json:"profileId"`
			ContextFingerprint string   `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			errorJSON(w, 400, "acknowledgement_required")
			return
		}
		if !validProfileParam(body.ProfileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		op, scan, err := manager.StartDriftScan(operations.DriftStart{VarFiles: body.VarFiles, ProfileID: body.ProfileID, ContextFingerprint: body.ContextFingerprint})
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
			errorJSON(w, 500, "scan_not_started")
		default:
			writeJSON(w, 202, map[string]any{"operation": op, "scan": scan})
		}
	})
	mux.HandleFunc("GET /api/v1/drift-scans", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"scans": manager.DriftScans()})
	})
	mux.HandleFunc("GET /api/v1/drift-scans/{id}", func(w http.ResponseWriter, r *http.Request) {
		serveDriftScan(w, r, manager)
	})
	mux.HandleFunc("GET /api/v1/drift-scans/{id}/resources/{change}", func(w http.ResponseWriter, r *http.Request) {
		serveDriftDetail(w, r, manager)
	})
}

// driftSource is the launch manager or a Hub workspace's execution scope.
type driftSource interface {
	DriftScan(id string) (operations.DriftScanView, bool)
	DriftDetail(id, change string) (drift.Detail, error)
}

func serveDriftScan(w http.ResponseWriter, r *http.Request, src driftSource) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		errorJSON(w, 404, "scan_not_found")
		return
	}
	scan, ok := src.DriftScan(id)
	if !ok {
		errorJSON(w, 404, "scan_not_found")
		return
	}
	writeJSON(w, 200, scan)
}

func serveDriftDetail(w http.ResponseWriter, r *http.Request, src driftSource) {
	id, change := r.PathValue("id"), r.PathValue("change")
	if !idPattern.MatchString(id) || !idPattern.MatchString(change) {
		errorJSON(w, 404, "change_not_found")
		return
	}
	detail, err := src.DriftDetail(id, change)
	switch {
	case errors.Is(err, operations.ErrScanStale):
		writeJSON(w, 409, map[string]string{"error": "scan_stale", "reason": "This scan no longer matches the current workspace or context. Scan again to see current drift."})
	case err != nil:
		errorJSON(w, 404, "change_not_found")
	default:
		writeJSON(w, 200, detail)
	}
}
