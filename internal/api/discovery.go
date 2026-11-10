package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"time"

	"hclaro/internal/discovery"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/state"
)

// verifyTimeout bounds an explicit identity verification or the STS check of a prepare.
const verifyTimeout = 45 * time.Second

var awsProfileName = regexp.MustCompile(`^[A-Za-z0-9_.+=@,-]{1,64}$`)

// discoveryPin resolves the context of a discovery request: the launch workspace and engine,
// and the execution profile (checked against the reviewed fingerprint when one is given).
// environ is the profile's composed environment, or nil for Hclaro's own.
func discoveryPin(manager *operations.Manager, profileID, expected string) (discovery.Pin, []string, error) {
	c, environ, err := manager.ResolveProfile(profileID, expected)
	if err != nil {
		return discovery.Pin{}, nil, err
	}
	snap := manager.Snapshot()
	pin := discovery.Pin{Workspace: snap.Info.Workspace, Engine: snap.Info.Engine, ContextFingerprint: "inherited"}
	if c != nil {
		pin.ProjectID, pin.ProfileID, pin.ProfileName, pin.ContextFingerprint = c.ProjectID, c.ProfileID, c.ProfileName, c.Fingerprint
		pin.AWSProfile, pin.AWSRegion = c.AWSProfile, c.AWSRegion
	} else {
		pin.AWSProfile = inheritedAWSProfile()
	}
	return pin, environ, nil
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
func discoveryRoutes(mux *http.ServeMux, manager *operations.Manager, hub *projects.Hub) {
	available := func(w http.ResponseWriter) (*discovery.Service, bool) {
		switch {
		case manager.Discovery == nil:
			reasonJSON(w, 409, "discovery_unavailable", "Cloud discovery is not available in this session.")
		case manager.Root() == "":
			reasonJSON(w, 409, "no_launch_project", "Cloud discovery runs for the project Hclaro was opened with from the terminal.")
		default:
			return manager.Discovery, true
		}
		return nil, false
	}
	pinOf := func(w http.ResponseWriter, profileID, expected string) (discovery.Pin, []string, bool) {
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return discovery.Pin{}, nil, false
		}
		pin, environ, err := discoveryPin(manager, profileID, expected)
		var rejected *operations.RejectError
		switch {
		case errors.As(err, &rejected):
			reasonJSON(w, 409, rejected.Code, rejected.Reason)
			return discovery.Pin{}, nil, false
		case err != nil:
			reasonJSON(w, 409, "context_unavailable", "The execution context could not be resolved.")
			return discovery.Pin{}, nil, false
		}
		return pin, environ, true
	}
	mux.HandleFunc("GET /api/v1/discovery/options", func(w http.ResponseWriter, r *http.Request) {
		profileID := r.URL.Query().Get("profileId")
		if !validProfileParam(profileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		snap := manager.Snapshot()
		canScan, reason := true, ""
		switch {
		case manager.Discovery == nil:
			canScan, reason = false, "Cloud discovery is not available in this session."
		case manager.Root() == "":
			canScan, reason = false, "Cloud discovery runs for the project Hclaro was opened with from the terminal."
		}
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
		var identity *discovery.CloudIdentity
		region, regionSource := "us-east-1", "default"
		if canScan {
			if pin, environ, perr := discoveryPin(manager, profileID, ""); perr == nil {
				region, regionSource = discovery.RegionOf(pin, environ)
				if id, ok := manager.Discovery.Identity(pin); ok {
					identity = &id
				}
			}
		}
		writeJSON(w, 200, struct {
			CanScan       bool                     `json:"canScan"`
			Reason        string                   `json:"reason,omitempty"`
			Provider      string                   `json:"provider"`
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
		}{canScan, reason, "aws", discovery.Types, discovery.Regions, region, regionSource, map[string]int{
			"maxRegions": discovery.MaxRegions, "maxPerType": discovery.MaxPerTypeLimit, "defaultPerType": discovery.DefaultPerType,
			"maxConcurrency": discovery.MaxConcurrency, "defaultConcurrency": discovery.DefaultConcurrent, "pageSize": discovery.PageSize,
		}, snap.Info.Project, snap.Info.Engine, snap.Info.Workspace, snap.Info.Backend, snap.Status, identity, projectID, context, profileError})
	})
	mux.HandleFunc("POST /api/v1/discovery/identity", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProfileID          string `json:"profileId"`
			ContextFingerprint string `json:"contextFingerprint"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		pin, environ, ok := pinOf(w, body.ProfileID, body.ContextFingerprint)
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
	mux.HandleFunc("POST /api/v1/discovery/scans", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IdentityID         string                   `json:"identityId"`
			ProfileID          string                   `json:"profileId"`
			ContextFingerprint string                   `json:"contextFingerprint"`
			Scope              discovery.DiscoveryScope `json:"scope"`
			// The scan preview must have been shown: listing calls AWS APIs.
			Acknowledge *bool `json:"acknowledgeApiCalls"`
		}
		if !decodeBody(w, r, &body) {
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
		svc, ok := available(w)
		if !ok {
			return
		}
		pin, environ, ok := pinOf(w, body.ProfileID, body.ContextFingerprint)
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
	mux.HandleFunc("GET /api/v1/discovery/scans", func(w http.ResponseWriter, r *http.Request) {
		svc, ok := available(w)
		if !ok {
			return
		}
		writeJSON(w, 200, map[string]any{"scans": svc.Scans("")})
	})
	mux.HandleFunc("GET /api/v1/discovery/scans/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !idPattern.MatchString(id) {
			errorJSON(w, 404, "scan_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		pin, _, ok := pinOf(w, r.URL.Query().Get("profileId"), "")
		if !ok {
			return
		}
		view, err := svc.View(id, pin, manager.Snapshot())
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, view)
	})
	mux.HandleFunc("POST /api/v1/discovery/scans/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		id := r.PathValue("id")
		if !idPattern.MatchString(id) {
			errorJSON(w, 404, "scan_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		scan, err := svc.Cancel(id, "")
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, scan)
	})
	mux.HandleFunc("POST /api/v1/discovery/scans/{id}/candidates/{cid}/prepare", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProfileID          string `json:"profileId"`
			ContextFingerprint string `json:"contextFingerprint"`
			AcknowledgeUnknown bool   `json:"acknowledgeUnknownMapping"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		id, cid := r.PathValue("id"), r.PathValue("cid")
		if !idPattern.MatchString(id) || !idPattern.MatchString(cid) {
			errorJSON(w, 404, "candidate_not_found")
			return
		}
		svc, ok := available(w)
		if !ok {
			return
		}
		pin, environ, ok := pinOf(w, body.ProfileID, body.ContextFingerprint)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
		defer cancel()
		prefill, err := svc.Prepare(ctx, id, cid, pin, environ, manager.Snapshot(), body.AcknowledgeUnknown)
		if err != nil {
			discoveryError(w, err)
			return
		}
		writeJSON(w, 200, prefill)
	})
}

