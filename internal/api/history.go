package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"hclaro/internal/history"
	"hclaro/internal/operations"
)

var historyWorkspace = regexp.MustCompile(`^[A-Za-z0-9_-]{1,90}$`)

// historyFilter reads and validates the filter query parameters. Unknown parameters, such as
// a file path, are refused rather than ignored.
func historyFilter(w http.ResponseWriter, r *http.Request, allowed ...string) (history.Filter, bool) {
	q := r.URL.Query()
	known := append([]string{"projectId", "workspace", "kind", "status", "query"}, allowed...)
	for name := range q {
		if !slices.Contains(known, name) {
			errorJSON(w, 400, "invalid_request")
			return history.Filter{}, false
		}
	}
	f := history.Filter{ProjectID: q.Get("projectId"), Workspace: q.Get("workspace"), Kind: q.Get("kind"), Status: q.Get("status"), Query: q.Get("query")}
	switch {
	case f.ProjectID != "" && !history.ValidID(f.ProjectID),
		f.Workspace != "" && !historyWorkspace.MatchString(f.Workspace),
		f.Kind != "" && !slices.Contains(history.Kinds, f.Kind),
		f.Status != "" && !slices.Contains([]string{history.StatusRunning, history.StatusSucceeded, history.StatusFailed, history.StatusCancelled, history.StatusUnknown}, f.Status),
		len(f.Query) > 200 || strings.ContainsFunc(f.Query, unicode.IsControl):
		errorJSON(w, 400, "invalid_filter")
		return history.Filter{}, false
	}
	return f, true
}

type historyListItem struct {
	history.HistoryEntry
	// Live is true while this session still holds the operation's log.
	Live bool `json:"live"`
}

type historyProject struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Registered bool     `json:"registered"`
	Workspaces []string `json:"workspaces"`
}

