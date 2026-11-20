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
		writeJSON(w, 200, planOptions{caps.CanPlan, caps.PlanReason, caps.CanApply, caps.ApplyReason, snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, snap.Info.Backend, manager.VarFiles(), projectID, context, profileError})
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
	execRoutes(mux, "/api/v1", nil, launchSourceOf(manager, inits))
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

// lookupPlan finds a plan of src by the request's {id}; any other ID is not found.
func lookupPlan(r *http.Request, src interface {
	Plan(string) (*operations.Plan, bool)
}) (*operations.Plan, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		return nil, false
	}
	return src.Plan(id)
}

// scopedOperation is an operation with the Hub scope it ran in (omitted for the launch project).
type scopedOperation struct {
	operations.Operation
	Scope *projects.ScopeView `json:"scope,omitempty"`
}

// applyTimeout bounds approval and apply requests. A Hub scope revalidates first (engine
// version and workspace list, up to inspectTimeout) and a launch apply under an AWS account
// policy asks STS again (up to 20 s); one bound covers both.
const applyTimeout = inspectTimeout

// execRoutes registers the plan, apply and operation routes under prefix: "/api/v1" for the
// launch project (hub nil) and "/api/v1/contexts/{cid}" for Hub workspaces. Hub routes check
// the scoped execution switch and the {cid} context before reading the body. Requests carry
// no path, executable, argv, flag, environment value, -target or plan file.
func execRoutes(mux *http.ServeMux, prefix string, hub *projects.Hub, source sourceOf) {
	handle := routesUnder(mux, prefix, hub)
	handle("POST /plans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Acknowledge *bool    `json:"acknowledgeBackendAccess"`
			VarFiles    []string `json:"varFiles"`
			// Replace holds opaque resource IDs from the snapshot, never addresses.
			Replace []string `json:"replace"`
			// ProfileID binds a launch plan to an execution profile; ContextFingerprint is the
			// fingerprint of the context the user reviewed.
			ProfileID          *string `json:"profileId"`
			ContextFingerprint string  `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		// The preflight must have been shown: planning contacts providers and the backend.
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "Planning may contact providers and the backend and take the state lock. Confirm backend access first.")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		if len(body.ContextFingerprint) > 128 {
			errorJSON(w, 400, "invalid_request")
			return
		}
		src, ok := source(w, r, true)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		op, err := src.StartPlan(ctx, operations.PlanStart{VarFiles: body.VarFiles, Replace: body.Replace, ProfileID: profileID, ContextFingerprint: body.ContextFingerprint})
		switch {
		case err == nil:
			writeJSON(w, 202, scopedOperation{op, src.View()})
		case mapOpError(w, r, hub, src.Holder(), err):
		case errors.Is(err, operations.ErrUnavailable):
			reasonJSON(w, 409, "planning_unavailable", "Planning is not available for this workspace.")
		case errors.Is(err, operations.ErrReplace):
			reasonJSON(w, 400, "invalid_replace", "Choose managed resources of this workspace to replace.")
		default:
			errorJSON(w, 500, "plan_not_started")
		}
	})
	handle("GET /plans", func(w http.ResponseWriter, r *http.Request) {
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		out := []planSummary{}
		for _, p := range src.Plans() {
			out = append(out, summarize(p))
		}
		writeJSON(w, 200, map[string]any{"plans": out, "scope": src.View()})
	})
	handle("GET /plans/{id}", func(w http.ResponseWriter, r *http.Request) {
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		p, ok := lookupPlan(r, src)
		if !ok {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		changed, status := src.ContextStatus(p)
		writeJSON(w, 200, planResponse{p, src.Snapshot().Info.Version, changed, status, src.SourceStatus(p), src.View()})
	})
	// Preflight evaluates every apply check on the server. verifyIdentity runs the read-only
	// AWS STS caller identity probe, which contacts AWS, and is false unless the user asks.
	// A Hub scope is revalidated first by each of preflight, approval and apply; the apply
	// revalidates it again, fully, with the root claim held.
	handle("POST /plans/{id}/preflight", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			VerifyIdentity bool `json:"verifyIdentity"`
		}
		src, ok := planRequest(w, r, source, &body, func() bool { return true })
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, preflightTimeout)
		defer cancel()
		report, err := src.Preflight(ctx, r.PathValue("id"), operations.PreflightRequest{VerifyIdentity: body.VerifyIdentity})
		if err != nil {
			applyError(w, r, hub, src, err)
			return
		}
		writeJSON(w, 200, preflightResponse{report, src.View()})
	})
	handle("POST /plans/{id}/approval", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Binding      string   `json:"preflightBinding"`
			Acknowledged []string `json:"acknowledged"`
		}
		src, ok := planRequest(w, r, source, &body, func() bool {
			if len(body.Binding) > 128 || len(body.Acknowledged) > 32 {
				return false
			}
			for _, a := range body.Acknowledged {
				if len(a) > 64 {
					return false
				}
			}
			return true
		})
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, applyTimeout)
		defer cancel()
		approval, err := src.Approve(ctx, r.PathValue("id"), operations.ApprovalRequest{Binding: body.Binding, Acknowledged: body.Acknowledged})
		if err != nil {
			applyError(w, r, hub, src, err)
			return
		}
		writeJSON(w, 200, approvalResponse{approval, src.View()})
	})
	handle("POST /plans/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token        string `json:"confirmationToken"`
			Confirmation string `json:"confirmation"`
		}
		src, ok := planRequest(w, r, source, &body, func() bool { return len(body.Token) <= 128 && len(body.Confirmation) <= 256 })
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, applyTimeout)
		defer cancel()
		op, err := src.StartApply(ctx, r.PathValue("id"), body.Token, body.Confirmation)
		if err != nil {
			applyError(w, r, hub, src, err)
			return
		}
		writeJSON(w, 202, scopedOperation{op, src.View()})
	})
	handle("GET /plans/{id}/changes/{change}", func(w http.ResponseWriter, r *http.Request) {
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		p, ok := lookupPlan(r, src)
		if !ok {
			errorJSON(w, 404, "plan_not_found")
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
	handle("GET /operations", func(w http.ResponseWriter, r *http.Request) {
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		ops := src.Operations()
		if ops == nil {
			ops = []operations.Operation{}
		}
		writeJSON(w, 200, map[string]any{"operations": ops, "scope": src.View()})
	})
	handle("GET /operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		src, op, _, ok := lookupOperation(w, r, source)
		if !ok {
			return
		}
		writeJSON(w, 200, scopedOperation{op, src.View()})
	})
	handle("POST /operations/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		src, ok := source(w, r, false)
		if !ok {
			return
		}
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "operation_not_found")
			return
		}
		op, err := src.Cancel(r.PathValue("id"))
		switch {
		case errors.Is(err, operations.ErrNotFound):
			errorJSON(w, 404, "operation_not_found")
		case errors.Is(err, operations.ErrNotRunning):
			errorJSON(w, 409, "operation_not_running")
		default:
			writeJSON(w, 202, scopedOperation{op, src.View()})
		}
	})
	handle("GET /operations/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		src, _, log, ok := lookupOperation(w, r, source)
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
		id := r.PathValue("id")
		stream(w, r, func() operations.Operation {
			op, _, _ := src.Operation(id)
			return op
		}, log, after)
	})
}

// planRequest reads the body of a preflight, approval or apply request and resolves its
// source without creating a Hub scope: a context with no scope has no plans. A plan ID of
// another source answers plan_not_found when the request runs.
// bounded applies the body's length limits, the same for launch and Hub requests.
func planRequest(w http.ResponseWriter, r *http.Request, source sourceOf, body any, bounded func() bool) (execSource, bool) {
	if !decodeBody(w, r, body) {
		return nil, false
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		errorJSON(w, 404, "plan_not_found")
		return nil, false
	}
	if !bounded() {
		errorJSON(w, 400, "invalid_request")
		return nil, false
	}
	return source(w, r, false)
}

// applyError maps a refused preflight, approval or apply. Nothing was applied.
func applyError(w http.ResponseWriter, r *http.Request, hub *projects.Hub, src execSource, err error) {
	switch {
	case errors.Is(err, operations.ErrNotFound):
		errorJSON(w, 404, "plan_not_found")
	case mapOpError(w, r, hub, src.Holder(), err):
	default:
		errorJSON(w, 500, "apply_not_started")
	}
}

// lookupOperation finds an operation of the request's source; anything else is
// operation_not_found.
func lookupOperation(w http.ResponseWriter, r *http.Request, source sourceOf) (execSource, operations.Operation, *operations.Log, bool) {
	src, ok := source(w, r, false)
	if !ok {
		return nil, operations.Operation{}, nil, false
	}
	id := r.PathValue("id")
	op, log, found := operations.Operation{}, (*operations.Log)(nil), false
	if idPattern.MatchString(id) {
		op, log, found = src.Operation(id)
	}
	if !found {
		errorJSON(w, 404, "operation_not_found")
		return nil, operations.Operation{}, nil, false
	}
	return src, op, log, true
}

type planOptions struct {
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
}

type planResponse struct {
	*operations.Plan
	EngineVersionLabel string                 `json:"currentEngineVersion"`
	ContextChanged     bool                   `json:"contextChanged"`
	Apply              operations.ApplyStatus `json:"apply"`
	SourceStatus       gitctx.SourceStatus    `json:"sourceStatus"`
	Scope              *projects.ScopeView    `json:"scope,omitempty"`
}

type preflightResponse struct {
	operations.PreflightReport
	Scope *projects.ScopeView `json:"scope,omitempty"`
}

type approvalResponse struct {
	operations.Approval
	Scope *projects.ScopeView `json:"scope,omitempty"`
}
