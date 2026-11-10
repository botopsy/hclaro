package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"hclaro/internal/gitctx"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// Scoped plan and apply routes for Hub workspaces (M24.05, M24.06, ADR M24.03 "API contract"). Every route names
// an open read context in its path; the server resolves the context's execution scope and
// never falls back to the launch project. Plan and operation IDs of another scope, or of the
// launch project, answer exactly like missing ones. Requests carry no path, executable,
// argv, flag, environment value, -target or plan file.

// scopedOperation is an operation of an execution scope with the scope it ran in.
type scopedOperation struct {
	operations.Operation
	Scope projects.ScopeView `json:"scope"`
}

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

// scopeError maps a scope resolution or plan start error. Nothing ran.
func scopeError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, err error) {
	switch {
	case errors.Is(err, projects.ErrContextNotFound):
		openContext(w, r, hub)
	case errors.Is(err, operations.ErrLoading):
		reasonJSON(w, 409, "workspace_loading", "Hclaro is still reading this workspace. Try again when it is loaded.")
	case errors.Is(err, operations.ErrClosed):
		reasonJSON(w, 503, "shutting_down", "Hclaro is shutting down.")
	default:
		projectError(w, hub, err)
	}
}

func scopedRoutes(mux *http.ServeMux, hub *projects.Hub) {
	// Options describe what a plan in this workspace would run against. Resolving them
	// creates the execution scope, which runs no engine command.
	mux.HandleFunc("GET /api/v1/contexts/{cid}/plans/options", func(w http.ResponseWriter, r *http.Request) {
		if !scopedContext(w, r, hub) {
			return
		}
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
		s, err := hub.Execution(r.PathValue("cid"))
		var unsupported *projects.ScopeError
		if errors.As(err, &unsupported) && unsupported.Code == projects.CodeExecutionUnsupported {
			writeJSON(w, 200, options{Code: unsupported.Code, Reason: unsupported.Message, BackendAccess: true, VarFiles: []string{}})
			return
		}
		if err != nil {
			scopeError(w, r, hub, err)
			return
		}
		view := s.View()
		writeJSON(w, 200, options{CanPlan: true, Scope: &view, BackendAccess: true, VarFiles: s.VarFiles(), ExecutionContext: s.Execution(), ContextFingerprint: s.ProfileFingerprint()})
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/plans", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body struct {
			Acknowledge *bool    `json:"acknowledgeBackendAccess"`
			VarFiles    []string `json:"varFiles"`
			// Replace holds opaque resource IDs from this context's snapshot.
			Replace []string `json:"replace"`
			// ContextFingerprint is the one the plan options returned.
			ContextFingerprint string `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "Planning may contact providers and the backend and take the state lock. Confirm backend access first.")
			return
		}
		if len(body.ContextFingerprint) > 128 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		s, ok := resolveScope(w, r, hub, true)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		op, err := s.StartReviewedPlan(ctx, body.VarFiles, body.Replace, body.ContextFingerprint)
		var rejected *operations.RejectError
		switch {
		case errors.As(err, &rejected):
			reasonJSON(w, 409, rejected.Code, rejected.Reason)
		case errors.Is(err, operations.ErrBusy):
			reason := "Another operation is running in this project. Wait for it to finish, then try again."
			if kind := s.Holder(); kind != "" {
				reason = "Another operation (" + kind + ") is running in this project. Wait for it to finish, then try again."
			}
			reasonJSON(w, 409, "project_busy", reason)
		case errors.Is(err, operations.ErrUnavailable):
			reasonJSON(w, 409, projects.CodeExecutionUnsupported, "Planning is not available for this workspace.")
		case errors.Is(err, operations.ErrVarFiles):
			reasonJSON(w, 400, "invalid_var_files", "Choose variable files from the plan options of this workspace.")
		case errors.Is(err, operations.ErrReplace):
			reasonJSON(w, 400, "invalid_replace", "Choose managed resources of this workspace to replace.")
		case err != nil:
			scopeError(w, r, hub, err)
		default:
			writeJSON(w, 202, scopedOperation{op, s.View()})
		}
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/plans", func(w http.ResponseWriter, r *http.Request) {
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		out := []planSummary{}
		var view *projects.ScopeView
		if s != nil {
			for _, p := range s.Plans() {
				out = append(out, summarize(p))
			}
			v := s.View()
			view = &v
		}
		writeJSON(w, 200, map[string]any{"plans": out, "scope": view})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/plans/{pid}", func(w http.ResponseWriter, r *http.Request) {
		s, p, ok := scopedPlan(w, r, hub)
		if !ok {
			return
		}
		changed, status := s.ContextStatus(p)
		writeJSON(w, 200, struct {
			*operations.Plan
			EngineVersionLabel string                 `json:"currentEngineVersion"`
			ContextChanged     bool                   `json:"contextChanged"`
			Apply              operations.ApplyStatus `json:"apply"`
			SourceStatus       gitctx.SourceStatus    `json:"sourceStatus"`
			Scope              projects.ScopeView     `json:"scope"`
		}{p, s.Snapshot().Info.Version, changed, status, s.SourceStatus(p), s.View()})
	})
	// Preflight, approval and apply of a scope's saved plan (M24.06). They reuse the launch
	// project's policy (operations.Manager) in the scope's own manager, so approvals and
	// preflight bindings exist only in that scope. Each request revalidates the scope first;
	// the apply revalidates it again, fully, with the root claim held.
	mux.HandleFunc("POST /api/v1/contexts/{cid}/plans/{pid}/preflight", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body struct {
			VerifyIdentity bool `json:"verifyIdentity"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		s, ok := scopedPlanScope(w, r, hub)
		if !ok {
			return
		}
		report, err := s.Preflight(r.Context(), r.PathValue("pid"), operations.PreflightRequest{VerifyIdentity: body.VerifyIdentity})
		if err != nil {
			scopedApplyError(w, r, hub, err)
			return
		}
		writeJSON(w, 200, struct {
			operations.PreflightReport
			Scope projects.ScopeView `json:"scope"`
		}{report, s.View()})
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/plans/{pid}/approval", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body struct {
			Binding      string   `json:"preflightBinding"`
			Acknowledged []string `json:"acknowledged"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if len(body.Binding) > 128 || len(body.Acknowledged) > 32 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		for _, a := range body.Acknowledged {
			if len(a) > 64 {
				errorJSON(w, 400, "invalid_request")
				return
			}
		}
		s, ok := scopedPlanScope(w, r, hub)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		approval, err := s.Approve(ctx, r.PathValue("pid"), operations.ApprovalRequest{Binding: body.Binding, Acknowledged: body.Acknowledged})
		if err != nil {
			scopedApplyError(w, r, hub, err)
			return
		}
		writeJSON(w, 200, struct {
			operations.Approval
			Scope projects.ScopeView `json:"scope"`
		}{approval, s.View()})
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/plans/{pid}/apply", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body struct {
			Token        string `json:"confirmationToken"`
			Confirmation string `json:"confirmation"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if len(body.Token) > 128 || len(body.Confirmation) > 256 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		s, ok := scopedPlanScope(w, r, hub)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		op, err := s.StartApply(ctx, r.PathValue("pid"), body.Token, body.Confirmation)
		if err != nil {
			scopedApplyError(w, r, hub, err)
			return
		}
		writeJSON(w, 202, scopedOperation{op, s.View()})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/plans/{pid}/changes/{change}", func(w http.ResponseWriter, r *http.Request) {
		_, p, ok := scopedPlan(w, r, hub)
		if !ok {
			return
		}
		change := r.PathValue("change")
		detail, found := p.Details[change]
		if !idPattern.MatchString(change) || !found {
			errorJSON(w, 404, "change_not_found")
			return
		}
		writeJSON(w, 200, detail)
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/operations", func(w http.ResponseWriter, r *http.Request) {
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		ops := []operations.Operation{}
		var view *projects.ScopeView
		if s != nil {
			ops = s.Operations()
			v := s.View()
			view = &v
		}
		writeJSON(w, 200, map[string]any{"operations": ops, "scope": view})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/operations/{oid}", func(w http.ResponseWriter, r *http.Request) {
		s, op, _, ok := scopedOperationOf(w, r, hub)
		if !ok {
			return
		}
		writeJSON(w, 200, scopedOperation{op, s.View()})
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/operations/{oid}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		if s == nil || !idPattern.MatchString(r.PathValue("oid")) {
			errorJSON(w, 404, "operation_not_found")
			return
		}
		op, err := s.Cancel(r.PathValue("oid"))
		switch {
		case errors.Is(err, operations.ErrNotFound):
			errorJSON(w, 404, "operation_not_found")
		case errors.Is(err, operations.ErrNotRunning):
			errorJSON(w, 409, "operation_not_running")
		default:
			writeJSON(w, 202, scopedOperation{op, s.View()})
		}
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/operations/{oid}/events", func(w http.ResponseWriter, r *http.Request) {
		s, _, log, ok := scopedOperationOf(w, r, hub)
		if !ok {
			return
		}
		if log == nil {
			errorJSON(w, 410, "log_not_retained")
			return
		}
		after, ok := eventCursor(r)
		if !ok {
			errorJSON(w, 400, "invalid_cursor")
			return
		}
		oid := r.PathValue("oid")
		stream(w, r, func() operations.Operation {
			op, _, _ := s.Operation(oid)
			return op
		}, log, after)
	})
}

