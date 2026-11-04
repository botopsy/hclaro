package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"hclaro/internal/operations"
	"hclaro/internal/plan"
	"hclaro/internal/workspace"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type planSummary struct {
	ID          string         `json:"id"`
	OperationID string         `json:"operationId"`
	CreatedAt   string         `json:"createdAt"`
	Engine      string         `json:"engine"`
	Workspace   string         `json:"workspace"`
	VarFiles    []string       `json:"varFiles"`
	Summary     map[string]int `json:"summary"`
	Errored     bool           `json:"errored"`
	Changes     int            `json:"changes"`
	Outputs     int            `json:"outputs"`
	Drift       int            `json:"drift"`
}

func summarize(p *operations.Plan) planSummary {
	changes := 0
	for action, n := range p.Summary {
		if action != plan.NoOp {
			changes += n
		}
	}
	outputs := 0
	for _, o := range p.Outputs {
		if o.Action != plan.NoOp {
			outputs++
		}
	}
	return planSummary{p.ID, p.OperationID, p.CreatedAt, p.Engine, p.Workspace, p.VarFiles, p.Summary, p.Errored, changes, outputs, len(p.Drift)}
}

func planRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, csrf, version string) {
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"csrfToken": csrf, "version": version})
	})
	mux.HandleFunc("GET /api/v1/plans/options", func(w http.ResponseWriter, r *http.Request) {
		snap := store.Get()
		writeJSON(w, 200, struct {
			CanPlan   bool     `json:"canPlan"`
			Reason    string   `json:"reason,omitempty"`
			Engine    string   `json:"engine"`
			Version   string   `json:"version"`
			Workspace string   `json:"workspace"`
			Backend   string   `json:"backend"`
			VarFiles  []string `json:"varFiles"`
		}{snap.Info.Capabilities.CanPlan, snap.Info.Capabilities.PlanReason, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, snap.Info.Backend, manager.VarFiles()})
	})
	mux.HandleFunc("POST /api/v1/plans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Acknowledge *bool    `json:"acknowledgeBackendAccess"`
			VarFiles    []string `json:"varFiles"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.More() {
			errorJSON(w, 400, "invalid_request")
			return
		}
		// The preflight must have been shown: planning contacts providers and the backend.
		if body.Acknowledge == nil || !*body.Acknowledge {
			errorJSON(w, 400, "acknowledgement_required")
			return
		}
		op, err := manager.StartPlan(body.VarFiles)
		switch {
		case errors.Is(err, operations.ErrBusy):
			errorJSON(w, 409, "operation_running")
		case errors.Is(err, operations.ErrLoading):
			errorJSON(w, 409, "workspace_loading")
		case errors.Is(err, operations.ErrUnavailable):
			errorJSON(w, 409, "planning_unavailable")
		case errors.Is(err, operations.ErrVarFiles):
			errorJSON(w, 400, "invalid_var_files")
		case errors.Is(err, operations.ErrClosed):
			errorJSON(w, 503, "shutting_down")
		case err != nil:
			errorJSON(w, 500, "plan_not_started")
		default:
			writeJSON(w, 202, op)
		}
	})
	mux.HandleFunc("GET /api/v1/plans", func(w http.ResponseWriter, r *http.Request) {
		plans := manager.Plans()
		out := make([]planSummary, 0, len(plans))
		for _, p := range plans {
			out = append(out, summarize(p))
		}
		writeJSON(w, 200, map[string]any{"plans": out})
	})
	mux.HandleFunc("GET /api/v1/plans/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := lookupPlan(r, manager)
		if !ok {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		snap := store.Get()
		current := manager.Fingerprint(snap, p.VarFiles)
		writeJSON(w, 200, struct {
			*operations.Plan
			EngineVersionLabel string `json:"currentEngineVersion"`
			ContextChanged     bool   `json:"contextChanged"`
		}{p, snap.Info.Version, current == "" || current != p.Fingerprint})
	})
	mux.HandleFunc("GET /api/v1/plans/{id}/changes/{change}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := lookupPlan(r, manager)
		change := r.PathValue("change")
		detail, found := p.Details[change]
		if !ok || !idPattern.MatchString(change) || !found {
			errorJSON(w, 404, "change_not_found")
			return
		}
		writeJSON(w, 200, detail)
	})
	mux.HandleFunc("GET /api/v1/operations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"operations": manager.Operations()})
	})
	mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, _, ok := lookupOperation(r, manager)
		if !ok {
			errorJSON(w, 404, "operation_not_found")
			return
		}
		writeJSON(w, 200, op)
	})
	mux.HandleFunc("POST /api/v1/operations/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.More() {
			errorJSON(w, 400, "invalid_request")
			return
		}
		op, err := manager.Cancel(r.PathValue("id"))
		switch {
		case errors.Is(err, operations.ErrNotFound):
			errorJSON(w, 404, "operation_not_found")
		case errors.Is(err, operations.ErrNotRunning):
			errorJSON(w, 409, "operation_not_running")
		default:
			writeJSON(w, 202, op)
		}
	})
	mux.HandleFunc("GET /api/v1/operations/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		_, log, ok := lookupOperation(r, manager)
		if !ok {
			errorJSON(w, 404, "operation_not_found")
			return
		}
		if log == nil {
			errorJSON(w, 410, "log_not_retained")
			return
		}
		after := int64(0)
		cursor := r.URL.Query().Get("after")
		if cursor == "" {
			cursor = r.Header.Get("Last-Event-ID")
		}
		if cursor != "" {
			n, err := strconv.ParseInt(cursor, 10, 64)
			if err != nil || n < 0 {
				errorJSON(w, 400, "invalid_cursor")
				return
			}
			after = n
		}
		stream(w, r, manager, r.PathValue("id"), log, after)
	})
}

func lookupPlan(r *http.Request, manager *operations.Manager) (*operations.Plan, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		return &operations.Plan{}, false
	}
	p, ok := manager.Plan(id)
	if !ok {
		return &operations.Plan{}, false
	}
	return p, true
}

func lookupOperation(r *http.Request, manager *operations.Manager) (operations.Operation, *operations.Log, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		return operations.Operation{}, nil, false
	}
	return manager.Operation(id)
}

// stream sends retained log events as SSE, then status updates until the operation ends.
// Clients resume with ?after=<seq> or Last-Event-ID; replay is bounded by the log ring.
func stream(w http.ResponseWriter, r *http.Request, manager *operations.Manager, id string, log *operations.Log, after int64) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(event, id string, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if id != "" {
			if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	status := func() bool {
		op, _, _ := manager.Operation(id)
		return send("status", "", op)
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	first := true
	for {
		events, dropped, done, wait := log.Since(after)
		if first && dropped {
			if !send("truncated", "", map[string]string{"message": "Older log lines were dropped to bound memory use."}) {
				return
			}
		}
		first = false
		for _, e := range events {
			if !send("log", strconv.FormatInt(e.Seq, 10), e) {
				return
			}
			after = e.Seq
		}
		if !status() {
			return
		}
		if done {
			send("end", "", map[string]bool{"done": true})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wait:
		case <-heartbeat.C:
		}
	}
}
