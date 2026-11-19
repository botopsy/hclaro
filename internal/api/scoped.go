package api

import (
	"errors"
	"net/http"
	"strconv"

	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// Scoped plan and apply routes for Hub workspaces (M24.05, M24.06, ADR M24.03 "API contract"). Every route names
// an open read context in its path; the server resolves the context's execution scope and
// never falls back to the launch project. Plan and operation IDs of another scope, or of the
// launch project, answer exactly like missing ones. Requests carry no path, executable,
// argv, flag, environment value, -target or plan file.

// resolveScope answers the request itself and returns nil unless the gate is on, the {cid}
// context is open and (with create) its execution scope exists or could be created.
// Without create, a context with no scope gives (nil, true).
func resolveScope(w http.ResponseWriter, r *http.Request, hub *projects.Hub, create bool) (*projects.Scope, bool) {
	if !scopedContext(w, r, hub) {
		return nil, false
	}
	var s *projects.Scope
	var err error
	if create {
		s, err = hub.Execution(r.PathValue("cid"))
	} else {
		s, err = hub.ExistingScope(r.PathValue("cid"))
	}
	if err != nil {
		scopeError(w, r, hub, err)
		return nil, false
	}
	return s, true
}

// scopedContext answers the request itself and returns false unless the gate is on and the
// {cid} context is open. The gate is checked first, so nothing is revealed while it is off.
func scopedContext(w http.ResponseWriter, r *http.Request, hub *projects.Hub) bool {
	if !hub.ScopedExecution {
		projectError(w, hub, projects.ErrScopedDisabled)
		return false
	}
	if !hubIDPattern.MatchString(r.PathValue("cid")) {
		reasonJSON(w, 404, "context_closed", "This workspace view is no longer open. Open the workspace again.")
		return false
	}
	return openContext(w, r, hub) != nil
}

// scopeError maps a scope resolution error. Nothing ran.
func scopeError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, err error) {
	if !mapOpError(w, r, hub, "", err) {
		projectError(w, hub, err)
	}
}

func scopedRoutes(mux *http.ServeMux, hub *projects.Hub) {
	// Options describe what a plan in this workspace would run against. Resolving them runs
	// no engine command and never creates or evicts an execution scope.
	mux.HandleFunc("GET /api/v1/contexts/{cid}/plans/options", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		type options struct {
			CanPlan bool `json:"canPlan"`
			// Code and Reason explain why planning is not available.
			Code   string              `json:"code,omitempty"`
			Reason string              `json:"reason,omitempty"`
			Scope  *projects.ScopeView `json:"scope"`
			// Backend access is always required: planning may contact providers and the
			// backend and take the state lock.
			BackendAccess    bool                         `json:"backendAccessRequired"`
			VarFiles         []string                     `json:"varFiles"`
			ExecutionContext *operations.ExecutionContext `json:"executionContext"`
			// ContextFingerprint must be sent back unchanged when starting a plan.
			ContextFingerprint string `json:"contextFingerprint"`
		}
		s, err := hub.Preview(r.PathValue("cid"))
		var unsupported *projects.ScopeError
		if errors.As(err, &unsupported) && (unsupported.Code == projects.CodeExecutionUnsupported || unsupported.Code == projects.CodeStateLocked) {
			writeJSON(w, 200, options{Code: unsupported.Code, Reason: unsupported.Message, BackendAccess: true, VarFiles: []string{}})
			return
		}
		if err != nil {
			scopeError(w, r, hub, err)
			return
		}
		// An open scope whose last read was blocked by a state lock cannot plan until the
		// state has been read (FU-WIN-STATE-READ).
		if snap := s.Snapshot(); snap.Locked() {
			writeJSON(w, 200, options{Code: projects.CodeStateLocked, Reason: snap.Info.Capabilities.PlanReason, BackendAccess: true, VarFiles: []string{}})
			return
		}
		view := s.View()
		writeJSON(w, 200, options{CanPlan: true, Scope: &view, BackendAccess: true, VarFiles: s.VarFiles(), ExecutionContext: s.Execution(), ContextFingerprint: s.ProfileFingerprint()})
	}))
	execRoutes(mux, "/api/v1/contexts/{cid}", hub, hubSourceOf(hub))
}

// eventCursor reads the SSE resume position from ?after or Last-Event-ID.
func eventCursor(r *http.Request) (int64, bool) {
	cursor := r.URL.Query().Get("after")
	if cursor == "" {
		cursor = r.Header.Get("Last-Event-ID")
	}
	if cursor == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	return n, err == nil && n >= 0
}
