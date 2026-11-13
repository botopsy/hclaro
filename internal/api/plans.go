package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"hclaro/internal/gitctx"
	"hclaro/internal/operations"
	"hclaro/internal/plan"
	"hclaro/internal/profiles"
	"hclaro/internal/projects"
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
	Kind        string         `json:"kind"`
	Replace     []string       `json:"replace"`
	Summary     map[string]int `json:"summary"`
	Errored     bool           `json:"errored"`
	Changes     int            `json:"changes"`
	Outputs     int            `json:"outputs"`
	Drift       int            `json:"drift"`
}

// preflightTimeout bounds the apply checks: the Git probe (10 s) and the identity probe
// (20 s) run concurrently inside it.
const preflightTimeout = 30 * time.Second

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
	return planSummary{p.ID, p.OperationID, p.CreatedAt, p.Engine, p.Workspace, p.VarFiles, p.Kind, p.Replace, p.Summary, p.Errored, changes, outputs, len(p.Drift)}
}

// launchContext is the execution context summary for the launch project and a profile. The
// workspace is the one the launch snapshot was read from; a profile that prefers another
// engine or selects another workspace gets a blocking issue. ok is false when the launch
// project is not registered in the Hub (profiles belong to registered projects).
func launchContext(hub *projects.Hub, snap workspace.Snapshot, profileID string) (summary executionSummary, ok bool, err error) {
	if hub == nil || hub.Profiles == nil {
		return executionSummary{}, false, nil
	}
	id := hub.LaunchID()
	if id == "" {
		return executionSummary{}, false, nil
	}
	resolved, p, err := hub.Resolve(id, profileID)
	if err != nil {
		return executionSummary{}, true, err
	}
	summary = contextSummary(hub, p, resolved, "")
	summary.Engine, summary.EngineFound, summary.EngineVersion = snap.Info.Engine, snap.Info.Engine != "", snap.Info.Version
	summary.Workspace, summary.WorkspaceSource = snap.Info.Workspace, "launch"
	issues := append([]profiles.Issue{}, resolved.Issues...)
	if resolved.Engine != "" && resolved.Engine != snap.Info.Engine {
		issues = append(issues, profiles.Issue{Code: "profile_engine", Message: "This profile prefers " + resolved.Engine + ", but this project was opened with " + snap.Info.Engine + ". Restart Hclaro with --engine " + resolved.Engine + ", or choose another profile."})
	}
	if resolved.Workspace != "" && resolved.Workspace != snap.Info.Workspace {
		issues = append(issues, profiles.Issue{Code: "profile_workspace", Message: "This profile selects workspace " + resolved.Workspace + ", but this project reads workspace " + snap.Info.Workspace + ". A Terraform workspace is a state namespace, not a cloud account or AWS profile."})
	}
	summary.Issues = issues
	return summary, true, nil
}

func planRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, inits *operations.Inits, hub *projects.Hub, csrf, version string) {
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		// Capabilities tell the UI which optional features the server enables; the server
		// still enforces each one on every request.
		writeJSON(w, 200, map[string]any{"csrfToken": csrf, "version": version, "capabilities": map[string]bool{"scopedExecution": hub != nil && hub.ScopedExecution}})
	})
	mux.HandleFunc("GET /api/v1/plans/options", func(w http.ResponseWriter, r *http.Request) {
		profileID := r.URL.Query().Get("profileId")
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		snap := store.Get()
		caps := snap.Info.Capabilities
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
			CanPlan     bool     `json:"canPlan"`
			Reason      string   `json:"reason,omitempty"`
			CanApply    bool     `json:"canApply"`
			ApplyReason string   `json:"applyReason,omitempty"`
			Engine      string   `json:"engine"`
			Version     string   `json:"version"`
			Workspace   string   `json:"workspace"`
			Backend     string   `json:"backend"`
			VarFiles    []string `json:"varFiles"`
			// ProjectID is the launch project's Hub ID; profiles exist only when it is set.
			ProjectID    string            `json:"projectId,omitempty"`
			Context      *executionSummary `json:"context,omitempty"`
			ProfileError string            `json:"profileError,omitempty"`
		}{caps.CanPlan, caps.PlanReason, caps.CanApply, caps.ApplyReason, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, snap.Info.Backend, manager.VarFiles(), projectID, context, profileError})
	})
	mux.HandleFunc("POST /api/v1/plans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Acknowledge *bool    `json:"acknowledgeBackendAccess"`
			VarFiles    []string `json:"varFiles"`
			// Replace holds opaque resource IDs from the snapshot, never addresses.
			Replace []string `json:"replace"`
			// ProfileID binds the plan to an execution profile; ContextFingerprint is the
			// fingerprint of the context summary the user reviewed.
			ProfileID          string `json:"profileId"`
			ContextFingerprint string `json:"contextFingerprint"`
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
		if !validProfileParam(body.ProfileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		op, err := manager.StartPlanWith(operations.PlanStart{VarFiles: body.VarFiles, Replace: body.Replace, ProfileID: body.ProfileID, ContextFingerprint: body.ContextFingerprint})
		var rejected *operations.RejectError
		switch {
		case errors.As(err, &rejected):
			writeJSON(w, 409, map[string]string{"error": rejected.Code, "reason": rejected.Reason})
		case errors.Is(err, operations.ErrBusy):
			errorJSON(w, 409, "operation_running")
		case errors.Is(err, operations.ErrLoading):
			errorJSON(w, 409, "workspace_loading")
		case errors.Is(err, operations.ErrUnavailable):
			errorJSON(w, 409, "planning_unavailable")
		case errors.Is(err, operations.ErrVarFiles):
			errorJSON(w, 400, "invalid_var_files")
		case errors.Is(err, operations.ErrReplace):
			errorJSON(w, 400, "invalid_replace")
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
		changed, status := manager.ContextStatus(p)
		writeJSON(w, 200, struct {
			*operations.Plan
			EngineVersionLabel string                 `json:"currentEngineVersion"`
			ContextChanged     bool                   `json:"contextChanged"`
			Apply              operations.ApplyStatus `json:"apply"`
			SourceStatus       gitctx.SourceStatus    `json:"sourceStatus"`
		}{p, snap.Info.Version, changed, status, manager.SourceStatus(p)})
	})
	// Preflight evaluates every apply check on the server. verifyIdentity runs the read-only
	// AWS STS caller identity probe, which contacts AWS, and is false unless the user asks.
	mux.HandleFunc("POST /api/v1/plans/{id}/preflight", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			VerifyIdentity bool `json:"verifyIdentity"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		ctx, cancel := withDeadline(w, r, preflightTimeout)
		defer cancel()
		report, err := manager.Preflight(ctx, r.PathValue("id"), operations.PreflightRequest{VerifyIdentity: body.VerifyIdentity})
		if err != nil {
			applyError(w, err)
			return
		}
		writeJSON(w, 200, report)
	})
	mux.HandleFunc("POST /api/v1/plans/{id}/approval", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Binding      string   `json:"preflightBinding"`
			Acknowledged []string `json:"acknowledged"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "plan_not_found")
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
		approval, err := manager.Approve(r.PathValue("id"), operations.ApprovalRequest{Binding: body.Binding, Acknowledged: body.Acknowledged})
		if err != nil {
			applyError(w, err)
			return
		}
		writeJSON(w, 200, approval)
	})
	mux.HandleFunc("POST /api/v1/plans/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token        string `json:"confirmationToken"`
			Confirmation string `json:"confirmation"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		op, err := manager.StartApply(r.PathValue("id"), body.Token, body.Confirmation)
		if err != nil {
			applyError(w, err)
			return
		}
		writeJSON(w, 202, op)
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
	// Reload inspects the launch project again and rereads its state, as at startup.
	mux.HandleFunc("POST /api/v1/workspace/reload", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		switch err := manager.Reread(); {
		case errors.Is(err, operations.ErrBusy):
			writeJSON(w, 409, map[string]string{"error": "operation_running", "reason": "An operation is running in this project. Read it again when it finishes."})
		case errors.Is(err, operations.ErrLoading):
			errorJSON(w, 409, "workspace_loading")
		case errors.Is(err, operations.ErrClosed):
			errorJSON(w, 503, "shutting_down")
		case err != nil:
			writeJSON(w, 409, map[string]string{"error": "reload_unavailable", "reason": "No project was opened from the terminal."})
		default:
			writeJSON(w, 202, map[string]string{"status": "loading"})
		}
	})
	mux.HandleFunc("GET /api/v1/operations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"operations": manager.Operations()})
	})
	mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, _, ok := lookupOperation(r, manager, inits)
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
		if errors.Is(err, operations.ErrNotFound) && inits != nil {
			op, err = inits.Cancel(r.PathValue("id"))
		}
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
		_, log, ok := lookupOperation(r, manager, inits)
		if !ok {
			errorJSON(w, 404, "operation_not_found")
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
		stream(w, r, func() operations.Operation {
			op, _, _ := lookupOperation(r, manager, inits)
			return op
		}, log, after)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, body any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(body) != nil || dec.More() {
		errorJSON(w, 400, "invalid_request")
		return false
	}
	return true
}

// applyError maps a refused approval or apply to a typed response. Nothing was run.
func applyError(w http.ResponseWriter, err error) {
	var rejected *operations.RejectError
	switch {
	case errors.Is(err, operations.ErrNotFound):
		errorJSON(w, 404, "plan_not_found")
	case errors.As(err, &rejected):
		writeJSON(w, 409, map[string]string{"error": rejected.Code, "reason": rejected.Reason})
	default:
		errorJSON(w, 500, "apply_not_started")
	}
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

// lookupOperation finds a launch-project operation or a Hub initialization by ID.
func lookupOperation(r *http.Request, manager *operations.Manager, inits *operations.Inits) (operations.Operation, *operations.Log, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		return operations.Operation{}, nil, false
	}
	if op, log, ok := manager.Operation(id); ok {
		return op, log, ok
	}
	return inits.Operation(id)
}

// stream sends retained log events as SSE, then status updates until the operation ends.
// Clients resume with ?after=<seq> or Last-Event-ID; replay is bounded by the log ring.
func stream(w http.ResponseWriter, r *http.Request, current func() operations.Operation, log *operations.Log, after int64) {
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
	status := func() bool { return send("status", "", current()) }
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
