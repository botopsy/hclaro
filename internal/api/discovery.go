package api

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"time"

	"hclaro/internal/awscli"
	"hclaro/internal/discovery"
	"hclaro/internal/ids"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/state"
)

// verifyTimeout bounds an explicit identity verification or the STS check of a prepare.
const verifyTimeout = 45 * time.Second

var awsProfileName = regexp.MustCompile(`^[A-Za-z0-9_.+=@,-]{1,64}$`)

// discoveryPin resolves the context of a discovery request: the launch workspace and engine,
// and the execution profile. With check set, for a request that contacts AWS, expected must be
// the fingerprint the user reviewed; a read that contacts nothing and names none pins the
// profile as it is now, as a Hub scope's DiscoveryPin does. environ is the profile's composed
// environment, or nil for Hclaro's own.
func discoveryPin(manager *operations.Manager, check bool, profileID, expected string) (discovery.Pin, []string, error) {
	if !check && expected == "" && profileID != "" && manager.Binder != nil {
		if b, err := manager.Binder(profileID); err == nil {
			expected = b.Context.Fingerprint
		}
	}
	c, environ, err := manager.ResolveProfile(profileID, expected)
	if err != nil {
		return discovery.Pin{}, nil, err
	}
	snap := manager.Snapshot()
	pin := discovery.Pin{Workspace: snap.Info.Workspace, Engine: snap.Info.Engine, ContextFingerprint: "inherited"}
	if c != nil {
		pin.ProjectID, pin.ProfileID, pin.ProfileName, pin.ContextFingerprint = c.ProjectID, c.ProfileID, c.ProfileName, c.Fingerprint
		pin.AWSProfile, pin.AWSRegion = c.AWSProfile, c.AWSRegion
		pin.GCPConfiguration, pin.GCPProject, pin.GCPRegion = c.GCPConfiguration, c.GCPProject, c.GCPRegion
	} else {
		pin.AWSProfile = inheritedAWSProfile()
	}
	return pin, environ, nil
}

// providerOf validates the provider a request names: "" and "aws" are AWS (the pin keeps ""),
// "gcp" is Google Cloud. Anything else is refused.
func providerOf(name string) (string, bool) {
	switch name {
	case "", "aws":
		return "", true
	case "gcp":
		return "gcp", true
	}
	return "", false
}

// inheritedAWSProfile is the AWS_PROFILE Hclaro inherited, when it is a plain profile name.
// It is shown so the user sees which profile the CLI uses without an execution profile.
func inheritedAWSProfile() string {
	if name := os.Getenv("AWS_PROFILE"); awsProfileName.MatchString(name) && !state.Suspicious(name) {
		return name
	}
	return ""
}

// discoveryRoutes serve AWS discovery for the launch workspace. GET routes never run a
// command or contact AWS; verifying the identity, starting a scan and preparing an import are
// explicit, CSRF-protected POSTs.
func discoveryRoutes(mux *http.ServeMux, deps *Services, manager *operations.Manager, hub *projects.Hub) {
	available := func(w http.ResponseWriter) (*discovery.Service, bool) {
		switch {
		case deps.Discovery == nil:
			reasonJSON(w, 409, "discovery_unavailable", "Cloud discovery is not available in this session.")
		case manager.Root() == "":
			reasonJSON(w, 409, "no_launch_project", "Cloud discovery runs for the project Hclaro was opened with from the terminal.")
		default:
			return deps.Discovery, true
		}
		return nil, false
	}
	mux.HandleFunc("GET /api/v1/discovery/options", func(w http.ResponseWriter, r *http.Request) {
		profileID := r.URL.Query().Get("profileId")
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		provider, ok := providerOf(r.URL.Query().Get("provider"))
		if !ok {
			errorJSON(w, 400, "invalid_request")
			return
		}
		snap := manager.Snapshot()
		canScan, reason := true, ""
		switch {
		case deps.Discovery == nil:
			canScan, reason = false, "Cloud discovery is not available in this session."
		case manager.Root() == "":
			canScan, reason = false, "Cloud discovery runs for the project Hclaro was opened with from the terminal."
		}
		context, projectID, profileError := launchOptionsContext(hub, snap, profileID)
		var identity *discovery.CloudIdentity
		region, regionSource := "us-east-1", "default"
		regions, name, providers := discovery.Regions, "aws", []providerOption{}
		if provider == "gcp" {
			region, regionSource, regions, name = "us-central1", "default", discovery.GCPRegions, "gcp"
		}
		if canScan {
			providers = providerOptions(deps.Discovery)
			if pin, environ, perr := discoveryPin(manager, false, profileID, ""); perr == nil {
				pin.Provider = provider
				if provider == "gcp" {
					if pin.GCPRegion != "" {
						region, regionSource = pin.GCPRegion, "profile"
					}
				} else {
					region, regionSource = discovery.RegionOf(pin, environ)
				}
				if id, ok := deps.Discovery.Identity(pin); ok {
					identity = &id
				}
			}
		}
		writeJSON(w, 200, discoveryOptions{canScan, reason, name, providers, typesOf(name), regions, region, regionSource, discoveryLimits(), snap.Info.Project, snap.Info.Engine, snap.Info.Workspace, snap.Info.Backend, snap.Status, identity, projectID, context, profileError})
	})
	discoveryExecRoutes(mux, "/api/v1", nil, available, launchSourceOf(manager, nil))
}

