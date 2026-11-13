package operations

import (
	"slices"

	"hclaro/internal/engine"
	"hclaro/internal/gitctx"
	"hclaro/internal/preflight"
	"hclaro/internal/profiles"
	"hclaro/internal/workspace"
)

// ExecutionContext is the sanitized execution context an operation is bound to: which
// profile and revision, the workspace, variable files, plan options and cloud aliases. It
// is copied into the operation when it starts and never changes afterwards, so editing or
// deleting the profile cannot redirect a running process. It holds no environment values.
type ExecutionContext struct {
	ProjectID          string   `json:"projectId,omitempty"`
	ProfileID          string   `json:"profileId,omitempty"`
	ProfileName        string   `json:"profileName,omitempty"`
	Revision           int      `json:"profileRevision,omitempty"`
	EnginePreference   string   `json:"enginePreference,omitempty"`
	Workspace          string   `json:"workspace"`
	VarFiles           []string `json:"varFiles"`
	Parallelism        int      `json:"parallelism,omitempty"`
	LockTimeoutSeconds int      `json:"lockTimeoutSeconds,omitempty"`
	AWSProfile         string   `json:"awsProfile,omitempty"`
	AWSRegion          string   `json:"awsRegion,omitempty"`
	EnvNames           []string `json:"envNames"`
	Identity           string   `json:"identity"`
	Notices            []string `json:"notices"`
	Fingerprint        string   `json:"fingerprint"`
	// approved maps a profile variable file reference to its canonical root-relative path.
	approved map[string]string
}

// Clone returns an independent copy.
func (c *ExecutionContext) Clone() *ExecutionContext {
	if c == nil {
		return nil
	}
	out := *c
	out.VarFiles = slices.Clone(c.VarFiles)
	out.EnvNames = slices.Clone(c.EnvNames)
	out.Notices = slices.Clone(c.Notices)
	out.approved = map[string]string{}
	for k, v := range c.approved {
		out.approved[k] = v
	}
	return &out
}

// Approve records the canonical path of an approved variable file reference.
func (c *ExecutionContext) Approve(ref, path string) {
	if c.approved == nil {
		c.approved = map[string]string{}
	}
	c.approved[ref] = path
}

// Binding is a resolved profile: its sanitized context and the composed child environment.
// The environment is held only in memory for the operation and its saved plan.
type Binding struct {
	Context ExecutionContext
	Environ []string
}

// Binder resolves a profile of the launch project. It runs no command. A *RejectError
// explains a profile that cannot be used (not found, missing variable file, unsafe
// inherited flags, …).
type Binder func(profileID string) (Binding, error)

// PlanStart is a validated plan request. ProfileID binds the plan to an execution profile;
// ContextFingerprint must then equal the fingerprint the user reviewed.
type PlanStart struct {
	VarFiles           []string
	Replace            []string
	ProfileID          string
	ContextFingerprint string
}

// bind resolves and checks a profile for the current launch snapshot. Caller holds mu.
func (m *Manager) bind(snap workspace.Snapshot, profileID, expected string) (*ExecutionContext, []string, error) {
	if profileID == "" {
		return nil, nil, nil
	}
	if m.Binder == nil {
		return nil, nil, reject("profiles_unavailable", "Execution profiles are not available for this project. Save it to the Hub first.")
	}
	b, err := m.Binder(profileID)
	if err != nil {
		return nil, nil, err
	}
	c := b.Context.Clone()
	switch {
	case expected == "" || c.Fingerprint != expected:
		return nil, nil, reject("context_changed", "The execution profile changed after you reviewed it. Review the execution context again before planning.")
	case c.EnginePreference != "" && c.EnginePreference != snap.Info.Engine:
		return nil, nil, reject("profile_engine", "This profile prefers "+engineLabel(c.EnginePreference)+", but this project was opened with "+engineLabel(snap.Info.Engine)+". Restart Hclaro with --engine "+c.EnginePreference+", or choose another profile.")
	case c.Workspace != "" && c.Workspace != snap.Info.Workspace:
		return nil, nil, reject("profile_workspace", "This profile selects workspace "+c.Workspace+", but this project reads workspace "+snap.Info.Workspace+". A Terraform workspace is a state namespace, not an account: choose a profile for "+snap.Info.Workspace+".")
	}
	// The operation runs in the workspace the snapshot was read from, passed explicitly.
	c.Workspace = snap.Info.Workspace
	return c, slices.Clone(b.Environ), nil
}

// bound returns the planner for an operation: the launch adapter, or a copy bound to a
// profile's environment and workspace.
func (m *Manager) bound(environ []string, ws string) engine.Planner {
	if environ == nil || m.Bind == nil {
		return m.planner
	}
	return m.Bind(environ, ws)
}

// fingerprintFor is the plan fingerprint; with a profile it covers the composed
// environment and the profile's context fingerprint.
func (m *Manager) fingerprintFor(snap workspace.Snapshot, varFiles []string, skip string, c *ExecutionContext, environ []string) (string, error) {
	if c == nil {
		return m.fingerprintExcept(snap, varFiles, skip)
	}
	labels := append([]string{snap.Info.Engine, snap.Info.Version, snap.Info.Workspace, "profile:" + c.Fingerprint}, varFiles...)
	return workspace.FingerprintExcept(m.root, environ, map[string]bool{skip: skip != ""}, labels...)
}

