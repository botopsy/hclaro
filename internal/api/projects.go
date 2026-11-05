package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// inspectTimeout bounds one explicit project inspection (version and workspace list).
const inspectTimeout = 2 * time.Minute

func reasonJSON(w http.ResponseWriter, code int, name, reason string) {
	writeJSON(w, code, map[string]string{"error": name, "reason": reason})
}

// projectError maps Hub errors to typed responses. Messages never include engine output.
func projectError(w http.ResponseWriter, hub *projects.Hub, err error) {
	var path *projects.PathError
	switch {
	case errors.As(err, &path):
		reasonJSON(w, 400, "invalid_path", path.Message)
	case errors.Is(err, projects.ErrRegistryUnavailable):
		_, notice, _ := hub.Registry.Status()
		if notice == "" {
			notice = "The project registry changed on disk and can no longer be read. Restart Hclaro after checking the file."
		}
		reasonJSON(w, 409, "registry_unavailable", notice)
	case errors.Is(err, projects.ErrNotFound):
		reasonJSON(w, 404, "project_not_found", "This project is no longer registered.")
	case errors.Is(err, projects.ErrInvalidAlias):
		reasonJSON(w, 400, "invalid_alias", "Use 1–80 characters without control characters or surrounding spaces.")
	case errors.Is(err, projects.ErrInvalidEngine):
		reasonJSON(w, 400, "invalid_engine", "Choose terraform, tofu or automatic detection.")
	case errors.Is(err, projects.ErrInvalidOrder):
		reasonJSON(w, 400, "invalid_order", "The project list changed. Reload the Hub and try again.")
	case errors.Is(err, projects.ErrTooMany):
		reasonJSON(w, 409, "too_many_projects", "The Hub holds at most 200 projects.")
	case errors.Is(err, projects.ErrSharedDataDir):
		reasonJSON(w, 409, "shared_data_dir", "TF_DATA_DIR is an absolute path, so every project would share one data directory and backend initialization. Restart Hclaro without it, or with a relative TF_DATA_DIR.")
	case errors.Is(err, projects.ErrBusy):
		reasonJSON(w, 409, "project_busy", "Initialization or another operation is running in this project. Wait for it to finish, then try again.")
	case errors.Is(err, projects.ErrInvalidWorkspace):
		reasonJSON(w, 400, "invalid_workspace", "Use 1–90 letters, digits, - or _, starting with a letter or digit.")
	case errors.Is(err, projects.ErrInvalidAction):
		reasonJSON(w, 400, "invalid_action", "Choose new, select or delete.")
	default:
		reasonJSON(w, 500, "registry_write_failed", "The project registry could not be saved. Check the configuration directory permissions.")
	}
}

func projectRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		list, err := hub.List()
		status, notice, path := hub.Registry.Status()
		if errors.Is(err, projects.ErrRegistryUnavailable) && status == projects.RegistryReady {
			status, notice = projects.RegistryUnavailable, "The project registry changed on disk and can no longer be read. Changes are disabled until it is fixed and Hclaro restarts."
		}
		writeJSON(w, 200, struct {
			Projects    []projects.Summary   `json:"projects"`
			Registry    map[string]string    `json:"registry"`
			Launch      string               `json:"launchRoot,omitempty"`
			Environment projects.Environment `json:"environment"`
		}{list, map[string]string{"status": status, "notice": notice, "path": path}, hub.Launch, projects.Env()})
	})
	mux.HandleFunc("POST /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path   string `json:"path"`
			Alias  string `json:"alias"`
			Engine string `json:"engine"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		summary, err := hub.Add(body.Path, body.Alias, body.Engine)
		if errors.Is(err, projects.ErrDuplicate) {
			writeJSON(w, 409, map[string]string{"error": "duplicate_project", "reason": "This folder is already registered.", "existingId": summary.ID})
			return
		}
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 201, summary)
	})
	mux.HandleFunc("PATCH /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Alias  *string `json:"alias"`
			Pinned *bool   `json:"pinned"`
			Engine *string `json:"engine"`
			// RootPath repairs a moved project; it is validated like a new one.
			RootPath *string `json:"rootPath"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		summary, err := hub.Update(r.PathValue("id"), projects.Change{Alias: body.Alias, Pinned: body.Pinned, EnginePreference: body.Engine, RootPath: body.RootPath})
		if errors.Is(err, projects.ErrDuplicate) {
			reasonJSON(w, 409, "duplicate_project", "Another registered project already uses this folder.")
			return
		}
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, summary)
	})
	mux.HandleFunc("DELETE /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := hub.Remove(r.PathValue("id")); err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"removed": true})
	})
	mux.HandleFunc("POST /api/v1/projects/order", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := hub.Reorder(body.IDs); err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ordered": true})
	})
	// Inspect runs the engine's version and workspace list reads. It is a POST because it
	// executes commands; GET routes never do.
	mux.HandleFunc("POST /api/v1/projects/{id}/inspect", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		// The engine reads can outlast the server's default write timeout.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(inspectTimeout + 10*time.Second))
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		summary, err := hub.Inspect(ctx, r.PathValue("id"))
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, summary)
	})
	// Browse lists one folder's subdirectory names for the folder chooser. The path is in
	// the body so it never appears in a URL.
	mux.HandleFunc("POST /api/v1/projects/browse", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		listing, err := projects.Browse(body.Path)
		if err != nil {
			reasonJSON(w, 400, "browse_failed", "This folder cannot be listed. Check that it exists and is readable.")
			return
		}
		writeJSON(w, 200, listing)
	})
	// Opening a context starts a read-only state read for one project workspace.
	mux.HandleFunc("POST /api/v1/contexts", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectID string `json:"projectId"`
			Workspace string `json:"workspace"`
			Reload    bool   `json:"reload"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		c, reused, err := hub.Open(body.ProjectID, body.Workspace, body.Reload)
		var path *projects.PathError
		if errors.As(err, &path) {
			reasonJSON(w, 409, "project_unavailable", path.Message)
			return
		}
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, struct {
			*projects.Context
			Reused bool `json:"reused"`
		}{c, reused})
	})
	contextOf := func(w http.ResponseWriter, r *http.Request) *projects.Context {
		c, reason, err := hub.Context(r.PathValue("cid"))
		if err != nil {
			message := map[string]string{
				"project_removed":   "This project was removed from the Hub.",
				"project_changed":   "This project's folder or engine changed. Open the workspace again.",
				"reloaded":          "A newer read of this workspace replaced this one.",
				"evicted":           "This snapshot was released to save memory. Open the workspace again.",
				"initialized":       "This project was initialized after this snapshot was read. Open the workspace again.",
				"workspace_deleted": "This workspace was deleted from the project.",
			}[reason]
			if message == "" {
				message = "This workspace view is no longer open. Open the workspace again."
			}
			reasonJSON(w, 404, "context_closed", message)
			return nil
		}
		return c
	}
	initRoutes(mux, hub)
	workspaceRoutes(mux, hub)
	toolRoutes(mux, hub)
	mux.HandleFunc("GET /api/v1/contexts/{cid}", func(w http.ResponseWriter, r *http.Request) {
		if c := contextOf(w, r); c != nil {
			writeJSON(w, 200, workspaceBody(c.Snapshot(), c))
		}
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/resources", func(w http.ResponseWriter, r *http.Request) {
		if c := contextOf(w, r); c != nil {
			listResources(w, r, c.Snapshot())
		}
	})
	mux.HandleFunc("GET /api/v1/contexts/{cid}/resources/{rid}", func(w http.ResponseWriter, r *http.Request) {
		if c := contextOf(w, r); c != nil {
			resourceDetail(w, c.Snapshot(), r.PathValue("rid"))
		}
	})
}

var contextIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// initRoutes serve the read-only init preflight and the explicit, CSRF-protected start of a
// standard init. The server builds the argv; the request only names the reviewed context.
func initRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/projects/{id}/init-status", func(w http.ResponseWriter, r *http.Request) {
		status, err := hub.InitStatus(r.PathValue("id"))
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, status)
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/operations/init", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode              string `json:"mode"`
			ExpectedContextID string `json:"expectedContextId"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		// Only the standard mode exists: no upgrade, migration, reconfiguration or backend values.
		if body.Mode != "standard" {
			reasonJSON(w, 400, "invalid_mode", "Only the standard init is available.")
			return
		}
		if !contextIDPattern.MatchString(body.ExpectedContextID) {
			reasonJSON(w, 400, "invalid_context", "Review the project before initializing it.")
			return
		}
		op, err := hub.StartInit(r.PathValue("id"), body.ExpectedContextID)
		var refusal *projects.InitRefusal
		switch {
		case errors.As(err, &refusal):
			reasonJSON(w, 409, refusal.Code, refusal.Message)
		case errors.Is(err, projects.ErrContextChanged):
			reasonJSON(w, 409, "context_changed", "The project, its engine or the environment changed since you reviewed it. Nothing was run. Review it again.")
		case errors.Is(err, operations.ErrClosed):
			reasonJSON(w, 503, "shutting_down", "Hclaro is shutting down.")
		case err != nil:
			projectError(w, hub, err)
		default:
			writeJSON(w, 202, op)
		}
	})
}

