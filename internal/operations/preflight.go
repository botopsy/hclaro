package operations

import (
	"context"
	"crypto/subtle"
	"slices"
	"strings"
	"sync"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/preflight"
)

// preflightBinding ties a passed preflight to the retained artifact. It is opaque to the browser
// (a 256-bit token), short-lived, and replaced by every new preflight of the plan.
type preflightBinding struct {
	token       string
	expires     time.Time
	checkedAt   time.Time
	acknowledge []string
	identity    preflight.Identity
	policy      string
}

// PreflightRequest asks for a preflight. VerifyIdentity runs the read-only cloud identity
// probe, which contacts AWS; it is never run otherwise.
type PreflightRequest struct {
	VerifyIdentity bool
}

// PreflightContext is the context identity shown at the top of the preflight.
type PreflightContext struct {
	Project          string            `json:"project"`
	Engine           string            `json:"engine"`
	EngineVersion    string            `json:"engineVersion"`
	Workspace        string            `json:"workspace"`
	Backend          string            `json:"backend"`
	ExecutionMode    string            `json:"executionMode"`
	PlanKind         string            `json:"planKind"`
	PlanCreatedAt    string            `json:"planCreatedAt"`
	ArtifactSHA256   string            `json:"artifactSha256"`
	Fingerprint      string            `json:"fingerprint"`
	VarFiles         []string          `json:"varFiles"`
	ExecutionContext *ExecutionContext `json:"executionContext,omitempty"`
}

// PreflightReport is the server-side preflight of one saved plan. Binding is set only when
// nothing blocks; it must be presented, with every warning acknowledged, to get an approval.
type PreflightReport struct {
	PlanID      string               `json:"planId"`
	GeneratedAt string               `json:"generatedAt"`
	ExpiresAt   string               `json:"expiresAt,omitempty"`
	Context     PreflightContext     `json:"context"`
	Checks      []preflight.Check    `json:"checks"`
	Blocked     bool                 `json:"blocked"`
	Blockers    int                  `json:"blockers"`
	Acknowledge []string             `json:"acknowledge"`
	Impact      preflight.Impact     `json:"impact"`
	Policy      preflight.PolicyInfo `json:"policy"`
	Identity    preflight.Identity   `json:"identity"`
	Binding     string               `json:"binding,omitempty"`
	Phrase      string               `json:"phrase,omitempty"`
	Command     []string             `json:"command"`
}

// PreflightSummary is recorded on an apply operation: which policy applied and which
// warnings were acknowledged. It holds check IDs only, never evidence or account IDs.
type PreflightSummary struct {
	CheckedAt        string   `json:"checkedAt"`
	Policy           string   `json:"policy"`
	Acknowledged     []string `json:"acknowledged"`
	IdentityVerified bool     `json:"identityVerified"`
}

// ApprovalRequest presents a preflight binding and the acknowledged warning check IDs.
type ApprovalRequest struct {
	Binding      string
	Acknowledged []string
}

func notRequested() preflight.Identity { return preflight.Identity{State: "not_requested"} }

// facts collects the preflight input for p. It hashes the artifact and probes Git; the
// identity is passed in because it is only probed on request.
func (m *Manager) facts(ctx context.Context, p *Plan, identity preflight.Identity) preflight.Input {
	current, reason := m.currentFingerprint(p)
	snap := m.store.Get()
	m.mu.Lock()
	a := m.artifact
	retained := a != nil && a.planID == p.ID && m.notes[p.ID] == ""
	in := preflight.Input{
		Now:              time.Now(),
		Recorded:         preflight.Recorded{Engine: p.Engine, Version: snap.Info.Version, Workspace: p.Workspace},
		Current:          preflight.Recorded{Engine: snap.Info.Engine, Version: snap.Info.Version, Workspace: snap.Info.Workspace},
		FingerprintMatch: current != "" && current == p.Fingerprint,
		ProfileReason:    reason,
		ArtifactRetained: retained,
		ArtifactNote:     m.notes[p.ID],
		Digest:           p.ArtifactSHA256,
		Ineligible:       ineligible(p),
		Unsupported:      unsupportedCount(p),
		Backend:          snap.Info.Backend,
		ExecutionMode:    snap.Info.ExecutionMode,
		CanApply:         snap.Info.Capabilities.CanApply,
		ApplyReason:      snap.Info.Capabilities.ApplyReason,
		EngineSupported:  engine.Supported(snap.Info.Engine, snap.Info.Version),
		Running:          m.running != nil,
		Loading:          snap.Status == "loading",
		Closed:           m.closed,
		Kind:             p.Kind,
		Requested:        slices.Clone(p.Replace),
		Changes:          p.Changes,
		Summary:          p.Summary,
		Destructive:      p.Destructive,
		Imports:          p.Imports,
		Identity:         identity,
	}
	path := ""
	if retained {
		in.Recorded.Version = a.version
		in.Digest = a.digest
		in.ExpiresAt = a.created.Add(m.PlanMaxAge)
		path = a.path
	}
	m.mu.Unlock()
	if p.Context != nil {
		in.ProfileName, in.AWSProfile = p.Context.ProfileName, p.Context.AWSProfile
	}
	if holder, held := m.Locks.Holder(m.root); held {
		in.LockHolder = holder
	}
	if path != "" {
		if digest, err := sha256File(path); err == nil {
			match := digest == in.Digest
			in.DigestMatch = &match
		}
	}
	in.Git = preflight.GitResult{State: preflight.GitUnavailable, Paths: []string{}}
	if m.GitProbe != nil {
		in.Git = m.GitProbe(ctx, m.root)
	}
	in.Source = sourceFacts(m.SourceStatus(p))
	in.Policy, in.PolicyInfo = preflight.LoadPolicy(m.root)
	return in
}

