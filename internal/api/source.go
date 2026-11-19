package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"hclaro/internal/discovery"
	"hclaro/internal/gitctx"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// execSource is where a plan, apply, import, operation or discovery request runs: the launch
// project's manager (launchSource), one Hub workspace's execution scope (hubSource), or
// noScope for a Hub context that has none yet. The routes are registered once against it
// (execRoutes, importExecRoutes, discoveryExecRoutes). Plan and operation IDs of another
// source answer exactly like missing ones.
type execSource interface {
	Plans() []*operations.Plan
	Plan(id string) (*operations.Plan, bool)
	ContextStatus(p *operations.Plan) (bool, operations.ApplyStatus)
	SourceStatus(p *operations.Plan) gitctx.SourceStatus
	Snapshot() workspace.Snapshot
	StartPlan(ctx context.Context, req operations.PlanStart) (operations.Operation, error)
	Preflight(ctx context.Context, id string, req operations.PreflightRequest) (operations.PreflightReport, error)
	Approve(ctx context.Context, id string, req operations.ApprovalRequest) (operations.Approval, error)
	StartApply(ctx context.Context, id, token, typed string) (operations.Operation, error)
	PreviewImport(ctx context.Context, req imports.Request) (imports.Preview, error)
	StartImport(ctx context.Context, req operations.ImportRequest) (operations.Operation, error)
	Operations() []operations.Operation
	Operation(id string) (operations.Operation, *operations.Log, bool)
	Cancel(id string) (operations.Operation, error)
	// DiscoveryPin is the AWS discovery context and its environment. check is set for a
	// request that contacts AWS: the reviewed fingerprint must match and a Hub scope is
	// revalidated first. It runs nothing.
	DiscoveryPin(ctx context.Context, check bool, profileID, reviewed string) (discovery.Pin, []string, error)
	// Holder names the kind of operation holding the root, or "".
	Holder() string
	// View is the Hub scope, or nil for the launch project and a context without a scope.
	View() *projects.ScopeView
}

// sourceOf resolves the source of a request. create is set only for requests that start or
// prepare work, so reads never create or evict a Hub scope. It answers refusals itself.
type sourceOf func(w http.ResponseWriter, r *http.Request, create bool) (execSource, bool)

// launchSource is the launch project. Operation IDs also find Hub initializations, which
// share the launch operation routes.
type launchSource struct {
	*operations.Manager
	inits *operations.Inits
}

func (l launchSource) StartPlan(_ context.Context, req operations.PlanStart) (operations.Operation, error) {
	return l.StartPlanWith(req)
}
func (l launchSource) Approve(_ context.Context, id string, req operations.ApprovalRequest) (operations.Approval, error) {
	return l.Manager.Approve(id, req)
}
func (l launchSource) StartApply(_ context.Context, id, token, typed string) (operations.Operation, error) {
	return l.Manager.StartApply(id, token, typed)
}
func (l launchSource) PreviewImport(_ context.Context, req imports.Request) (imports.Preview, error) {
	return l.Manager.PreviewImport(req), nil
}
func (l launchSource) StartImport(_ context.Context, req operations.ImportRequest) (operations.Operation, error) {
	return l.Manager.StartImport(req)
}
func (l launchSource) Operation(id string) (operations.Operation, *operations.Log, bool) {
	if op, log, ok := l.Manager.Operation(id); ok {
		return op, log, ok
	}
	return l.inits.Operation(id)
}
func (l launchSource) Cancel(id string) (operations.Operation, error) {
	op, err := l.Manager.Cancel(id)
	if errors.Is(err, operations.ErrNotFound) && l.inits != nil {
		return l.inits.Cancel(id)
	}
	return op, err
}
func (l launchSource) DiscoveryPin(_ context.Context, _ bool, profileID, reviewed string) (discovery.Pin, []string, error) {
	return discoveryPin(l.Manager, profileID, reviewed)
}
func (launchSource) Holder() string            { return "" }
func (launchSource) View() *projects.ScopeView { return nil }

func launchSourceOf(manager *operations.Manager, inits *operations.Inits) sourceOf {
	return func(http.ResponseWriter, *http.Request, bool) (execSource, bool) {
		return launchSource{manager, inits}, true
	}
}

// hubSource is the execution scope of one Hub workspace. Its profile is the one the context
// was opened with; a request cannot name another.
type hubSource struct{ *projects.Scope }