func historyRoutes(mux *http.ServeMux, store *history.Store, manager *operations.Manager, inits *operations.Inits, version string) {
	live := func(id string) bool {
		if _, log, ok := manager.Operation(id); ok {
			return log != nil
		}
		if _, log, ok := inits.Operation(id); ok {
			return log != nil
		}
		return false
	}
	mux.HandleFunc("GET /api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		f, ok := historyFilter(w, r, "offset", "limit")
		if !ok {
			return
		}
		q := r.URL.Query()
		offset, limit := 0, 50
		var err error
		if q.Has("offset") {
			if offset, err = strconv.Atoi(q.Get("offset")); err != nil || offset < 0 || offset > history.MaxEntriesLimit {
				errorJSON(w, 400, "invalid_offset")
				return
			}
		}
		if q.Has("limit") {
			if limit, err = strconv.Atoi(q.Get("limit")); err != nil || limit < 1 || limit > 200 {
				errorJSON(w, 400, "invalid_limit")
				return
			}
		}
		all := store.List(history.Filter{})
		// Facets come from the whole history so the filters stay usable while filtered.
		projects := []historyProject{}
		for _, e := range all {
			i := slices.IndexFunc(projects, func(p historyProject) bool { return p.ID == e.ProjectID })
			if i < 0 {
				projects = append(projects, historyProject{ID: e.ProjectID, Name: e.ProjectName, Registered: e.Registered, Workspaces: []string{}})
				i = len(projects) - 1
			}
			if e.Workspace != "" && !slices.Contains(projects[i].Workspaces, e.Workspace) {
				projects[i].Workspaces = append(projects[i].Workspaces, e.Workspace)
			}
		}
		for i := range projects {
			slices.Sort(projects[i].Workspaces)
		}
		slices.SortStableFunc(projects, func(a, b historyProject) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		matches := store.List(f)
		start := min(offset, len(matches))
		end := min(start+limit, len(matches))
		items := make([]historyListItem, 0, end-start)
		for _, e := range matches[start:end] {
			// Lists carry no log lines; the detail does.
			e.Excerpt, e.Session = []history.Line{}, ""
			items = append(items, historyListItem{e, live(e.ID)})
		}
		status, notice, _ := store.Status()
		writeJSON(w, 200, struct {
			Status    string                         `json:"status"`
			Notice    string                         `json:"notice,omitempty"`
			Retention history.HistoryRetentionPolicy `json:"retention"`
			Total     int                            `json:"total"`
			Stored    int                            `json:"stored"`
			Entries   []historyListItem              `json:"entries"`
			Projects  []historyProject               `json:"projects"`
		}{status, notice, store.Retention(), len(matches), len(all), items, projects})
	})
	mux.HandleFunc("GET /api/v1/history/compare", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for name := range q {
			if name != "left" && name != "right" {
				errorJSON(w, 400, "invalid_request")
				return
			}
		}
		left, right := q.Get("left"), q.Get("right")
		if !history.ValidID(left) || !history.ValidID(right) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		a, okA := store.Get(left)
		b, okB := store.Get(right)
		if !okA || !okB {
			errorJSON(w, 404, "history_not_found")
			return
		}
		result, err := history.Compare(a, b)
		if history.IsIncompatible(err) {
			writeJSON(w, 409, map[string]string{"error": "plans_incompatible", "reason": err.Error()})
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/v1/history/export", func(w http.ResponseWriter, r *http.Request) {
		f, ok := historyFilter(w, r, "format")
		if !ok {
			return
		}
		format := r.URL.Query().Get("format")
		if format != "json" && format != "markdown" {
			errorJSON(w, 400, "invalid_format")
			return
		}
		now := time.Now().UTC()
		report := history.NewReport(store.List(f), f, version, now)
		name := "hclaro-history-" + now.Format("20060102-150405")
		if format == "json" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
			writeJSON(w, 200, report)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.md"`)
		_, _ = w.Write([]byte(report.Markdown()))
	})
	mux.HandleFunc("PATCH /api/v1/history/retention", func(w http.ResponseWriter, r *http.Request) {
		var body history.HistoryRetentionPolicy
		if !decodeBody(w, r, &body) {
			return
		}
		err := store.SetRetention(body)
		switch {
		case errors.Is(err, history.ErrUnavailable):
			writeJSON(w, 409, map[string]string{"error": "history_unavailable", "reason": "Operation history is read-only or unavailable in this session."})
		case err != nil && body.Validate() != nil:
			writeJSON(w, 400, map[string]string{"error": "invalid_retention", "reason": err.Error()})
		case err != nil:
			writeJSON(w, 500, map[string]string{"error": "history_write_failed", "reason": "Operation history could not be written."})
		default:
			writeJSON(w, 200, store.Retention())
		}
	})
	mux.HandleFunc("DELETE /api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Confirm   bool   `json:"confirm"`
			ProjectID string `json:"projectId"`
			Workspace string `json:"workspace"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if !body.Confirm || body.ProjectID != "" && !history.ValidID(body.ProjectID) || body.Workspace != "" && (!historyWorkspace.MatchString(body.Workspace) || body.ProjectID == "") {
			errorJSON(w, 400, "invalid_request")
			return
		}
		removed, err := store.Clear(history.Filter{ProjectID: body.ProjectID, Workspace: body.Workspace})
		if historyWriteError(w, err) {
			return
		}
		writeJSON(w, 200, map[string]int{"removed": removed})
	})
	mux.HandleFunc("GET /api/v1/history/{id}", func(w http.ResponseWriter, r *http.Request) {
		e, ok := store.Get(r.PathValue("id"))
		if !ok {
			errorJSON(w, 404, "history_not_found")
			return
		}
		e.Session = ""
		writeJSON(w, 200, historyListItem{e, live(e.ID)})
	})
	mux.HandleFunc("DELETE /api/v1/history/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if !history.ValidID(r.PathValue("id")) {
			errorJSON(w, 404, "history_not_found")
			return
		}
		if historyWriteError(w, store.Delete(r.PathValue("id"))) {
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
}

func historyWriteError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, history.ErrNotFound):
		errorJSON(w, 404, "history_not_found")
	case errors.Is(err, history.ErrRunning):
		writeJSON(w, 409, map[string]string{"error": "operation_running", "reason": "This operation is still running. Its entry can be deleted once it ends."})
	case errors.Is(err, history.ErrUnavailable):
		writeJSON(w, 409, map[string]string{"error": "history_unavailable", "reason": "Operation history is read-only or unavailable in this session."})
	default:
		writeJSON(w, 500, map[string]string{"error": "history_write_failed", "reason": "Operation history could not be written."})
	}
	return true
}
