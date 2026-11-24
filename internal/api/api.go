package api

import (
	"cmp"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/assist"
	"hclaro/internal/cost"
	"hclaro/internal/discovery"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/security"
	"hclaro/internal/state"
	"hclaro/internal/workspace"
)

// withDeadline bounds the request context by d and moves the response write deadline past
// it, so a handler that runs that long is not cut off by the server's WriteTimeout. A later
// call replaces the deadline, so handlers with several bounded steps call it per step.
func withDeadline(w http.ResponseWriter, r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d + 10*time.Second))
	return context.WithTimeout(r.Context(), d)
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
func errorJSON(w http.ResponseWriter, code int, name string) {
	writeJSON(w, code, map[string]string{"error": name})
}

// Services are the session services the API serves next to operations. A nil service
// disables its routes; routes read the fields on each request.
type Services struct {
	// Assist holds the session's AI switch and handoff packages (AI-01.03). It has no access
	// to plans, approvals or operations.
	Assist *assist.Session
	// Discovery lists existing AWS objects for the launch workspace on request (M16).
	Discovery *discovery.Service
	// Cost estimates the monthly cost impact of reviewed plans on request (M17).
	Cost *cost.Service
	// History is the operation history; its routes are served only with the Hub.
	History *history.Store
	// Bootstrap is the browser launch link's one-time nonce; nil disables the exchange route.
	Bootstrap *security.Bootstrap
}

// Handler serves the API and embedded UI. hub is nil when the Workspace Hub is disabled;
// deps may be nil.
func Handler(store *workspace.Store, manager *operations.Manager, hub *projects.Hub, deps *Services, assets fs.FS, host, token, csrf, version string) http.Handler {
	if deps == nil {
		deps = &Services{}
	}
	mux := http.NewServeMux()
	var inits *operations.Inits
	if hub != nil {
		inits = hub.Inits
	}
	planRoutes(mux, store, manager, inits, hub, csrf, version)
	importRoutes(mux, deps, store, manager, inits)
	driftRoutes(mux, store, manager, hub)
	graphRoutes(mux, store, manager, hub)
	gitRoutes(mux, store, manager, hub)
	discoveryRoutes(mux, deps, manager, hub)
	costRoutes(mux, deps, store, manager)
	assistRoutes(mux, deps, store, manager, hub)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "version": version})
	})
	mux.HandleFunc("GET /api/v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, workspaceBody(store.Get(), nil))
	})
	mux.HandleFunc("GET /api/v1/resources", func(w http.ResponseWriter, r *http.Request) {
		listResources(w, r, store.Get())
	})
	mux.HandleFunc("GET /api/v1/resources/", func(w http.ResponseWriter, r *http.Request) {
		resourceDetail(w, store.Get(), strings.TrimPrefix(r.URL.Path, "/api/v1/resources/"))
	})
	if hub != nil {
		projectRoutes(mux, hub)
		profileRoutes(mux, hub)
		authRoutes(mux, hub)
		comparisonRoutes(mux, hub)
		if deps.History != nil {
			historyRoutes(mux, deps.History, manager, inits, version)
		}
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { errorJSON(w, 404, "route_not_found") })
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// No directory listings and no fallback HTML for unknown API or asset paths.
		path := strings.TrimPrefix(r.URL.Path, "/")
		// Hub client routes are the only other paths that serve the app shell.
		if hub != nil && clientRoute.MatchString(r.URL.Path) {
			http.ServeFileFS(w, r, assets, "index.html")
			return
		}
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
	return security.Guard(host, token, csrf, deps.Bootstrap, mux)
}

// clientRoute matches the Hub's client-side routes: /projects, /projects/<id>,
// /projects/<id>/workspaces/<name>/<view>, .../plans/<plan id>, /compare and /history. Segments
// are only routing hints; the server resolves IDs and names itself.
var clientRoute = regexp.MustCompile(`^(/projects(/[0-9a-f]{32}(/workspaces/[A-Za-z0-9_-]{1,90}(/(resources|overview|modules|providers|tools|git|import|discovery|drift|graph|operations|plans(/[0-9a-f]{32})?))?)?)?|/compare|/history)/?$`)

type workspaceResponse struct {
	workspace.Snapshot
	Managed   int               `json:"managed"`
	Data      int               `json:"data"`
	Modules   []state.Module    `json:"modules"`
	Providers map[string]int    `json:"providers"`
	Types     map[string]int    `json:"types"`
	Context   *projects.Context `json:"context,omitempty"`
}

func workspaceBody(snap workspace.Snapshot, c *projects.Context) workspaceResponse {
	return workspaceResponse{snap, snap.Index.Managed, snap.Index.Data, snap.Index.Modules, snap.Index.Providers, snap.Index.Types, c}
}

func listResources(w http.ResponseWriter, r *http.Request, snap workspace.Snapshot) {
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
		resource.KeyAttributes = state.Summary(resource.Type, resource.Attributes)
		resource.Attributes = nil
		matches = append(matches, resource)
	}
	groupKey := func(state.Resource) string { return "" }
	switch q.Get("group") {
	case "":
	case "type":
		groupKey = func(r state.Resource) string { return r.Type }
	case "module":
		groupKey = func(r state.Resource) string { return r.Module }
	default:
		errorJSON(w, 400, "invalid_group")
		return
	}
	var groups []resourceGroup
	if q.Get("group") != "" {
		// Order by group, then address, so a page never splits a group at random; counts cover every match.
		slices.SortStableFunc(matches, func(a, b state.Resource) int {
			return cmp.Or(cmp.Compare(groupKey(a), groupKey(b)), cmp.Compare(a.Address, b.Address))
		})
	}
	if q.Get("sort") == "desc" {
		slices.Reverse(matches)
	} else if q.Get("sort") != "" && q.Get("sort") != "asc" {
		errorJSON(w, 400, "invalid_sort")
		return
	}
	if q.Get("group") != "" {
		for _, m := range matches {
			if k := groupKey(m); len(groups) > 0 && groups[len(groups)-1].Key == k {
				groups[len(groups)-1].Count++
			} else {
				groups = append(groups, resourceGroup{k, 1})
			}
		}
	}
	total := len(matches)
	start := min(offset, total)
	end := min(start+limit, total)
	writeJSON(w, 200, resourcePage{matches[start:end], total, groups})
}

func resourceDetail(w http.ResponseWriter, snap workspace.Snapshot, id string) {
	if resource, ok := snap.Index.ByID[id]; ok {
		writeJSON(w, 200, resource)
		return
	}
	errorJSON(w, 404, "resource_not_found")
}

type resourcePage struct {
	Resources []state.Resource `json:"resources"`
	Total     int              `json:"total"`
	// Groups counts every match, not just this page, when the request names a group.
	Groups []resourceGroup `json:"groups,omitempty"`
}

type resourceGroup struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}
