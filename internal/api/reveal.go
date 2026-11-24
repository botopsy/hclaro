package api

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/projects"
	"hclaro/internal/state"
)

// revealLimit bounds reveals per minute, so a script running in the page cannot dump the state.
const revealLimit = 10

// revealRoutes serves M30: one server-redacted scalar, on explicit request. The value is
// returned once with no-store and is never logged, cached, stored or given to AI.
func revealRoutes(mux *http.ServeMux, hub *projects.Hub) {
	var mu sync.Mutex
	var recent []time.Time
	allow := func() bool {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		for len(recent) > 0 && now.Sub(recent[0]) >= time.Minute {
			recent = recent[1:]
		}
		if len(recent) >= revealLimit {
			return false
		}
		recent = append(recent, now)
		return true
	}
	mux.HandleFunc("POST /api/v1/contexts/{cid}/resources/{rid}/reveal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		c := openContext(w, r, hub)
		if c == nil {
			return
		}
		var body struct {
			Path []any `json:"path"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		resource, ok := c.Snapshot().Index.ByID[r.PathValue("rid")]
		if !ok {
			errorJSON(w, 404, "resource_not_found")
			return
		}
		if !allow() {
			reasonJSON(w, 429, "reveal_rate_limited", "Too many reveals in the last minute. Wait a moment, then try again.")
			return
		}
		raw, err := c.ReadState(r.Context())
		switch {
		case errors.Is(err, projects.ErrBusy):
			reasonJSON(w, 409, "project_busy", "Another operation is running in this project. Wait for it to finish, then try again.")
			return
		case errors.Is(err, engine.ErrReadStateLocked):
			reasonJSON(w, 409, "state_locked", engine.ReadAdvice(err, ""))
			return
		case errors.Is(err, engine.ErrReadCredentials):
			reasonJSON(w, 403, "reveal_denied", "Your credentials can't read this value. The backend denied access. It stays hidden.")
			return
		case errors.Is(err, engine.ErrReadNetwork):
			reasonJSON(w, 502, "reveal_unreachable", engine.ReadAdvice(err, ""))
			return
		case err != nil:
			reasonJSON(w, 502, "reveal_failed", "The state could not be read again. It stays hidden.")
			return
		}
		value, err := state.Reveal(raw, resource.Address, body.Path)
		switch {
		case err == nil:
			writeJSON(w, 200, map[string]any{"value": value})
		case errors.Is(err, state.ErrRevealNotFound):
			reasonJSON(w, 404, "path_not_found", "That value is no longer in the state. Read the workspace again.")
		case errors.Is(err, state.ErrRevealNotHidden):
			reasonJSON(w, 400, "not_redacted", "This value is not hidden, so there is nothing to reveal.")
		case errors.Is(err, state.ErrRevealNotScalar):
			reasonJSON(w, 400, "not_scalar", "Only a single value can be revealed. Reveal its entries one at a time.")
		case errors.Is(err, state.ErrRevealTooLong):
			reasonJSON(w, 400, "too_long", "This value is too long to reveal.")
		default:
			reasonJSON(w, 502, "reveal_failed", "The state could not be read again. It stays hidden.")
		}
	})
}
