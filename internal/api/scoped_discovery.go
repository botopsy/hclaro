package api

import (
	"context"
	"net/http"

	"hclaro/internal/discovery"
	"hclaro/internal/projects"
)

// Scoped AWS discovery routes for Hub workspaces (M24.09). They mirror the launch discovery
// routes, bound to one open read context: the AWS CLI runs with that scope's execution
// profile environment, and verifications, scans and prepared imports belong to that context
// only. They are dropped, and running scans cancelled, when the scope closes. GET routes run
// nothing and contact nothing; verifying, scanning and preparing are explicit POSTs that carry
// the reviewed context fingerprint and revalidate the scope first. Requests carry no profile.
func scopedDiscoveryRoutes(mux *http.ServeMux, hub *projects.Hub) {
	// pinOf resolves the discovery context of an open scope; check is set for requests that
	// contact AWS. It answers refusals itself.
	pinOf := func(w http.ResponseWriter, r *http.Request, check bool, reviewed string) (*discovery.Service, *projects.Scope, discovery.Pin, []string, bool) {
		if hub.Discovery == nil {
			if scopedContext(w, r, hub) {
				reasonJSON(w, 409, "discovery_unavailable", "Cloud discovery is not available in this session.")
			}
			return nil, nil, discovery.Pin{}, nil, false
		}
		if len(reviewed) > 128 {
			errorJSON(w, 400, "invalid_request")
			return nil, nil, discovery.Pin{}, nil, false
		}
		s, ok := resolveScope(w, r, hub, true)
		if !ok {
			return nil, nil, discovery.Pin{}, nil, false
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		pin, environ, err := scopeDiscoveryPin(ctx, s, check, reviewed)
		if err != nil {
			scopedStartError(w, r, hub, s, err)
			return nil, nil, discovery.Pin{}, nil, false
		}
		return hub.Discovery, s, pin, environ, true
	}
	mux.HandleFunc("GET /api/v1/contexts/{cid}/discovery/options", func(w http.ResponseWriter, r *http.Request) {
		s, base, ok := scopedOptions(w, r, hub)
		if !ok {
			return
		}
		out := struct {
			CanScan bool `json:"canScan"`
			analysisOptions
			Provider      string                   `json:"provider"`
			Types         []discovery.ResourceType `json:"types"`
			Regions       []string                 `json:"regions"`
			DefaultRegion string                   `json:"defaultRegion"`
			RegionSource  string                   `json:"regionSource"`
			Limits        map[string]int           `json:"limits"`
			Project       string                   `json:"project"`
			StateStatus   string                   `json:"stateStatus"`
			Identity      *discovery.CloudIdentity `json:"identity,omitempty"`
			ProjectID     string                   `json:"projectId,omitempty"`
		}{analysisOptions: base, Provider: "aws", Types: discovery.Types, Regions: discovery.Regions, DefaultRegion: "us-east-1", RegionSource: "default", Limits: map[string]int{
			"maxRegions": discovery.MaxRegions, "maxPerType": discovery.MaxPerTypeLimit, "defaultPerType": discovery.DefaultPerType,
			"maxConcurrency": discovery.MaxConcurrency, "defaultConcurrency": discovery.DefaultConcurrent, "pageSize": discovery.PageSize,
		}}
		switch {
		case s == nil:
		case hub.Discovery == nil:
			out.Reason = "Cloud discovery is not available in this session."
		default:
			view := s.View()
			snap := s.Snapshot()
			out.Project, out.ProjectID, out.StateStatus = view.Alias, view.ProjectID, snap.Status
			// Reading the pin runs nothing; the identity is only an earlier verification of
			// this context.
			pin, environ, err := scopeDiscoveryPin(r.Context(), s, false, "")
			if err != nil {
				out.Reason = "The execution context of this workspace changed. Open the workspace again."
				break
			}
			out.CanScan = true
			out.DefaultRegion, out.RegionSource = discovery.RegionOf(pin, environ)
			if id, ok := hub.Discovery.Identity(pin); ok {
				out.Identity = &id
			}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/discovery/identity", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ContextFingerprint string `json:"contextFingerprint"`
		}
		if !scopedGate(w, hub) || !decodeBody(w, r, &body) {
			return
		}
		svc, _, pin, environ, ok := pinOf(w, r, true, body.ContextFingerprint)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
		defer cancel()
		identity, err := svc.Verify(ctx, pin, environ)
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, identity)
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/discovery/scans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IdentityID         string                   `json:"identityId"`
			ContextFingerprint string                   `json:"contextFingerprint"`
			Scope              discovery.DiscoveryScope `json:"scope"`
			// The scan preview must have been shown: listing calls AWS APIs.
			Acknowledge *bool `json:"acknowledgeApiCalls"`
		}
		if !scopedGate(w, hub) || !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			errorJSON(w, 400, "acknowledgement_required")
			return
		}
		if !idPattern.MatchString(body.IdentityID) {
			reasonJSON(w, 409, "identity_required", "Verify the AWS context before scanning.")
			return
		}
		svc, _, pin, environ, ok := pinOf(w, r, true, body.ContextFingerprint)
		if !ok {
			return
		}
		scan, err := svc.Start(pin, environ, body.IdentityID, body.Scope)
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 202, scan)
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/discovery/scans", func(w http.ResponseWriter, r *http.Request) {
		if !scopedContext(w, r, hub) {
			return
		}
		scans := []discovery.Scan{}
		if hub.Discovery != nil {
			scans = hub.Discovery.Scans(r.PathValue("cid"))
		}
		writeJSON(w, 200, map[string]any{"scans": scans})
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/discovery/scans/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !scopedGate(w, hub) {
			return
		}
		id := r.PathValue("id")
		if !idPattern.MatchString(id) {
			errorJSON(w, 404, "scan_not_found")
			return
		}
		svc, s, pin, _, ok := pinOf(w, r, false, "")
		if !ok {
			return
		}
		view, err := svc.View(id, pin, s.Snapshot())
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, view)
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/discovery/scans/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !scopedGate(w, hub) || !decodeBody(w, r, &body) || !scopedContext(w, r, hub) {
			return
		}
		id := r.PathValue("id")
		if !idPattern.MatchString(id) || hub.Discovery == nil {
			errorJSON(w, 404, "scan_not_found")
			return
		}
		scan, err := hub.Discovery.Cancel(id, r.PathValue("cid"))
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, scan)
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/discovery/scans/{id}/candidates/{cand}/prepare", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ContextFingerprint string `json:"contextFingerprint"`
			AcknowledgeUnknown bool   `json:"acknowledgeUnknownMapping"`
		}
		if !scopedGate(w, hub) || !decodeBody(w, r, &body) {
			return
		}
		id, cand := r.PathValue("id"), r.PathValue("cand")
		if !idPattern.MatchString(id) || !idPattern.MatchString(cand) {
			errorJSON(w, 404, "candidate_not_found")
			return
		}
		svc, s, pin, environ, ok := pinOf(w, r, true, body.ContextFingerprint)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
		defer cancel()
		prefill, err := svc.Prepare(ctx, id, cand, pin, environ, s.Snapshot(), body.AcknowledgeUnknown)
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, prefill)
	})
}

// scopedGate refuses a scoped request while Hub execution is off, before reading the body,
// so nothing is revealed.
func scopedGate(w http.ResponseWriter, hub *projects.Hub) bool {
	if !hub.ScopedExecution {
		projectError(w, hub, projects.ErrScopedDisabled)
		return false
	}
	return true
}

// scopeDiscoveryPin is the discovery context of scope s, with the inherited AWS profile name
// when it has no execution profile, as for the launch project.
func scopeDiscoveryPin(ctx context.Context, s *projects.Scope, check bool, reviewed string) (discovery.Pin, []string, error) {
	pin, environ, err := s.DiscoveryPin(ctx, check, reviewed)
	if err == nil && pin.ProfileID == "" {
		pin.AWSProfile = inheritedAWSProfile()
	}
	return pin, environ, err
}
