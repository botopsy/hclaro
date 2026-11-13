package api

import (
	"context"
	"errors"
	"net/http"

	"hclaro/internal/drift"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
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
	if !scopedContext(w, r, hub) {
		return nil, analysisOptions{}, false
	}
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

// scopedStartError maps a refused scoped drift scan or graph start. Nothing ran.
func scopedStartError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, s *projects.Scope, err error) {
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
	case errors.Is(err, operations.ErrVarFiles):
		reasonJSON(w, 400, "invalid_var_files", "Choose variable files from the scan options of this workspace.")
	default:
		scopeError(w, r, hub, err)
	}
}

func scopedAnalysisRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans/options", func(w http.ResponseWriter, r *http.Request) {
		s, base, ok := scopedOptions(w, r, hub)
		if !ok {
			return
		}
		out := struct {
			CanScan bool `json:"canScan"`
			analysisOptions
			VarFiles []string `json:"varFiles"`
		}{analysisOptions: base, VarFiles: []string{}}
		if s != nil {
			out.CanScan, out.Reason = s.DriftCapability()
			out.VarFiles = s.VarFiles()
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/drift-scans", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
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
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		op, scan, err := s.StartDriftScan(ctx, body.VarFiles, body.ContextFingerprint)
		if err != nil {
			scopedStartError(w, r, hub, s, err)
			return
		}
		writeJSON(w, 202, map[string]any{"operation": scopedOperation{op, s.View()}, "scan": scan})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans", func(w http.ResponseWriter, r *http.Request) {
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		scans := []operations.DriftScanView{}
		if s != nil {
			scans = s.DriftScans()
		}
		writeJSON(w, 200, map[string]any{"scans": scans})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveDriftScan(w, r, s)
		} else if ok {
			serveDriftScan(w, r, noScope{})
		}
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/drift-scans/{id}/resources/{change}", func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveDriftDetail(w, r, s)
		} else if ok {
			serveDriftDetail(w, r, noScope{})
		}
	})

	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs/options", func(w http.ResponseWriter, r *http.Request) {
		s, base, ok := scopedOptions(w, r, hub)
		if !ok {
			return
		}
		out := struct {
			CanGenerate bool `json:"canGenerate"`
			analysisOptions
			Command []string `json:"command"`
		}{analysisOptions: base, Command: []string{}}
		if s != nil {
			out.CanGenerate, out.Reason = s.GraphCapability()
			out.Command = append([]string{base.Engine}, engine.GraphArgs()...)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/graphs", func(w http.ResponseWriter, r *http.Request) {
		if !hub.ScopedExecution {
			projectError(w, hub, projects.ErrScopedDisabled)
			return
		}
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
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		op, run, err := s.StartGraph(ctx, body.ContextFingerprint)
		if err != nil {
			scopedStartError(w, r, hub, s, err)
			return
		}
		writeJSON(w, 202, map[string]any{"operation": scopedOperation{op, s.View()}, "graph": run})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs", func(w http.ResponseWriter, r *http.Request) {
		s, ok := resolveScope(w, r, hub, false)
		if !ok {
			return
		}
		graphs := []operations.GraphView{}
		if s != nil {
			graphs = s.Graphs()
		}
		writeJSON(w, 200, map[string]any{"graphs": graphs})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveGraph(w, r, s)
		} else if ok {
			serveGraph(w, r, noScope{})
		}
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/graphs/{id}/nodes/{node}", func(w http.ResponseWriter, r *http.Request) {
		if s, ok := resolveScope(w, r, hub, false); ok && s != nil {
			serveGraphImpact(w, r, s)
		} else if ok {
			serveGraphImpact(w, r, noScope{})
		}
	})
}
