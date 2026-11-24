package api

import (
	"context"
	"errors"
	"net/http"

	"hclaro/internal/discovery"
	"hclaro/internal/drift"
	"hclaro/internal/engine"
	"hclaro/internal/gitctx"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// Scoped drift scan and dependency graph routes for Hub workspaces (M24.09). They mirror the
// launch routes, bound to one open read context: the scan or graph runs in that workspace's
// execution scope only, and its results are kept in that scope only, within the launch
// retention bounds, and dropped when the scope is closed or evicted. A scan is a refresh-only
// plan whose file is never kept; a graph runs `graph` only. Neither writes state. Requests
// carry no path, profile, workspace, executable or flag.

// noScope answers every lookup of a context that has no execution scope yet as not found,
// exactly like an unknown ID in a scope.
type noScope struct{}

func (noScope) DriftScan(string) (operations.DriftScanView, bool) {
	return operations.DriftScanView{}, false
}
func (noScope) DriftDetail(string, string) (drift.Detail, error) {
	return drift.Detail{}, operations.ErrScanNotFound
}
func (noScope) Graph(string, string) (operations.GraphView, error) {
	return operations.GraphView{}, operations.ErrGraphNotFound
}
func (noScope) GraphImpact(string, string, string) (operations.GraphImpact, error) {
	return operations.GraphImpact{}, operations.ErrGraphNotFound
}

// noScope is also an execSource with no plans, operations or discovery results. Requests
// that start work create a scope first, so its start methods are never reached.
func (noScope) Plans() []*operations.Plan                         { return nil }
func (noScope) Plan(string) (*operations.Plan, bool)              { return nil, false }
func (noScope) Snapshot() workspace.Snapshot                      { return workspace.Snapshot{} }
func (noScope) Operations() []operations.Operation                { return nil }
func (noScope) Holder() string                                    { return "" }
func (noScope) View() *projects.ScopeView                         { return nil }
func (noScope) SourceStatus(*operations.Plan) gitctx.SourceStatus { return gitctx.SourceStatus{} }
func (noScope) ContextStatus(*operations.Plan) (bool, operations.ApplyStatus) {
	return false, operations.ApplyStatus{}
}
func (noScope) Operation(string) (operations.Operation, *operations.Log, bool) {
	return operations.Operation{}, nil, false
}
func (noScope) Cancel(string) (operations.Operation, error) {
	return operations.Operation{}, operations.ErrNotFound
}
func (noScope) StartPlan(context.Context, operations.PlanStart) (operations.Operation, error) {
	return operations.Operation{}, operations.ErrNotFound
}
func (noScope) Preflight(context.Context, string, operations.PreflightRequest) (operations.PreflightReport, error) {
	return operations.PreflightReport{}, operations.ErrNotFound
}
func (noScope) Approve(context.Context, string, operations.ApprovalRequest) (operations.Approval, error) {
	return operations.Approval{}, operations.ErrNotFound
}
func (noScope) LocateChange(*operations.Plan, string) (gitctx.SourceReference, error) {
	return gitctx.SourceReference{}, operations.ErrNotFound
}
func (noScope) DriftScans() []operations.DriftScanView { return nil }
func (noScope) Graphs() []operations.GraphView         { return nil }
func (noScope) StartDriftScan(context.Context, operations.DriftStart) (operations.Operation, operations.DriftScan, error) {
	return operations.Operation{}, operations.DriftScan{}, operations.ErrNotFound
}
func (noScope) StartGraph(context.Context, operations.GraphStart) (operations.Operation, operations.GraphRun, error) {
	return operations.Operation{}, operations.GraphRun{}, operations.ErrNotFound
}
func (noScope) Discard(context.Context, string) error { return operations.ErrNotFound }
func (noScope) StartApply(context.Context, string, string, string) (operations.Operation, error) {
	return operations.Operation{}, operations.ErrNotFound
}
func (noScope) PreviewImport(context.Context, imports.Request) (imports.Preview, error) {
	return imports.Preview{}, operations.ErrNotFound
}
func (noScope) StartImport(context.Context, operations.ImportRequest) (operations.Operation, error) {
	return operations.Operation{}, operations.ErrNotFound
}
func (noScope) DiscoveryPin(context.Context, bool, string, string) (discovery.Pin, []string, error) {
	return discovery.Pin{}, nil, discovery.ErrNotFound
}

// analysisOptions are the parts of the drift and graph options of a Hub workspace that
// describe the scope. Code and Reason explain why the scope itself is unavailable.
type analysisOptions struct {
	Code               string                       `json:"code,omitempty"`
	Reason             string                       `json:"reason,omitempty"`
	Engine             string                       `json:"engine"`
	Version            string                       `json:"version"`
	Workspace          string                       `json:"workspace"`
	Backend            string                       `json:"backend"`
	Execution          string                       `json:"executionMode"`
	Scope              *projects.ScopeView          `json:"scope"`
	ExecutionContext   *operations.ExecutionContext `json:"executionContext"`
	ContextFingerprint string                       `json:"contextFingerprint"`
}

// scopedOptions resolves the execution scope of an options request for display; it runs no
// engine command and never creates or evicts a scope (Hub.Preview). A workspace whose scope is unsupported gets options with its reason and a
// nil scope; any other failure is answered here and returns ok false.
func scopedOptions(w http.ResponseWriter, r *http.Request, hub *projects.Hub) (*projects.Scope, analysisOptions, bool) {
	s, err := hub.Preview(r.PathValue("cid"))
	var unsupported *projects.ScopeError
	if errors.As(err, &unsupported) && unsupported.Code == projects.CodeExecutionUnsupported {
		return nil, analysisOptions{Code: unsupported.Code, Reason: unsupported.Message}, true
	}
	if err != nil {
		scopeError(w, r, hub, err)
		return nil, analysisOptions{}, false
	}
	view := s.View()
	return s, analysisOptions{Engine: view.Engine, Version: view.Version, Workspace: view.Workspace, Backend: view.Backend, Execution: view.ExecutionMode, Scope: &view, ExecutionContext: s.Execution(), ContextFingerprint: s.ProfileFingerprint()}, true
}

// scopedStartError maps a refused scoped drift scan, graph or discovery request. Nothing ran.
func scopedStartError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, s *projects.Scope, err error) {
	if !mapOpError(w, r, hub, s.Holder(), err) {
		projectError(w, hub, err)
	}
}

func scopedAnalysisRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans/options", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		s, base, ok := scopedOptions(w, r, hub)
		if !ok {
			return
		}
		out := scopedDriftOptions{analysisOptions: base, VarFiles: []string{}}
		if s != nil {
			out.CanScan, out.Reason = s.DriftCapability()
			out.VarFiles = s.VarFiles()
		}
		writeJSON(w, 200, out)
	}))

	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs/options", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		s, base, ok := scopedOptions(w, r, hub)
		if !ok {
			return
		}
		out := scopedGraphOptions{analysisOptions: base, Command: []string{}}
		if s != nil {
			out.CanGenerate, out.Reason = s.GraphCapability()
			out.Command = append([]string{base.Engine}, engine.GraphArgs()...)
		}
		writeJSON(w, 200, out)
	}))
	analysisExecRoutes(mux, "/api/v1/contexts/{cid}", hub, hubSourceOf(hub))
}

type scopedDriftOptions struct {
	CanScan bool `json:"canScan"`
	analysisOptions
	VarFiles []string `json:"varFiles"`
}

type scopedGraphOptions struct {
	CanGenerate bool `json:"canGenerate"`
	analysisOptions
	Command []string `json:"command"`
}
