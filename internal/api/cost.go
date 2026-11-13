package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"hclaro/internal/cost"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// optInTimeout bounds the version check run when estimation is turned on.
const optInTimeout = 30 * time.Second

// costStale reports whether an estimate no longer describes the current plan context.
func costStale(manager *operations.Manager, store *workspace.Store, b cost.Binding) (bool, string) {
	p, ok := manager.Plan(b.PlanID)
	if !ok {
		return true, "This plan is no longer available. Run a new plan to estimate it."
	}
	snap := store.Get()
	if p.Fingerprint != b.Fingerprint || p.ArtifactSHA256 != b.ArtifactSHA256 || snap.Info.Workspace != b.Workspace || snap.Info.Engine != b.Engine {
		return true, "The estimate was made for another plan or context."
	}
	if changed, _ := manager.ContextStatus(p); changed {
		return true, "The project context changed since this plan was made (files, variables, workspace, engine or profile). The estimate no longer applies. Run a new plan and estimate it again."
	}
	return false, ""
}

// costRoutes serve cost estimates of the launch project's reviewed plans. Nothing runs and
// nothing is sent until the user opted in after the disclosure; GET routes never start a
// process. Estimates are separate from plans and applies: their failure or cancellation
// never changes a plan or blocks an apply.
func costRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager) {
	available := func(w http.ResponseWriter) (*cost.Service, bool) {
		switch {
		case manager.Cost == nil:
			reasonJSON(w, 409, "cost_unavailable", "Cost estimation is not available in this session.")
		case manager.Root() == "":
			reasonJSON(w, 409, "no_launch_project", "Cost estimates are made for plans of the project Hclaro was opened with from the terminal.")
		default:
			return manager.Cost, true
		}
		return nil, false
	}
	view := func(e cost.CostEstimate, b cost.Binding) cost.CostEstimate {
		e.Stale, e.StaleReason = costStale(manager, store, b)
		return e
	}
	mux.HandleFunc("GET /api/v1/cost/provider", func(w http.ResponseWriter, r *http.Request) {
		if manager.Cost == nil {
			writeJSON(w, 200, map[string]any{"provider": cost.ProviderInfracost, "state": "unavailable", "reason": "Cost estimation is not available in this session.", "installed": false, "enabled": false, "canEstimate": false, "strictLocal": false, "offlineAvailable": false})
			return
		}
		caps := manager.Cost.Capabilities()
		if manager.Root() == "" {
			caps.CanEstimate, caps.Reason = false, "Cost estimates are made for plans of the project Hclaro was opened with from the terminal."
		}
		writeJSON(w, 200, caps)
	})
	mux.HandleFunc("POST /api/v1/cost/opt-in", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Acknowledged      *bool  `json:"acknowledged"`
			DisclosureVersion string `json:"disclosureVersion"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledged == nil || !*body.Acknowledged || len(body.DisclosureVersion) > 16 {
			errorJSON(w, 400, "acknowledgement_required")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		ctx, cancel := withDeadline(w, r, optInTimeout)
		defer cancel()
		caps, err := svc.OptIn(ctx, body.DisclosureVersion)
		if err != nil {
			costError(w, err)
			return
		}
		writeJSON(w, 200, caps)
	})
	mux.HandleFunc("POST /api/v1/cost/opt-out", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if manager.Cost == nil {
			reasonJSON(w, 409, "cost_unavailable", "Cost estimation is not available in this session.")
			return
		}
		writeJSON(w, 200, manager.Cost.OptOut())
	})
	mux.HandleFunc("POST /api/v1/plans/{id}/cost-estimates", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		p, ok := lookupPlan(r, manager)
		if !ok {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		if changed, _ := manager.ContextStatus(p); changed {
			costError(w, &cost.ProviderError{Kind: cost.KindContextChanged})
			return
		}
		input, err := manager.CostInput(p.ID)
		switch {
		case errors.Is(err, cost.ErrInputTooLarge):
			costError(w, &cost.ProviderError{Kind: cost.KindInputTooLarge})
			return
		case err != nil || len(input) == 0:
			costError(w, &cost.ProviderError{Kind: cost.KindNoInput})
			return
		}
		b := cost.Binding{PlanID: p.ID, ArtifactSHA256: p.ArtifactSHA256, Fingerprint: p.Fingerprint, Engine: p.Engine, Workspace: p.Workspace}
		if p.Context != nil {
			b.ProfileID = p.Context.ProfileID
		}
		est, err := svc.Start(cost.Input{
			Binding: b, Document: input, Changes: cost.ChangesOf(p.Review),
			ProjectKey: cost.ProjectKey(manager.Root()), ProjectName: filepath.Base(manager.Root()),
		})
		if err != nil {
			costError(w, err)
			return
		}
		writeJSON(w, 202, view(est, b))
	})
	mux.HandleFunc("GET /api/v1/plans/{id}/cost-estimate", func(w http.ResponseWriter, r *http.Request) {
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		est, b, err := svc.Latest(r.PathValue("id"))
		if err != nil {
			reasonJSON(w, 404, "estimate_not_found", "This plan has not been estimated in this session.")
			return
		}
		writeJSON(w, 200, view(est, b))
	})
	mux.HandleFunc("GET /api/v1/cost-estimates/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "estimate_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		est, b, err := svc.Get(r.PathValue("id"))
		if err != nil {
			errorJSON(w, 404, "estimate_not_found")
			return
		}
		writeJSON(w, 200, view(est, b))
	})
	mux.HandleFunc("POST /api/v1/cost-estimates/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if !idPattern.MatchString(r.PathValue("id")) {
			errorJSON(w, 404, "estimate_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		est, err := svc.Cancel(r.PathValue("id"))
		if err != nil {
			errorJSON(w, 404, "estimate_not_found")
			return
		}
		_, b, _ := svc.Get(est.ID)
		writeJSON(w, 200, view(est, b))
	})
	mux.HandleFunc("GET /api/v1/cost/history", func(w http.ResponseWriter, r *http.Request) {
		svc, ok := available(w)
		if !ok {
			return
		}
		records := []cost.AggregateRecord{}
		if svc.Store != nil {
			records = svc.Store.List(cost.ProjectKey(manager.Root()))
		}
		writeJSON(w, 200, map[string]any{"records": records, "persisted": svc.Store != nil})
	})
	mux.HandleFunc("POST /api/v1/cost/history/clear", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		removed := 0
		if svc.Store != nil {
			n, err := svc.Store.Clear(cost.ProjectKey(manager.Root()))
			if err != nil {
				reasonJSON(w, 500, "history_write_failed", "The saved estimates could not be cleared.")
				return
			}
			removed = n
		}
		writeJSON(w, 200, map[string]int{"removed": removed})
	})
}

// costError maps cost errors to typed responses. Messages never include CLI output.
func costError(w http.ResponseWriter, err error) {
	kind := cost.KindOf(err)
	status := 502
	switch kind {
	case cost.KindStrictLocal, cost.KindOptIn, cost.KindMissingCLI, cost.KindUnsupported, cost.KindContextChanged, cost.KindBusy, cost.KindNoInput, cost.KindInputTooLarge, cost.KindAuth:
		status = 409
	case cost.KindShutdown:
		status = 503
	}
	reasonJSON(w, status, kind, cost.Message(kind))
}