func (h hubSource) StartPlan(ctx context.Context, req operations.PlanStart) (operations.Operation, error) {
	return h.StartReviewedPlan(ctx, req.VarFiles, req.Replace, req.ContextFingerprint)
}
func (h hubSource) StartImport(ctx context.Context, req operations.ImportRequest) (operations.Operation, error) {
	return h.Scope.StartImport(ctx, req.Request, req.PreviewSHA256, req.VarFiles, req.ContextFingerprint)
}
func (h hubSource) DiscoveryPin(ctx context.Context, check bool, _, reviewed string) (discovery.Pin, []string, error) {
	return scopeDiscoveryPin(ctx, h.Scope, check, reviewed)
}
func (h hubSource) View() *projects.ScopeView {
	v := h.Scope.View()
	return &v
}

// hubSourceOf resolves the execution scope of the request's {cid} context.
func hubSourceOf(hub *projects.Hub) sourceOf {
	return func(w http.ResponseWriter, r *http.Request, create bool) (execSource, bool) {
		s, ok := resolveScope(w, r, hub, create)
		if !ok {
			return nil, false
		}
		if s == nil {
			return noScope{}, true
		}
		return hubSource{s}, true
	}
}

// routesUnder registers "METHOD /path" handlers under prefix, wrapping them in scoped when
// hub is set (Hub context routes).
func routesUnder(mux *http.ServeMux, prefix string, hub *projects.Hub) func(string, http.HandlerFunc) {
	return func(pattern string, h http.HandlerFunc) {
		method, path, _ := strings.Cut(pattern, " ")
		if hub != nil {
			h = scoped(hub, h)
		}
		mux.HandleFunc(method+" "+prefix+path, h)
	}
}

// scoped serves a Hub context route. It checks the scoped execution switch and that {cid}
// is open before the handler reads the body, so nothing is revealed while it is off.
func scoped(hub *projects.Hub, handle http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if scopedContext(w, r, hub) {
			handle(w, r)
		}
	}
}

// mapOpError answers an error shared by every operation start, preflight, approval and
// apply, with one {error, reason} shape for launch and Hub requests; nothing ran. hub is nil
// for launch requests. It returns false for an error it does not know, which the caller
// answers with its own feature cases.
func mapOpError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, holder string, err error) bool {
	var rejected *operations.RejectError
	var scope *projects.ScopeError
	switch {
	case errors.As(err, &rejected):
		reasonJSON(w, 409, rejected.Code, rejected.Reason)
	case errors.As(err, &scope):
		projectError(w, hub, err)
	case hub != nil && errors.Is(err, projects.ErrContextNotFound):
		openContext(w, r, hub)
	case errors.Is(err, operations.ErrBusy):
		reason := "Another operation is running in this project. Wait for it to finish, then try again."
		if holder != "" {
			reason = "Another operation (" + holder + ") is running in this project. Wait for it to finish, then try again."
		}
		// The codes are kept from M24 (ADR "Stable error codes"): launch and Hub requests
		// already answered them before the routes were shared.
		code := "operation_running"
		if hub != nil {
			code = "project_busy"
		}
		reasonJSON(w, 409, code, reason)
	case errors.Is(err, operations.ErrLoading):
		reasonJSON(w, 409, "workspace_loading", "Hclaro is still reading this workspace. Try again when it is loaded.")
	case errors.Is(err, operations.ErrStateLocked):
		reasonJSON(w, 409, projects.CodeStateLocked, "Another process holds the state lock, so the state could not be read and nothing ran. Hclaro never unlocks state. It reads the state again for about two minutes; after that, read the workspace again once the other process has finished.")
	case errors.Is(err, operations.ErrVarFiles):
		reasonJSON(w, 400, "invalid_var_files", "Choose variable files from the options of this workspace.")
	case errors.Is(err, operations.ErrClosed):
		reasonJSON(w, 503, "shutting_down", "Hclaro is shutting down.")
	default:
		return false
	}
	return true
}

// profileOf is the execution profile a request body names. A launch request may name one; a
// Hub context's profile is fixed when it is opened, so a Hub request naming one is invalid.
func profileOf(w http.ResponseWriter, hub *projects.Hub, id *string) (string, bool) {
	if id == nil {
		return "", true
	}
	if hub != nil || !validProfileParam(*id) {
		errorJSON(w, 400, "invalid_request")
		return "", false
	}
	return *id, true
}