// workspaceRoutes serve the review of a workspace command, which runs read-only engine
// commands, and its explicit, CSRF-protected start. The server builds the argv from the
// action and a validated name; the start must name an unexpired, unused review.
func workspaceRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("POST /api/v1/projects/{id}/workspaces/review", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action engine.WorkspaceAction `json:"action"`
			Name   string                 `json:"name"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(inspectTimeout + 10*time.Second))
		ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
		defer cancel()
		review, err := hub.ReviewWorkspace(ctx, r.PathValue("id"), body.Action, body.Name)
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, review)
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/operations/workspace", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action            engine.WorkspaceAction `json:"action"`
			Name              string                 `json:"name"`
			ExpectedContextID string                 `json:"expectedContextId"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if !engine.ValidAction(body.Action) {
			projectError(w, hub, projects.ErrInvalidAction)
			return
		}
		if !engine.ValidArgWorkspace(body.Name) {
			projectError(w, hub, projects.ErrInvalidWorkspace)
			return
		}
		if !contextIDPattern.MatchString(body.ExpectedContextID) {
			reasonJSON(w, 400, "invalid_context", "Review the workspace command before running it.")
			return
		}
		op, err := hub.StartWorkspace(r.PathValue("id"), body.Action, body.Name, body.ExpectedContextID)
		switch {
		case errors.Is(err, projects.ErrContextChanged):
			reasonJSON(w, 409, "context_changed", "This review expired, was already used, or the project, its engine or the environment changed. Nothing was run. Review it again.")
		case errors.Is(err, operations.ErrClosed):
			reasonJSON(w, 503, "shutting_down", "Hclaro is shutting down.")
		case err != nil:
			projectError(w, hub, err)
		default:
			writeJSON(w, 202, op)
		}
	})
}

var hubIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// toolRoutes serve the read-only project utilities and the project's operation history. The
// server builds the argv from the tool name; the request names only an open workspace
// context and, for fmt -check, whether the check is recursive.
func toolRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/projects/{id}/operations", func(w http.ResponseWriter, r *http.Request) {
		ops, err := hub.Operations(r.PathValue("id"))
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, map[string]any{"operations": ops})
	})
	for _, tool := range []engine.Tool{engine.ToolValidate, engine.ToolFormat, engine.ToolProviders, engine.ToolOutputs} {
		mux.HandleFunc("POST /api/v1/projects/{id}/operations/"+string(tool), func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				ContextID string `json:"contextId"`
				Recursive *bool  `json:"recursive"`
			}
			if !decodeBody(w, r, &body) {
				return
			}
			// Only fmt -check has an option; every other flag is fixed server-side.
			if body.Recursive != nil && tool != engine.ToolFormat {
				errorJSON(w, 400, "invalid_request")
				return
			}
			if !hubIDPattern.MatchString(body.ContextID) {
				reasonJSON(w, 400, "invalid_context", "Open a workspace of this project, then run the tool from it.")
				return
			}
			op, err := hub.StartTool(r.PathValue("id"), body.ContextID, tool, body.Recursive != nil && *body.Recursive)
			var refusal *projects.ToolRefusal
			switch {
			case errors.As(err, &refusal):
				reasonJSON(w, 409, refusal.Code, refusal.Message)
			case errors.Is(err, projects.ErrContextNotFound):
				reasonJSON(w, 404, "context_closed", "This workspace view is no longer open, or belongs to another project. Open the workspace again.")
			case errors.Is(err, projects.ErrContextChanged):
				reasonJSON(w, 409, "context_changed", "The project's engine or folder changed after this workspace was opened. Nothing was run. Open the workspace again.")
			case errors.Is(err, operations.ErrToolRunning):
				reasonJSON(w, 409, "tool_running", "This tool is already running for this workspace. Wait for it to finish or cancel it.")
			case errors.Is(err, operations.ErrToolsBusy):
				reasonJSON(w, 409, "tools_busy", "Too many project tools are running. Wait for some to finish.")
			case errors.Is(err, operations.ErrClosed):
				reasonJSON(w, 503, "shutting_down", "Hclaro is shutting down.")
			case err != nil:
				projectError(w, hub, err)
			default:
				writeJSON(w, 202, op)
			}
		})
	}
}