// scopedPlan finds a plan of the request's scope; anything else is plan_not_found.
func scopedPlan(w http.ResponseWriter, r *http.Request, hub *projects.Hub) (*projects.Scope, *operations.Plan, bool) {
	s, ok := resolveScope(w, r, hub, false)
	if !ok {
		return nil, nil, false
	}
	if s == nil || !idPattern.MatchString(r.PathValue("pid")) {
		errorJSON(w, 404, "plan_not_found")
		return nil, nil, false
	}
	p, found := s.Plan(r.PathValue("pid"))
	if !found {
		errorJSON(w, 404, "plan_not_found")
		return nil, nil, false
	}
	return s, p, true
}

// scopedPlanScope resolves the scope of a preflight, approval or apply request without
// creating one: a context with no scope has no plans. Anything else is plan_not_found.
func scopedPlanScope(w http.ResponseWriter, r *http.Request, hub *projects.Hub) (*projects.Scope, bool) {
	s, ok := resolveScope(w, r, hub, false)
	if !ok {
		return nil, false
	}
	if s == nil || !idPattern.MatchString(r.PathValue("pid")) {
		errorJSON(w, 404, "plan_not_found")
		return nil, false
	}
	return s, true
}

// scopedApplyError maps a refused scoped preflight, approval or apply. Nothing was applied.
func scopedApplyError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, err error) {
	var scope *projects.ScopeError
	var rejected *operations.RejectError
	switch {
	case errors.As(err, &scope):
		projectError(w, hub, err)
	case errors.Is(err, operations.ErrNotFound):
		errorJSON(w, 404, "plan_not_found")
	case errors.As(err, &rejected):
		reasonJSON(w, 409, rejected.Code, rejected.Reason)
	case errors.Is(err, operations.ErrClosed):
		reasonJSON(w, 503, "shutting_down", "Hclaro is shutting down.")
	default:
		errorJSON(w, 500, "apply_not_started")
	}
}

// scopedOperationOf finds an operation of the request's scope; anything else is
// operation_not_found.
func scopedOperationOf(w http.ResponseWriter, r *http.Request, hub *projects.Hub) (*projects.Scope, operations.Operation, *operations.Log, bool) {
	s, ok := resolveScope(w, r, hub, false)
	if !ok {
		return nil, operations.Operation{}, nil, false
	}
	if s == nil || !idPattern.MatchString(r.PathValue("oid")) {
		errorJSON(w, 404, "operation_not_found")
		return nil, operations.Operation{}, nil, false
	}
	op, log, found := s.Operation(r.PathValue("oid"))
	if !found {
		errorJSON(w, 404, "operation_not_found")
		return nil, operations.Operation{}, nil, false
	}
	return s, op, log, true
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
