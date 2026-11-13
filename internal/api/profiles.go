package api

import (
	"errors"
	"net/http"
	"os/exec"

	"hclaro/internal/engine"
	"hclaro/internal/profiles"
	"hclaro/internal/projects"
)

// profileView is the API shape of a stored execution profile. It has no value fields.
type profileView struct {
	ID                 string   `json:"id"`
	ProjectID          string   `json:"projectId"`
	Name               string   `json:"name"`
	Engine             string   `json:"engine"`
	Workspace          string   `json:"workspace"`
	VarFiles           []string `json:"varFiles"`
	Parallelism        int      `json:"parallelism"`
	LockTimeoutSeconds int      `json:"lockTimeoutSeconds"`
	AWSProfile         string   `json:"awsProfile"`
	AWSRegion          string   `json:"awsRegion"`
	EnvNames           []string `json:"envNames"`
	Revision           int      `json:"revision"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
	Default            bool     `json:"default"`
}

func viewOf(p profiles.Profile, defaultID string) profileView {
	return profileView{p.ID, p.ProjectID, p.Name, p.Engine, p.Workspace, p.VarFiles, p.Options.Parallelism, p.Options.LockTimeoutSeconds, p.AWSProfile, p.AWSRegion, p.EnvNames, p.Revision, p.CreatedAt, p.UpdatedAt, p.ID == defaultID}
}

// profileBody is the editable profile. Strict decoding refuses any other field, so a
// client cannot send values, credentials or file contents.
type profileBody struct {
	Name               string   `json:"name"`
	Engine             string   `json:"engine"`
	Workspace          string   `json:"workspace"`
	VarFiles           []string `json:"varFiles"`
	Parallelism        int      `json:"parallelism"`
	LockTimeoutSeconds int      `json:"lockTimeoutSeconds"`
	AWSProfile         string   `json:"awsProfile"`
	AWSRegion          string   `json:"awsRegion"`
	EnvNames           []string `json:"envNames"`
}

func (b profileBody) input() profiles.Input {
	return profiles.Input{Name: b.Name, Engine: b.Engine, Workspace: b.Workspace, VarFiles: b.VarFiles, Options: profiles.Options{Parallelism: b.Parallelism, LockTimeoutSeconds: b.LockTimeoutSeconds}, AWSProfile: b.AWSProfile, AWSRegion: b.AWSRegion, EnvNames: b.EnvNames}
}

// checkVarFiles resolves every referenced variable file inside the project root before a
// profile is saved, so a file that is missing or that a symbolic link points outside the
// root is refused with its reason. Operations resolve the files again when they start.
func checkVarFiles(w http.ResponseWriter, root string, refs []string) bool {
	for _, ref := range refs {
		if _, err := profiles.ResolveVarFile(root, ref); err != nil {
			var vf *profiles.VarFileError
			if errors.As(err, &vf) {
				writeJSON(w, 400, map[string]string{"error": "invalid_profile", "field": "varFiles", "code": vf.Code, "reason": vf.Message})
				return false
			}
			errorJSON(w, 400, "invalid_profile")
			return false
		}
	}
	return true
}

// validProfileParam accepts an empty value (no profile) or a profile ID.
func validProfileParam(id string) bool { return id == "" || profiles.ValidID(id) }

func profileError(w http.ResponseWriter, hub *projects.Hub, err error) {
	var field *profiles.FieldError
	switch {
	case errors.As(err, &field):
		writeJSON(w, 400, map[string]string{"error": "invalid_profile", "field": field.Field, "reason": field.Message})
	case errors.Is(err, profiles.ErrNotFound):
		reasonJSON(w, 404, "profile_not_found", "This execution profile no longer exists. Reload the profile list.")
	case errors.Is(err, profiles.ErrRevision):
		reasonJSON(w, 409, "revision_conflict", "This profile was changed in another tab or window. Reload it and apply your edit again.")
	case errors.Is(err, profiles.ErrDuplicate):
		reasonJSON(w, 409, "duplicate_profile", "Another profile of this project already uses this name.")
	case errors.Is(err, profiles.ErrTooMany):
		reasonJSON(w, 409, "too_many_profiles", "A project can have at most 50 execution profiles.")
	case errors.Is(err, profiles.ErrUnavailable):
		_, notice := hub.Profiles.Status()
		if notice == "" {
			notice = "The execution profile file changed on disk and can no longer be read. Restart Hclaro after checking it."
		}
		reasonJSON(w, 409, "profiles_unavailable", notice)
	default:
		projectError(w, hub, err)
	}
}

// profileRoutes serve the execution profile registry of each Hub project and read-only
// context summaries. Nothing here runs an engine command or reads file contents.
func profileRoutes(mux *http.ServeMux, hub *projects.Hub) {
	// project resolves the path's project; profiles of other projects are never reachable.
	project := func(w http.ResponseWriter, r *http.Request) (projects.Record, bool) {
		p, err := hub.Registry.Get(r.PathValue("id"))
		if err != nil {
			projectError(w, hub, err)
			return projects.Record{}, false
		}
		if hub.Profiles == nil {
			reasonJSON(w, 409, "profiles_unavailable", "Execution profiles are not available in this session.")
			return projects.Record{}, false
		}
		return p, true
	}
	pid := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id := r.PathValue("pid")
		if !profiles.ValidID(id) {
			reasonJSON(w, 404, "profile_not_found", "This execution profile no longer exists. Reload the profile list.")
			return "", false
		}
		return id, true
	}
	mux.HandleFunc("GET /api/v1/projects/{id}/execution-profiles", func(w http.ResponseWriter, r *http.Request) {
		p, ok := project(w, r)
		if !ok {
			return
		}
		list, def, err := hub.Profiles.List(p.ID)
		status, notice := hub.Profiles.Status()
		if errors.Is(err, profiles.ErrUnavailable) && status == profiles.Ready {
			status, notice = profiles.Unavailable, "The execution profile file changed on disk and can no longer be read. Changes are disabled until it is fixed and Hclaro restarts."
		}
		views := make([]profileView, 0, len(list))
		for _, profile := range list {
			views = append(views, viewOf(profile, def))
		}
		writeJSON(w, 200, map[string]any{"profiles": views, "defaultId": def, "status": status, "notice": notice})
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/execution-profiles", func(w http.ResponseWriter, r *http.Request) {
		var body profileBody
		if !decodeBody(w, r, &body) {
			return
		}
		p, ok := project(w, r)
		if !ok || !checkVarFiles(w, p.RootPath, body.VarFiles) {
			return
		}
		created, err := hub.Profiles.Create(p.ID, body.input())
		if err != nil {
			profileError(w, hub, err)
			return
		}
		writeJSON(w, 201, viewOf(created, ""))
	})
	mux.HandleFunc("PATCH /api/v1/projects/{id}/execution-profiles/{pid}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			profileBody
			ExpectedRevision int `json:"expectedRevision"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		p, ok := project(w, r)
		if !ok {
			return
		}
		id, ok := pid(w, r)
		if !ok || !checkVarFiles(w, p.RootPath, body.VarFiles) {
			return
		}
		if err := hub.ProfileBusy(p.ID, id); err != nil {
			profileError(w, hub, err)
			return
		}
		updated, err := hub.Profiles.Update(p.ID, id, body.ExpectedRevision, body.input())
		if err != nil {
			profileError(w, hub, err)
			return
		}
		hub.ProfileChanged(p.ID, id, "profile_changed")
		_, def, _ := hub.Profiles.List(p.ID)
		writeJSON(w, 200, viewOf(updated, def))
	})
	mux.HandleFunc("DELETE /api/v1/projects/{id}/execution-profiles/{pid}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedRevision int `json:"expectedRevision"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		p, ok := project(w, r)
		if !ok {
			return
		}
		id, ok := pid(w, r)
		if !ok {
			return
		}
		if err := hub.ProfileBusy(p.ID, id); err != nil {
			profileError(w, hub, err)
			return
		}
		if err := hub.Profiles.Delete(p.ID, id, body.ExpectedRevision); err != nil {
			profileError(w, hub, err)
			return
		}
		hub.ProfileChanged(p.ID, id, "profile_removed")
		writeJSON(w, 200, map[string]bool{"removed": true})
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/execution-profiles/{pid}/clone", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		p, ok := project(w, r)
		if !ok {
			return
		}
		id, ok := pid(w, r)
		if !ok {
			return
		}
		created, err := hub.Profiles.Clone(p.ID, id, body.Name)
		if err != nil {
			profileError(w, hub, err)
			return
		}
		writeJSON(w, 201, viewOf(created, ""))
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/execution-profiles/default", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProfileID string `json:"profileId"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		p, ok := project(w, r)
		if !ok {
			return
		}
		if !validProfileParam(body.ProfileID) {
			profileError(w, hub, profiles.ErrNotFound)
			return
		}
		if err := hub.Profiles.SetDefault(p.ID, body.ProfileID); err != nil {
			profileError(w, hub, err)
			return
		}
		writeJSON(w, 200, map[string]string{"defaultId": body.ProfileID})
	})
	// Variable files a profile can approve: names, sizes and modification times only.
	mux.HandleFunc("GET /api/v1/projects/{id}/var-files", func(w http.ResponseWriter, r *http.Request) {
		p, ok := project(w, r)
		if !ok {
			return
		}
		summary := hub.Summarize(p)
		if summary.Status != projects.StatusReady {
			reasonJSON(w, 409, "project_unavailable", summary.Problem)
			return
		}
		files, truncated := profiles.Candidates(p.RootPath)
		writeJSON(w, 200, map[string]any{"files": files, "truncated": truncated})
	})
	// The execution context summary shown before an operation. It runs no command: the
	// engine is looked up on PATH only, and the version comes from an earlier inspection.
	mux.HandleFunc("GET /api/v1/projects/{id}/execution-context", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		profileID, tab := q.Get("profileId"), q.Get("workspace")
		if !validProfileParam(profileID) || tab != "" && !engine.ValidWorkspace(tab) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		resolved, p, err := hub.Resolve(r.PathValue("id"), profileID)
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, contextSummary(hub, p, resolved, tab))
	})
}

