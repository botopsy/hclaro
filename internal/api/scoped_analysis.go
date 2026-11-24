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
	mux.HandleFunc("POST /api/v1/contexts/{cid}/drift-scans", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// The preflight must have been shown: a scan queries providers and the backend.
			Acknowledge *bool    `json:"acknowledgeProviderAccess"`
			VarFiles    []string `json:"varFiles"`
			// ContextFingerprint is the one the scan options returned.
			ContextFingerprint string `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "A drift scan may contact providers and the backend and take the state lock. Confirm provider access first.")
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
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		op, scan, err := s.StartDriftScan(ctx, body.VarFiles, body.ContextFingerprint)
		if err != nil {
			scopedStartError(w, r, hub, s, err)
			return
		}
		writeJSON(w, 202, map[string]any{"operation": scopedOperation{op, hubSource{s}.View()}, "scan": scan})
	}))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		scans := []operations.DriftScanView{}
		if s != nil {
			scans = s.DriftScans()
		}
		writeJSON(w, 200, map[string]any{"scans": scans})
	}))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans/{id}", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveDriftScan(w, r, s)
		} else if ok {
			serveDriftScan(w, r, noScope{})
		}
	}))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans/{id}/resources/{change}", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveDriftDetail(w, r, s)
		} else if ok {
			serveDriftDetail(w, r, noScope{})
		}
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
	mux.HandleFunc("POST /api/v1/contexts/{cid}/graphs", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			// The preflight must have been shown: `graph` runs the engine and may read the backend.
			Acknowledge        *bool  `json:"acknowledgeEngineRun"`
			ContextFingerprint string `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "Generating a graph runs the engine, which may read the backend. Confirm first.")
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
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		op, run, err := s.StartGraph(ctx, body.ContextFingerprint)
		if err != nil {
			scopedStartError(w, r, hub, s, err)
			return
		}
		writeJSON(w, 202, map[string]any{"operation": scopedOperation{op, hubSource{s}.View()}, "graph": run})
	}))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		graphs := []operations.GraphView{}
		if s != nil {
			graphs = s.Graphs()
		}
		writeJSON(w, 200, map[string]any{"graphs": graphs})
	}))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs/{id}", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveGraph(w, r, s)
		} else if ok {
			serveGraph(w, r, noScope{})
		}
	}))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs/{id}/nodes/{node}", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveGraphImpact(w, r, s)
		} else if ok {
			serveGraphImpact(w, r, noScope{})
		}
	}))
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
