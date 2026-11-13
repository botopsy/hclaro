package api

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"hclaro/internal/gitctx"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// gitTimeout bounds one Git status or diff request.
const gitTimeout = 20 * time.Second

var fileIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// gitRoutes serve the Git context of the launch project and of Hub projects. The browser
// names a project (the launch project or a Hub ID) and an opaque file ID from the latest
// status; it never sends a path, Git argument, editor executable or URL. Reading HEAD and
// locating a resource's source read files only (GET); status and diff run read-only Git
// commands and opening an editor hands a validated URL to the OS, so they are CSRF-protected
// POSTs.
func gitRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, hub *projects.Hub) {
	// launch resolves the launch project's root, or answers why there is none.
	launch := func(w http.ResponseWriter) (*gitctx.Service, string, bool) {
		switch {
		case manager.Git == nil:
			reasonJSON(w, 409, "git_unavailable", "Git context is not available in this session.")
		case manager.Root() == "":
			reasonJSON(w, 409, "no_launch_project", "No project was opened from the terminal.")
		default:
			return manager.Git, manager.Root(), true
		}
		return nil, "", false
	}
	// project resolves a ready Hub project's canonical root.
	project := func(w http.ResponseWriter, r *http.Request) (*gitctx.Service, string, bool) {
		if hub == nil || hub.Git == nil {
			reasonJSON(w, 409, "git_unavailable", "Git context is not available in this session.")
			return nil, "", false
		}
		summary, err := hub.Summary(r.PathValue("id"))
		if err != nil {
			projectError(w, hub, err)
			return nil, "", false
		}
		if summary.Status != projects.StatusReady {
			reasonJSON(w, 409, "project_unavailable", "The project folder is "+summary.Status+"; repair it in the Hub first.")
			return nil, "", false
		}
		return hub.Git, summary.RootPath, true
	}
	type resolver func(w http.ResponseWriter, r *http.Request) (*gitctx.Service, string, bool)
	both := map[string]resolver{
		"/api/v1":               func(w http.ResponseWriter, _ *http.Request) (*gitctx.Service, string, bool) { return launch(w) },
		"/api/v1/projects/{id}": project,
	}
	for prefix, resolve := range both {
		mux.HandleFunc("GET "+prefix+"/git/head", func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.Query()) > 0 {
				errorJSON(w, 400, "invalid_request")
				return
			}
			if s, root, ok := resolve(w, r); ok {
				writeJSON(w, 200, s.HeadOf(root))
			}
		})
		mux.HandleFunc("POST "+prefix+"/git/status", func(w http.ResponseWriter, r *http.Request) {
			var body struct{}
			if !decodeBody(w, r, &body) {
				return
			}
			if s, root, ok := resolve(w, r); ok {
				ctx, cancel := withDeadline(w, r, gitTimeout)
				defer cancel()
				writeJSON(w, 200, s.Status(ctx, root))
			}
		})
		mux.HandleFunc("POST "+prefix+"/git/diff", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				FileID string `json:"fileId"`
			}
			if !decodeBody(w, r, &body) {
				return
			}
			if !fileIDPattern.MatchString(body.FileID) {
				reasonJSON(w, 400, "invalid_file", "Choose a changed file from the list.")
				return
			}
			s, root, ok := resolve(w, r)
			if !ok {
				return
			}
			ctx, cancel := withDeadline(w, r, gitTimeout)
			defer cancel()
			diff, err := s.Diff(ctx, root, body.FileID)
			if err != nil {
				gitError(w, err)
				return
			}
			writeJSON(w, 200, diff)
		})
		mux.HandleFunc("POST "+prefix+"/editor/open", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				FileID string `json:"fileId"`
				Line   int    `json:"line"`
				Editor string `json:"editor"`
			}
			if !decodeBody(w, r, &body) {
				return
			}
			if !fileIDPattern.MatchString(body.FileID) {
				reasonJSON(w, 400, "invalid_file", "Choose a file Hclaro listed.")
				return
			}
			if _, ok := gitctx.Editors[body.Editor]; !ok {
				reasonJSON(w, 400, "invalid_editor", "Choose VS Code or Cursor.")
				return
			}
			if body.Line < 1 || body.Line > 10_000_000 {
				reasonJSON(w, 400, "invalid_line", "The line is outside the file.")
				return
			}
			s, root, ok := resolve(w, r)
			if !ok {
				return
			}
			result, err := s.OpenEditor(r.Context(), root, body.FileID, body.Editor, body.Line)
			if err != nil {
				gitError(w, err)
				return
			}
			writeJSON(w, 200, result)
		})
	}
	// Source references locate a resource's configuration block from the redacted snapshot
	// (mode, module, type, name). They read IaC files only and run no command.
	mux.HandleFunc("GET /api/v1/resources/{id}/source", func(w http.ResponseWriter, r *http.Request) {
		s, root, ok := launch(w)
		if !ok {
			return
		}
		sourceReference(w, s, root, store.Get(), r.PathValue("id"))
	})
	if hub != nil {
		mux.HandleFunc("GET /api/v1/contexts/{cid}/resources/{rid}/source", func(w http.ResponseWriter, r *http.Request) {
			c, _, err := hub.Context(r.PathValue("cid"))
			if err != nil || c == nil {
				reasonJSON(w, 404, "context_closed", "This workspace view is no longer open. Open the workspace again.")
				return
			}
			if hub.Git == nil {
				reasonJSON(w, 409, "git_unavailable", "Git context is not available in this session.")
				return
			}
			sourceReference(w, hub.Git, c.RootPath, c.Snapshot(), r.PathValue("rid"))
		})
	}
}

func sourceReference(w http.ResponseWriter, s *gitctx.Service, root string, snap workspace.Snapshot, id string) {
	resource, ok := snap.Index.ByID[id]
	if !ok {
		errorJSON(w, 404, "resource_not_found")
		return
	}
	writeJSON(w, 200, s.Locate(root, resource.Mode, resource.Module, resource.Type, resource.Name))
}

// gitError maps gitctx errors to typed responses with safe messages.
func gitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitctx.ErrUnknownFile), errors.Is(err, gitctx.ErrNotFound):
		reasonJSON(w, 404, "file_not_found", "This file is no longer in the project scope or the list of changed files. Refresh and try again.")
	case errors.Is(err, gitctx.ErrNotDiffable):
		reasonJSON(w, 409, "not_diffable", "Only .tf and .tf.json configuration files are diffed. Variable files can hold secrets and are never shown.")
	case errors.Is(err, gitctx.ErrOutsideScope):
		reasonJSON(w, 403, "outside_scope", "The file resolves outside the project and its local modules (for example through a symbolic link). It is not read.")
	case errors.Is(err, gitctx.ErrMalformed):
		reasonJSON(w, 502, "git_output_invalid", "Git returned output Hclaro could not read safely. Nothing is shown.")
	case errors.Is(err, gitctx.ErrLine):
		reasonJSON(w, 400, "invalid_line", "The line is outside the file.")
	case errors.Is(err, gitctx.ErrEditor):
		reasonJSON(w, 400, "invalid_editor", "Choose VS Code or Cursor.")
	case errors.Is(err, gitctx.ErrOpenFailed):
		reasonJSON(w, 502, "editor_unavailable", "The operating system could not hand the link to the editor. Check that it is installed and registered for its links.")
	default:
		reasonJSON(w, 502, "git_failed", "Git could not produce a diff for this file.")
	}
}
