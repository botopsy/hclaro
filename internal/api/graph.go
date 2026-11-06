package api

import (
	"errors"
	"net/http"
	"regexp"

	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

var nodePattern = regexp.MustCompile(`^n[0-9]{1,6}$`)

// graphRoutes serves dependency graphs of the launch workspace. Generating one is an
// explicit, CSRF-protected POST that runs `graph` only; every GET is side-effect free and
// never runs a command. Progress and cancellation use the operation routes.
func graphRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/graphs/options", func(w http.ResponseWriter, r *http.Request) {
		profileID := r.URL.Query().Get("profileId")
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		snap := store.Get()
		canGenerate, reason := manager.GraphCapability()
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
			CanGenerate  bool              `json:"canGenerate"`
			Reason       string            `json:"reason,omitempty"`
			Engine       string            `json:"engine"`
			Version      string            `json:"version"`
			Workspace    string            `json:"workspace"`
			Backend      string            `json:"backend"`
			Execution    string            `json:"executionMode"`
			Command      []string          `json:"command"`
			ProjectID    string            `json:"projectId,omitempty"`
			Context      *executionSummary `json:"context,omitempty"`
			ProfileError string            `json:"profileError,omitempty"`
		}{canGenerate, reason, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, snap.Info.Backend, snap.Info.ExecutionMode, []string{snap.Info.Engine, "graph"}, projectID, context, profileError})
	})
	mux.HandleFunc("POST /api/v1/graphs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// The preflight must have been shown: `graph` runs the engine and may read the backend.
			Acknowledge        *bool  `json:"acknowledgeEngineRun"`
			ProfileID          string `json:"profileId"`
			ContextFingerprint string `json:"contextFingerprint"`
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
		op, run, err := manager.StartGraph(operations.GraphStart{ProfileID: body.ProfileID, ContextFingerprint: body.ContextFingerprint})
		var rejected *operations.RejectError
		switch {
		case errors.As(err, &rejected):
			writeJSON(w, 409, map[string]string{"error": rejected.Code, "reason": rejected.Reason})
		case errors.Is(err, operations.ErrBusy):
			errorJSON(w, 409, "operation_running")
		case errors.Is(err, operations.ErrLoading):
			errorJSON(w, 409, "workspace_loading")
		case errors.Is(err, operations.ErrClosed):
			errorJSON(w, 503, "shutting_down")
		case err != nil:
			errorJSON(w, 500, "graph_not_started")
		default:
			writeJSON(w, 202, map[string]any{"operation": op, "graph": run})
		}
	})
	mux.HandleFunc("GET /api/v1/graphs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"graphs": manager.Graphs()})
	})
	mux.HandleFunc("GET /api/v1/graphs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, planID := r.PathValue("id"), r.URL.Query().Get("planId")
		if !idPattern.MatchString(id) || planID != "" && !idPattern.MatchString(planID) {
			errorJSON(w, 404, "graph_not_found")
			return
		}
		view, err := manager.Graph(id, planID)
		if err != nil {
			graphError(w, err)
			return
		}
		writeJSON(w, 200, view)
	})
	mux.HandleFunc("GET /api/v1/graphs/{id}/nodes/{node}", func(w http.ResponseWriter, r *http.Request) {
		id, node, planID := r.PathValue("id"), r.PathValue("node"), r.URL.Query().Get("planId")
		if !idPattern.MatchString(id) || !nodePattern.MatchString(node) || planID != "" && !idPattern.MatchString(planID) {
			errorJSON(w, 404, "node_not_found")
			return
		}
		impact, err := manager.GraphImpact(id, node, planID)
		if err != nil {
			graphError(w, err)
			return
		}
		writeJSON(w, 200, impact)
	})
}

func graphError(w http.ResponseWriter, err error) {
	var rejected *operations.RejectError
	switch {
	case errors.As(err, &rejected):
		writeJSON(w, 409, map[string]string{"error": rejected.Code, "reason": rejected.Reason})
	case errors.Is(err, operations.ErrGraphStale):
		writeJSON(w, 409, map[string]string{"error": "graph_stale", "reason": "This graph no longer matches the current workspace or context. Generate it again."})
	case errors.Is(err, operations.ErrPlanNotFound):
		errorJSON(w, 404, "plan_not_found")
	case errors.Is(err, operations.ErrNodeNotFound):
		errorJSON(w, 404, "node_not_found")
	default:
		errorJSON(w, 404, "graph_not_found")
	}
}