// currentFingerprint recomputes a plan's fingerprint now. For a profile-bound plan the
// profile is resolved again. A refusal is context_changed for a changed or removed profile
// and configuration_unreadable when the configuration could not be read.
func (m *Manager) currentFingerprint(p *Plan) (string, *RejectError) {
	return m.contextFingerprint(p.VarFiles, p.Context, "plan", "Run a new plan with the current profile.")
}

// contextFingerprint recomputes the fingerprint of an operation context now; what names
// the kind of result ("plan", "scan") in the reason and next says what to do.
func (m *Manager) contextFingerprint(varFiles []string, c *ExecutionContext, what, next string) (string, *RejectError) {
	snap := m.store.Get()
	var environ []string
	if c != nil {
		changed := &RejectError{"context_changed", "The execution profile " + c.ProfileName + " was changed or removed after this " + what + " was created. " + next}
		if m.Binder == nil {
			return "", changed
		}
		b, err := m.Binder(c.ProfileID)
		if err != nil || b.Context.Fingerprint != c.Fingerprint {
			return "", changed
		}
		environ = b.Environ
	}
	fp, err := m.fingerprintFor(snap, varFiles, "", c, environ)
	if err != nil {
		return "", &RejectError{"configuration_unreadable", "Hclaro could not read the configuration, so it cannot tell whether it changed since this " + what + " was created: " + err.Error() + ". " + next}
	}
	return fp, nil
}

// unreadableNote is logged when the configuration cannot be fingerprinted at the start
// of an operation.
func unreadableNote(err error) string {
	return "Hclaro could not read the configuration: " + err.Error() + "."
}

// SourceStatus compares the plan's recorded source fingerprint with the working tree now.
func (m *Manager) SourceStatus(p *Plan) gitctx.SourceStatus {
	if m.Git == nil || p.Source == nil {
		return gitctx.SourceStatus{State: "not_recorded", Message: "No source fingerprint was recorded for this plan."}
	}
	return m.Git.Compare(p.Source, m.root)
}

// sourceFacts turns a source comparison into preflight facts; nil when none was recorded.
func sourceFacts(st gitctx.SourceStatus) *preflight.SourceFacts {
	if st.Recorded == nil || st.Current == nil {
		return nil
	}
	label := func(fp *gitctx.PlanSourceFingerprint) string {
		switch {
		case !fp.Repository:
			return "a folder outside Git"
		case fp.Commit == "":
			return "a branch without commits"
		}
		return "commit " + fp.Commit[:min(12, len(fp.Commit))]
	}
	return &preflight.SourceFacts{Unknown: st.State == "unknown", FilesChanged: st.FilesChanged, HeadChanged: st.HeadChanged, Planned: label(st.Recorded), Current: label(st.Current)}
}

// ContextStatus reports whether a plan's context changed and whether it can be applied now.
// A source change (IaC files of local modules outside the root, which the context
// fingerprint does not cover) also makes the plan unavailable for apply.
func (m *Manager) ContextStatus(p *Plan) (changed bool, status ApplyStatus) {
	current, rej := m.currentFingerprint(p)
	changed, status = current == "" || current != p.Fingerprint, m.applyStatus(p, current, rej)
	if src := m.SourceStatus(p); status.Available && (src.FilesChanged || src.State == "unknown") {
		status = ApplyStatus{Reason: src.Message + " Run a new plan.", ArtifactRetained: status.ArtifactRetained}
		changed = true
	}
	return changed, status
}

// FromResolved converts a resolved profile into the operation's sanitized context.
func FromResolved(r profiles.Resolved) ExecutionContext {
	c := ExecutionContext{
		ProjectID: r.ProjectID, ProfileID: r.ProfileID, ProfileName: r.ProfileName, Revision: r.Revision,
		EnginePreference: r.Engine, Workspace: r.Workspace, VarFiles: []string{}, Parallelism: r.Options.Parallelism,
		LockTimeoutSeconds: r.Options.LockTimeoutSeconds, AWSProfile: r.AWSProfile, AWSRegion: r.AWSRegion,
		EnvNames: []string{}, Identity: r.Identity, Notices: slices.Clone(r.Notices), Fingerprint: r.Fingerprint,
	}
	for _, f := range r.VarFiles {
		c.VarFiles = append(c.VarFiles, f.Ref)
		c.Approve(f.Ref, f.Path)
	}
	for _, e := range r.Env {
		c.EnvNames = append(c.EnvNames, e.Name)
	}
	return c
}

// ResolveProfile resolves a launch-project profile for work that is not an engine operation,
// such as AWS discovery (M16). expected, when not empty, must equal the context fingerprint
// the user reviewed. A profile that selects another engine or workspace is refused, as for a
// plan. With no profile, the context is nil and environ is nil (Hclaro's own environment).
func (m *Manager) ResolveProfile(profileID, expected string) (*ExecutionContext, []string, error) {
	if profileID == "" {
		return nil, nil, nil
	}
	if expected == "" {
		if m.Binder == nil {
			return nil, nil, reject("profiles_unavailable", "Execution profiles are not available for this project. Save it to the Hub first.")
		}
		b, err := m.Binder(profileID)
		if err != nil {
			return nil, nil, err
		}
		expected = b.Context.Fingerprint
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bind(m.store.Get(), profileID, expected)
}

// Snapshot returns the current launch snapshot.
func (m *Manager) Snapshot() workspace.Snapshot { return m.store.Get() }