func acknowledgements(checks []preflight.Check) (blockers int, ack []string) {
	ack = []string{}
	for _, c := range checks {
		if c.Blocks() {
			blockers++
		}
		if c.NeedsAcknowledgement() {
			ack = append(ack, c.ID)
		}
	}
	slices.Sort(ack)
	return blockers, ack
}

// sameSet reports whether got names exactly the IDs in want (sorted, unique).
func sameSet(want, got []string) bool {
	g := slices.Clone(got)
	slices.Sort(g)
	return slices.Equal(want, slices.Compact(g))
}

// Preflight evaluates every apply check for plan id on the server. If nothing blocks, it
// issues a binding that replaces any earlier binding and approval for the plan.
func (m *Manager) Preflight(ctx context.Context, id string, req PreflightRequest) (PreflightReport, error) {
	p, ok := m.Plan(id)
	if !ok {
		return PreflightReport{}, ErrNotFound
	}
	identity := notRequested()
	var environ []string
	m.mu.Lock()
	if a := m.artifact; a != nil && a.planID == id {
		environ = slices.Clone(a.environ)
		if a.preflight != nil {
			identity = a.preflight.identity
		}
	}
	m.mu.Unlock()
	// The identity probe and the other facts run concurrently to bound the request time.
	var wg sync.WaitGroup
	if req.VerifyIdentity {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.IdentityProbe == nil {
				identity = preflight.Identity{State: "unavailable", Message: "Identity verification is not available."}
				return
			}
			identity = m.IdentityProbe(ctx, environ)
		}()
	}
	in := m.facts(ctx, p, notRequested())
	wg.Wait()
	in.Identity = identity
	checks := preflight.Evaluate(in)
	blockers, ack := acknowledgements(checks)
	now := time.Now()
	snap := m.store.Get()
	report := PreflightReport{
		PlanID: p.ID, GeneratedAt: now.UTC().Format(time.RFC3339), Checks: checks, Blocked: blockers > 0, Blockers: blockers, Acknowledge: ack,
		Impact: preflight.ImpactOf(p.Kind, p.Replace, p.Changes, p.Summary, p.Imports), Policy: in.PolicyInfo, Identity: identity, Phrase: phrase(p),
		Context: PreflightContext{
			Project: snap.Info.Project, Engine: in.Recorded.Engine, EngineVersion: in.Recorded.Version, Workspace: p.Workspace, Backend: snap.Info.Backend,
			ExecutionMode: snap.Info.ExecutionMode, PlanKind: p.Kind, PlanCreatedAt: p.CreatedAt, ArtifactSHA256: p.ArtifactSHA256,
			Fingerprint: shortHash(p.Fingerprint), VarFiles: slices.Clone(p.VarFiles), ExecutionContext: p.Context.Clone(),
		},
		Command: []string{},
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.artifact
	if a == nil || a.planID != p.ID {
		if !report.Blocked {
			// The artifact went away while the checks ran.
			report.Blocked, report.Blockers = true, 1
		}
		return report, nil
	}
	report.Command = applyCommand(a)
	// A new preflight always replaces the previous binding and any unused approval.
	a.approval = nil
	a.preflight = nil
	if report.Blocked {
		return report, nil
	}
	expires := now.Add(m.PreflightTTL)
	if limit := a.created.Add(m.PlanMaxAge); limit.Before(expires) {
		expires = limit
	}
	a.preflight = &preflightBinding{token: newID() + newID(), expires: expires, checkedAt: now, acknowledge: ack, identity: identity, policy: in.PolicyInfo.Source}
	report.Binding = a.preflight.token
	report.ExpiresAt = expires.UTC().Format(time.RFC3339)
	return report, nil
}

func shortHash(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// checkBindingLocked verifies a presented preflight binding. Caller holds mu.
func (m *Manager) checkBindingLocked(a *artifact, token string) (*preflightBinding, error) {
	b := a.preflight
	switch {
	case b == nil || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(b.token)) != 1:
		return nil, reject("preflight_required", "Run the preflight checks for this plan before approving it.")
	case !time.Now().Before(b.expires):
		a.preflight = nil
		return nil, reject("preflight_expired", "The preflight checks expired. Run them again.")
	}
	return b, nil
}

// titles names the checks with the given IDs, for refusal messages.
func titles(checks []preflight.Check, ids []string) string {
	names := []string{}
	for _, c := range checks {
		if slices.Contains(ids, c.ID) {
			names = append(names, c.Title)
		}
	}
	return strings.Join(names, "; ")
}
