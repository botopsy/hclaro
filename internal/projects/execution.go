package projects

import (
	"errors"
	"os"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/profiles"
)

// ProfileRefusal explains why an execution profile cannot be used. Nothing ran.
type ProfileRefusal struct {
	Code    string
	Message string
}

func (e *ProfileRefusal) Error() string { return e.Message }

var (
	errProfileNotFound = &ProfileRefusal{"profile_not_found", "The execution profile no longer exists. Choose another profile."}
	errNoProfiles      = &ProfileRefusal{"profiles_unavailable", "Execution profiles are not available in this session."}
)

// execution resolves an optional profile of p. Without a profile it returns the project's
// engine preference and nil environ (the inherited environment, exactly as before). With a
// profile it returns the profile's engine preference (or the project's), the composed child
// environment and the sanitized context. Any blocking issue refuses: a broken profile is
// never used partially.
func (h *Hub) execution(p Record, profileID string) (preference string, environ []string, context *operations.ExecutionContext, err error) {
	if profileID == "" {
		return p.EnginePreference, nil, nil, nil
	}
	if h.Profiles == nil {
		return "", nil, nil, errNoProfiles
	}
	profile, err := h.Profiles.Get(p.ID, profileID)
	if err != nil {
		if errors.Is(err, profiles.ErrNotFound) {
			return "", nil, nil, errProfileNotFound
		}
		return "", nil, nil, errNoProfiles
	}
	base := os.Environ()
	resolved := profiles.Resolve(profile, p.RootPath, base)
	if issue := resolved.Blocking(); issue != nil {
		return "", nil, nil, &ProfileRefusal{issue.Code, issue.Message}
	}
	environ, err = profiles.Compose(base, resolved)
	if err != nil {
		return "", nil, nil, &ProfileRefusal{"unsafe_cli_args", "Inherited TF_CLI_ARGS settings add flags other than -no-color. " + engine.UnsafeArgsHelp(base)}
	}
	c := operations.FromResolved(resolved)
	preference = profile.Engine
	if preference == "" {
		preference = p.EnginePreference
	}
	return preference, environ, &c, nil
}

// Resolve returns the sanitized execution context of a project's profile, or of no profile
// when profileID is empty, for review before an operation. It runs no command and reads no
// file contents.
func (h *Hub) Resolve(projectID, profileID string) (profiles.Resolved, Record, error) {
	p, err := h.Registry.Get(projectID)
	if err != nil {
		return profiles.Resolved{}, Record{}, err
	}
	if profileID == "" {
		return profiles.None(p.ID, os.Environ()), p, nil
	}
	if h.Profiles == nil {
		return profiles.Resolved{}, p, errNoProfiles
	}
	profile, err := h.Profiles.Get(p.ID, profileID)
	if errors.Is(err, profiles.ErrNotFound) {
		return profiles.Resolved{}, p, errProfileNotFound
	}
	if err != nil {
		return profiles.Resolved{}, p, errNoProfiles
	}
	return profiles.Resolve(profile, p.RootPath, os.Environ()), p, nil
}

// isLaunch reports whether root is the folder Hclaro was launched with, under any spelling.
func (h *Hub) isLaunch(root string) bool {
	return h.Launch != "" && (root == h.Launch || sameDir(root, h.Launch))
}

// LaunchID returns the registry ID of the project Hclaro was launched with, or "" when it is
// not registered. Execution profiles belong to registered projects only.
func (h *Hub) LaunchID() string {
	if h.Launch == "" {
		return ""
	}
	records, _ := h.Registry.List()
	for _, p := range records {
		if h.isLaunch(p.RootPath) {
			return p.ID
		}
	}
	return ""
}

// LaunchBinder resolves profiles of the launch project for the operation manager.
func (h *Hub) LaunchBinder() operations.Binder {
	return func(profileID string) (operations.Binding, error) {
		id := h.LaunchID()
		if id == "" {
			return operations.Binding{}, &operations.RejectError{Code: "profiles_unavailable", Reason: "Save this project to the Hub to use execution profiles."}
		}
		p, err := h.Registry.Get(id)
		if err != nil {
			return operations.Binding{}, &operations.RejectError{Code: "profiles_unavailable", Reason: "Save this project to the Hub to use execution profiles."}
		}
		_, environ, c, err := h.execution(p, profileID)
		var refusal *ProfileRefusal
		if errors.As(err, &refusal) {
			return operations.Binding{}, &operations.RejectError{Code: refusal.Code, Reason: refusal.Message}
		}
		if err != nil || c == nil {
			return operations.Binding{}, &operations.RejectError{Code: "profile_not_found", Reason: errProfileNotFound.Message}
		}
		return operations.Binding{Context: *c, Environ: environ}, nil
	}
}

// ProfileBusy returns ErrBusy while a context opened with the profile runs an operation, so
// an edit or removal of the profile, which would close it, is refused first.
func (h *Hub) ProfileBusy(projectID, profileID string) error {
	if h.running(func(c *Context) bool { return c.ProjectID == projectID && c.profileID == profileID }) {
		return ErrBusy
	}
	return nil
}

// ProfileChanged closes read contexts opened with a profile that was edited or removed, so
// no tab keeps reading or running tools with an outdated context.
func (h *Hub) ProfileChanged(projectID, profileID, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for cid, c := range h.contexts {
		if c.ProjectID == projectID && c.profileID == profileID {
			h.closeLocked(cid, reason)
		}
	}
}

// Summarize returns the filesystem summary of a record (no engine command).
func (h *Hub) Summarize(p Record) Summary { return h.summary(p) }
