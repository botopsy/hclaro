package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"hclaro/internal/operations"
	"hclaro/internal/security"
	"hclaro/internal/state"
	"hclaro/internal/workspace"
)

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
func errorJSON(w http.ResponseWriter, code int, name string) {
	writeJSON(w, code, map[string]string{"error": name})
}

func Handler(store *workspace.Store, manager *operations.Manager, assets fs.FS, host, token, csrf, version string) http.Handler {
	mux := http.NewServeMux()
	planRoutes(mux, store, manager, csrf, version)
	importRoutes(mux, store, manager)
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "version": version})
	})
	mux.HandleFunc("/api/v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		snap := store.Get()
		writeJSON(w, 200, struct {
			workspace.Snapshot
			Managed   int            `json:"managed"`
			Data      int            `json:"data"`
			Modules   []state.Module `json:"modules"`
			Providers map[string]int `json:"providers"`
			Types     map[string]int `json:"types"`
		}{snap, snap.Index.Managed, snap.Index.Data, snap.Index.Modules, snap.Index.Providers, snap.Index.Types})
	})
	mux.HandleFunc("/api/v1/resources", func(w http.ResponseWriter, r *http.Request) {
		snap := store.Get()
		if snap.Status != "ready" && snap.Status != "empty" {
			errorJSON(w, 503, "snapshot_unavailable")
			return
		}
		q := r.URL.Query()
		if mode := q.Get("mode"); mode != "" && mode != "managed" && mode != "data" {
			errorJSON(w, 400, "invalid_mode")
			return
		}
		offset, limit := 0, 50
		var err error
		if q.Has("offset") {
			offset, err = strconv.Atoi(q.Get("offset"))
			if err != nil || offset < 0 || offset > len(snap.Index.Resources) {
				errorJSON(w, 400, "invalid_offset")
				return
			}
		}
		if q.Has("limit") {
			limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil || limit < 1 || limit > 200 {
				errorJSON(w, 400, "invalid_limit")
				return
			}
		}
		query := strings.ToLower(q.Get("query"))
		matches := []state.Resource{}
		for _, resource := range snap.Index.Resources {
			if q.Has("module") && resource.Module != q.Get("module") || q.Get("provider") != "" && resource.Provider != q.Get("provider") || q.Get("type") != "" && resource.Type != q.Get("type") || q.Get("mode") != "" && resource.Mode != q.Get("mode") {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(resource.Address+" "+resource.Provider+" "+resource.Type+" "+resource.Module+" "+resource.Name), query) {
				continue
			}
			resource.Attributes = nil
			matches = append(matches, resource)
		}
		if q.Get("sort") == "desc" {
			for l, h := 0, len(matches)-1; l < h; l, h = l+1, h-1 {
				matches[l], matches[h] = matches[h], matches[l]
			}
		} else if q.Get("sort") != "" && q.Get("sort") != "asc" {
			errorJSON(w, 400, "invalid_sort")
			return
		}
		total := len(matches)
		start := min(offset, total)
		end := min(start+limit, total)
		writeJSON(w, 200, struct {
			Resources []state.Resource `json:"resources"`
			Total     int              `json:"total"`
		}{matches[start:end], total})
	})
	mux.HandleFunc("/api/v1/resources/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/resources/")
		if resource, ok := store.Get().Index.ByID[id]; ok {
			writeJSON(w, 200, resource)
			return
		}
		errorJSON(w, 404, "resource_not_found")
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { errorJSON(w, 404, "route_not_found") })
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// No directory listings and no fallback HTML for unknown API or asset paths.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		fi, err := fs.Stat(assets, path)
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	return security.Guard(host, token, csrf, mux)
}