// discoveryExecRoutes registers the discovery routes that verify, scan, list, read, cancel
// and prepare under prefix, as execRoutes. The AWS CLI runs with the source's execution
// profile environment; a Hub context's verifications, scans and prepared imports belong to
// that context only and are dropped with its scope. GET routes run nothing and contact
// nothing; verifying, scanning and preparing are explicit POSTs that carry the reviewed
// context fingerprint and create and revalidate a Hub scope first. A read of a Hub context
// without a scope has nothing to show: scan_not_found.
func discoveryExecRoutes(mux *http.ServeMux, prefix string, hub *projects.Hub, available func(http.ResponseWriter) (*discovery.Service, bool), source sourceOf) {
	handle := routesUnder(mux, prefix, hub)
	// pinOf resolves the discovery service and context of a request; check is set for
	// requests that contact AWS, and only those create a Hub scope. It answers refusals itself.
	pinOf := func(w http.ResponseWriter, r *http.Request, check bool, profileID, reviewed string) (*discovery.Service, execSource, discovery.Pin, []string, bool) {
		if len(reviewed) > 128 {
			errorJSON(w, 400, "invalid_request")
			return nil, nil, discovery.Pin{}, nil, false
		}
		svc, ok := available(w)
		if !ok {
			return nil, nil, discovery.Pin{}, nil, false
		}
		src, ok := source(w, r, check)
		if !ok {
			return nil, nil, discovery.Pin{}, nil, false
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		pin, environ, err := src.DiscoveryPin(ctx, check, profileID, reviewed)
		switch {
		case err == nil:
			return svc, src, pin, environ, true
		case errors.Is(err, discovery.ErrNotFound):
			discoveryError(w, err)
		case mapOpError(w, r, hub, src.Holder(), err):
		default:
			reasonJSON(w, 409, "context_unavailable", "The execution context could not be resolved.")
		}
		return nil, nil, discovery.Pin{}, nil, false
	}
	handle("POST /discovery/identity", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProfileID          *string `json:"profileId"`
			ContextFingerprint string  `json:"contextFingerprint"`
			Provider           string  `json:"provider"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		provider, ok := providerOf(body.Provider)
		if !ok {
			errorJSON(w, 400, "invalid_request")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		svc, _, pin, environ, ok := pinOf(w, r, true, profileID, body.ContextFingerprint)
		if !ok {
			return
		}
		pin.Provider = provider
		ctx, cancel := withDeadline(w, r, verifyTimeout)
		defer cancel()
		identity, err := svc.Verify(ctx, pin, environ)
		if err != nil {
			discoveryErrorFor(w, err, provider)
			return
		}
		writeJSON(w, 200, identity)
	})
	handle("POST /discovery/scans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IdentityID         string                   `json:"identityId"`
			Provider           string                   `json:"provider"`
			ProfileID          *string                  `json:"profileId"`
			ContextFingerprint string                   `json:"contextFingerprint"`
			Scope              discovery.DiscoveryScope `json:"scope"`
			// The scan preview must have been shown: listing calls AWS APIs.
			Acknowledge *bool `json:"acknowledgeApiCalls"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		provider, ok := providerOf(body.Provider)
		if !ok {
			errorJSON(w, 400, "invalid_request")
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			reasonJSON(w, 400, "acknowledgement_required", "Listing cloud objects calls cloud APIs. Confirm the scan first.")
			return
		}
		if !ids.Valid(body.IdentityID) {
			reasonJSON(w, 409, "identity_required", "Verify the AWS context before scanning.")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		svc, _, pin, environ, ok := pinOf(w, r, true, profileID, body.ContextFingerprint)
		if !ok {
			return
		}
		pin.Provider = provider
		scan, err := svc.Start(pin, environ, body.IdentityID, body.Scope)
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 202, scan)
	})
	handle("GET /discovery/scans", func(w http.ResponseWriter, r *http.Request) {
		// A Hub workspace view lists scans without checking availability first.
		if hub != nil && hub.Discovery == nil {
			writeJSON(w, 200, map[string]any{"scans": []discovery.Scan{}})
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		// A Hub context lists only its own scans; the launch project's have no context.
		writeJSON(w, 200, map[string]any{"scans": svc.Scans(r.PathValue("cid"))})
	})
	handle("GET /discovery/scans/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !ids.Valid(id) {
			errorJSON(w, 404, "scan_not_found")
			return
		}
		profileID := ""
		if hub == nil {
			profileID = r.URL.Query().Get("profileId")
		}
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		provider, ok := providerOf(r.URL.Query().Get("provider"))
		if !ok {
			errorJSON(w, 400, "invalid_request")
			return
		}
		svc, src, pin, _, ok := pinOf(w, r, false, profileID, "")
		if !ok {
			return
		}
		pin.Provider = provider
		view, err := svc.View(id, pin, src.Snapshot())
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, view)
	})
	handle("POST /discovery/scans/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		id := r.PathValue("id")
		if !ids.Valid(id) {
			errorJSON(w, 404, "scan_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		scan, err := svc.Cancel(id, r.PathValue("cid"))
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, scan)
	})
	handle("POST /discovery/scans/{id}/candidates/{cand}/prepare", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProfileID          *string `json:"profileId"`
			ContextFingerprint string  `json:"contextFingerprint"`
			AcknowledgeUnknown bool    `json:"acknowledgeUnknownMapping"`
			Provider           string  `json:"provider"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		provider, ok := providerOf(body.Provider)
		if !ok {
			errorJSON(w, 400, "invalid_request")
			return
		}
		id, cand := r.PathValue("id"), r.PathValue("cand")
		if !ids.Valid(id) || !ids.Valid(cand) {
			errorJSON(w, 404, "candidate_not_found")
			return
		}
		profileID, ok := profileOf(w, hub, body.ProfileID)
		if !ok {
			return
		}
		svc, src, pin, environ, ok := pinOf(w, r, true, profileID, body.ContextFingerprint)
		if !ok {
			return
		}
		pin.Provider = provider
		ctx, cancel := withDeadline(w, r, verifyTimeout)
		defer cancel()
		prefill, err := svc.Prepare(ctx, id, cand, pin, environ, src.Snapshot(), body.AcknowledgeUnknown)
		if err != nil {
			discoveryErrorFor(w, err, provider)
			return
		}
		writeJSON(w, 200, prefill)
	})
}

// discoveryError maps discovery errors to typed responses. Messages never include CLI output.
func discoveryError(w http.ResponseWriter, err error) { discoveryErrorFor(w, err, "") }

// discoveryErrorFor is discoveryError with the CLI wording of provider ("" is AWS).
func discoveryErrorFor(w http.ResponseWriter, err error, provider string) {
	var call *awscli.CallError
	switch {
	case errors.As(err, &call):
		status := 502
		switch call.Kind {
		case awscli.KindIdentityChanged:
			status = 409
		case awscli.KindMissingCLI, awscli.KindUnsupportedCLI:
			status = 409
		}
		reasonJSON(w, status, call.Kind, discovery.Message(provider, call.Kind))
	case errors.Is(err, discovery.ErrInvalidScope):
		reasonJSON(w, 400, "invalid_scope", "Choose 1 to 6 known regions, at least one supported type of the provider, 1 to 1000 objects per type and 1 to 4 concurrent reads.")
	case errors.Is(err, discovery.ErrIdentityStale):
		reasonJSON(w, 409, "identity_required", "Verify the cloud context again: the verification is missing, expired or was made for another profile or workspace.")
	case errors.Is(err, discovery.ErrTooManyScans):
		reasonJSON(w, 409, "too_many_scans", "Two scans are already running. Wait for one to finish or cancel it.")
	case errors.Is(err, discovery.ErrClosed):
		errorJSON(w, 503, "shutting_down")
	case errors.Is(err, discovery.ErrNotFound):
		reasonJSON(w, 404, "scan_not_found", "This scan or object is no longer available. Scans are kept in memory for this session only.")
	case errors.Is(err, discovery.ErrScanStale):
		reasonJSON(w, 409, "scan_stale", "This scan was made for another execution profile or workspace. Scan again in this context.")
	case errors.Is(err, discovery.ErrScanNotReady):
		reasonJSON(w, 409, "scan_not_ready", "The scan is still running or failed.")
	case errors.Is(err, discovery.ErrNotImportable):
		reasonJSON(w, 409, "not_importable", "This object is already recorded in this workspace's state, or has no verified import ID.")
	case errors.Is(err, discovery.ErrAcknowledge):
		reasonJSON(w, 409, "acknowledgement_required", "The state mapping of this object is unknown. Confirm that you reviewed it before preparing an import.")
	default:
		reasonJSON(w, 502, "discovery_failed", "Cloud discovery failed.")
	}
}

// providerOption is one cloud discovery can list. Detected says its CLI is on PATH; the UI
// offers only detected providers besides AWS, whose missing CLI is explained when verifying.
type providerOption struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	CLI      string `json:"cli"`
	Detected bool   `json:"detected"`
}

var providerInfo = map[string]providerOption{
	"aws": {Name: "aws", Label: "AWS", CLI: "aws"},
	"gcp": {Name: "gcp", Label: "Google Cloud", CLI: "gcloud"},
}

func providerOptions(svc *discovery.Service) []providerOption {
	out := []providerOption{}
	for _, name := range svc.Providers() {
		if info, ok := providerInfo[name]; ok {
			info.Detected = name == "aws" || svc.Detect != nil && svc.Detect(info.CLI)
			out = append(out, info)
		}
	}
	return out
}

// typesOf returns the supported types of one provider.
func typesOf(provider string) []discovery.ResourceType {
	out := []discovery.ResourceType{}
	for _, t := range discovery.Types {
		if t.Provider == "" && provider == "aws" || t.Provider == provider {
			out = append(out, t)
		}
	}
	return out
}

type discoveryOptions struct {
	CanScan       bool                     `json:"canScan"`
	Reason        string                   `json:"reason,omitempty"`
	Provider      string                   `json:"provider"`
	Providers     []providerOption         `json:"providers"`
	Types         []discovery.ResourceType `json:"types"`
	Regions       []string                 `json:"regions"`
	DefaultRegion string                   `json:"defaultRegion"`
	RegionSource  string                   `json:"regionSource"`
	Limits        map[string]int           `json:"limits"`
	Project       string                   `json:"project"`
	Engine        string                   `json:"engine"`
	Workspace     string                   `json:"workspace"`
	Backend       string                   `json:"backend"`
	StateStatus   string                   `json:"stateStatus"`
	Identity      *discovery.CloudIdentity `json:"identity,omitempty"`
	ProjectID     string                   `json:"projectId,omitempty"`
	Context       *executionSummary        `json:"context,omitempty"`
	ProfileError  string                   `json:"profileError,omitempty"`
}

// discoveryLimits are the scan bounds the discovery options report, the same for launch and Hub.
func discoveryLimits() map[string]int {
	return map[string]int{
		"maxRegions": discovery.MaxRegions, "maxPerType": discovery.MaxPerTypeLimit, "maxObjects": discovery.MaxObjects, "defaultPerType": discovery.DefaultPerType,
		"maxConcurrency": discovery.MaxConcurrency, "defaultConcurrency": discovery.DefaultConcurrent, "pageSize": discovery.PageSize,
	}
}