// checkDiscoveryImport verifies an import started from a prepared discovery candidate. It
// returns false after writing the refusal.
func checkDiscoveryImport(w http.ResponseWriter, manager *operations.Manager, token, profileID, fingerprint string, check func(discovery.Pin) error) bool {
	if manager.Discovery == nil || !idPattern.MatchString(token) {
		reasonJSON(w, 409, "discovery_stale", "This import was prepared from a discovery scan that is no longer available. Prepare it again from Cloud discovery.")
		return false
	}
	pin, _, err := discoveryPin(manager, profileID, fingerprint)
	var rejected *operations.RejectError
	switch {
	case errors.As(err, &rejected):
		reasonJSON(w, 409, rejected.Code, rejected.Reason)
		return false
	case err != nil:
		reasonJSON(w, 409, "context_unavailable", "The execution context could not be resolved.")
		return false
	}
	if check(pin) != nil {
		reasonJSON(w, 409, "discovery_stale", "The import no longer matches the discovered object or its context (another account, workspace, profile, import ID or resource type, or the preparation expired). Prepare it again from Cloud discovery, or clear the discovery link to import manually.")
		return false
	}
	return true
}

// discoveryError maps discovery errors to typed responses. Messages never include CLI output.
func discoveryError(w http.ResponseWriter, err error) {
	var call *discovery.CallError
	switch {
	case errors.As(err, &call):
		status := 502
		switch call.Kind {
		case discovery.KindIdentityChanged:
			status = 409
		case discovery.KindMissingCLI, discovery.KindUnsupportedCLI:
			status = 409
		}
		reasonJSON(w, status, call.Kind, discovery.Message(call.Kind))
	case errors.Is(err, discovery.ErrInvalidScope):
		reasonJSON(w, 400, "invalid_scope", "Choose 1 to 6 known regions, at least one supported type, 1 to 1000 objects per type and 1 to 4 concurrent reads.")
	case errors.Is(err, discovery.ErrIdentityStale):
		reasonJSON(w, 409, "identity_required", "Verify the AWS context again: the verification is missing, expired or was made for another profile or workspace.")
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
