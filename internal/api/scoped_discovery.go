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
	mux.HandleFunc("GET /api/v1/contexts/{cid}/discovery/options", scoped(hub, func(w http.ResponseWriter, r *http.Request) {
		s, base, ok := scopedOptions(w, r, hub)
		if !ok {
			return
		}
		out := scopedDiscoveryOptions{analysisOptions: base, Provider: "aws", Types: discovery.Types, Regions: discovery.Regions, DefaultRegion: "us-east-1", RegionSource: "default", Limits: map[string]int{
			"maxRegions": discovery.MaxRegions, "maxPerType": discovery.MaxPerTypeLimit, "maxObjects": discovery.MaxObjects, "defaultPerType": discovery.DefaultPerType,
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
	}))
	available := func(w http.ResponseWriter) (*discovery.Service, bool) {
		if hub.Discovery == nil {
			reasonJSON(w, 409, "discovery_unavailable", "Cloud discovery is not available in this session.")
			return nil, false
		}
		return hub.Discovery, true
	}
	discoveryExecRoutes(mux, "/api/v1/contexts/{cid}", hub, available, hubSourceOf(hub))
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

type scopedDiscoveryOptions struct {
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
}