// executionSummary describes everything an operation in this context would use, without
// values. Identity is always "not_verified".
type executionSummary struct {
	ProjectID     string `json:"projectId"`
	Alias         string `json:"alias"`
	RootPath      string `json:"rootPath"`
	Status        string `json:"status"`
	Backend       string `json:"backend"`
	ExecutionMode string `json:"executionMode"`
	Engine        string `json:"engine"`
	EngineFound   bool   `json:"engineFound"`
	EngineVersion string `json:"engineVersion,omitempty"`
	Workspace     string `json:"workspace,omitempty"`
	// WorkspaceSource is "profile", "tab", "launch" or "cli_default".
	WorkspaceSource string            `json:"workspaceSource"`
	DataDir         string            `json:"dataDir"`
	Context         profiles.Resolved `json:"context"`
	Issues          []profiles.Issue  `json:"issues"`
}

func contextSummary(hub *projects.Hub, p projects.Record, r profiles.Resolved, tab string) executionSummary {
	summary := hub.Summarize(p)
	s := executionSummary{ProjectID: p.ID, Alias: p.Alias, RootPath: p.RootPath, Status: summary.Status, Backend: summary.Backend, ExecutionMode: summary.ExecutionMode, DataDir: projects.Env().DataDir, Context: r, Issues: append([]profiles.Issue{}, r.Issues...)}
	s.Engine = r.Engine
	if s.Engine == "" {
		s.Engine = p.EnginePreference
	}
	if s.Engine == "" {
		for _, name := range []string{"terraform", "tofu"} {
			if _, err := exec.LookPath(name); err == nil {
				s.Engine = name
				break
			}
		}
	}
	if s.Engine != "" {
		_, err := exec.LookPath(s.Engine)
		s.EngineFound = err == nil
	}
	if in := summary.Inspection; in != nil && in.Engine == s.Engine {
		s.EngineVersion = in.Version
	}
	switch {
	case r.Workspace != "":
		s.Workspace, s.WorkspaceSource = r.Workspace, "profile"
		if tab != "" && tab != r.Workspace {
			s.Issues = append(s.Issues, profiles.Issue{Code: "profile_workspace", Message: "Profile " + r.ProfileName + " selects workspace " + r.Workspace + ", but this tab reads " + tab + ". A Terraform workspace is a state namespace, not a cloud account or AWS profile."})
		}
	case tab != "":
		s.Workspace, s.WorkspaceSource = tab, "tab"
	case summary.Inspection != nil && summary.Inspection.CLIDefault != "":
		s.Workspace, s.WorkspaceSource = summary.Inspection.CLIDefault, "cli_default"
	default:
		s.WorkspaceSource = "cli_default"
	}
	return s
}
