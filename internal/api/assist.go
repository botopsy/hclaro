package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"

	"hclaro/internal/assist"
	"hclaro/internal/drift"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// AI handoff routes (AI-01.03). The server builds a sanitized context package from its own
// records (a plan, drift scan, import preview or resource of the launch project or of one
// open Hub workspace) and keeps it in memory; the browser only names the record. Export
// returns exactly the previewed bytes, by hash. Building and exporting send nothing off the
// device: only the user's Copy or Download hands the text over. Every package route is a
// CSRF-protected POST, and every route answers ai_disabled while AI is off.

var packageHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type packageRequest struct {
	Kind       string           `json:"kind"`
	PlanID     string           `json:"planId,omitempty"`
	ScanID     string           `json:"scanId,omitempty"`
	ResourceID string           `json:"resourceId,omitempty"`
	Import     *imports.Request `json:"import,omitempty"`
}

// assistSource is where a package's record comes from: the launch manager or a Hub scope.
// The plan, drift and import functions are nil for a Hub workspace without execution.
type assistSource struct {
	key     string
	scope   assist.Scope
	snap    workspace.Snapshot
	current func() bool // the snapshot the package was built from is still the current one
	plan    func(string) (*operations.Plan, bool)
	changed func(*operations.Plan) bool
	scan    func(string) (operations.DriftScanView, bool)
	detail  func(string, string) (drift.Detail, error)
	preview func(context.Context, imports.Request) (imports.Preview, error)
	fail    func(http.ResponseWriter, error)
}

func providerNames(snap workspace.Snapshot) []string {
	out := make([]string, 0, len(snap.Index.Providers))
	for p := range snap.Index.Providers {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func assistRoutes(mux *http.ServeMux, store *workspace.Store, manager *operations.Manager, hub *projects.Hub) {
	on := func(w http.ResponseWriter) (*assist.Session, bool) {
		if manager.Assist == nil || !manager.Assist.Enabled() {
			reasonJSON(w, 409, "ai_disabled", "AI assistance is off. Turn it on in Settings to build a context package.")
			return nil, false
		}
		return manager.Assist, true
	}
	mux.HandleFunc("GET /api/v1/ai/settings", func(w http.ResponseWriter, r *http.Request) {
		if manager.Assist == nil {
			writeJSON(w, 200, assist.Settings{})
			return
		}
		writeJSON(w, 200, manager.Assist.Settings())
	})
	// Settings changes: the master switch, the default assistant (among detected
	// headless-eligible ones) and accepting the disclosure for this session. Executable paths
	// are never set here (OD-AI-3).
	mux.HandleFunc("POST /api/v1/ai/settings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled          *bool   `json:"enabled"`
			DefaultProvider  *string `json:"defaultProvider"`
			AcceptDisclosure *string `json:"acceptDisclosure"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Enabled == nil && body.DefaultProvider == nil && body.AcceptDisclosure == nil {
			errorJSON(w, 400, "invalid_request")
			return
		}
		if manager.Assist == nil {
			reasonJSON(w, 409, "ai_unavailable", "AI assistance is not available in this session.")
			return
		}
		if body.Enabled != nil {
			manager.Assist.SetEnabled(*body.Enabled)
		}
		if body.DefaultProvider != nil {
			if err := manager.Assist.SetDefault(*body.DefaultProvider); err != nil {
				aiRefusal(w, err)
				return
			}
		}
		if body.AcceptDisclosure != nil {
			if err := manager.Assist.Accept(*body.AcceptDisclosure); err != nil {
				aiRefusal(w, err)
				return
			}
		}
		writeJSON(w, 200, manager.Assist.Settings())
	})
	// The session's one headless request: poll it, or cancel it.
	mux.HandleFunc("GET /api/v1/ai/run", func(w http.ResponseWriter, r *http.Request) {
		sess, ok := on(w)
		if !ok {
			return
		}
		run, found := sess.Current()
		if !found {
			aiRefusal(w, &assist.Refusal{Code: assist.RefuseNoRun})
			return
		}
		writeJSON(w, 200, run)
	})
	mux.HandleFunc("POST /api/v1/ai/run/cancel", func(w http.ResponseWriter, r *http.Request) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		writeJSON(w, 200, map[string]bool{"cancelled": sess.Cancel()})
	})

	launch := func(w http.ResponseWriter) (assistSource, bool) {
		if manager.Root() == "" {
			reasonJSON(w, 409, "no_launch_project", "Open a Hub workspace to ask about it. This session has no project opened from the terminal.")
			return assistSource{}, false
		}
		snap := store.Get()
		return assistSource{
			key:     "launch",
			scope:   assist.Scope{Project: snap.Info.Project, Workspace: snap.Info.Workspace, Engine: snap.Info.Engine, EngineVersion: snap.Info.Version, Providers: providerNames(snap), Root: manager.Root()},
			snap:    snap,
			current: func() bool { return store.Get().CapturedAt == snap.CapturedAt },
			plan:    manager.Plan,
			changed: func(p *operations.Plan) bool { c, _ := manager.ContextStatus(p); return c },
			scan:    manager.DriftScan,
			detail:  manager.DriftDetail,
			preview: func(_ context.Context, req imports.Request) (imports.Preview, error) {
				return manager.PreviewImport(req), nil
			},
		}, true
	}
	// hubSource resolves an open Hub context. Plans, drift scans and import previews live in
	// its execution scope; a resource needs only the read context.
	hubSource := func(w http.ResponseWriter, r *http.Request, kind string) (assistSource, bool) {
		c := openContext(w, r, hub)
		if c == nil {
			return assistSource{}, false
		}
		cid, snap := r.PathValue("cid"), c.Snapshot()
		src := assistSource{
			key:   cid,
			scope: assist.Scope{Project: c.Alias, Workspace: c.Workspace, Engine: c.Engine, EngineVersion: snap.Info.Version, Providers: providerNames(snap), Root: c.RootPath},
			snap:  snap,
			current: func() bool {
				now, _, err := hub.Context(cid)
				return err == nil && now.Snapshot().CapturedAt == snap.CapturedAt
			},
			fail: func(w http.ResponseWriter, err error) { scopeError(w, r, hub, err) },
		}
		if kind != assist.KindPlan && kind != assist.KindDrift && kind != assist.KindImport {
			return src, true
		}
		s, ok := resolveScope(w, r, hub, kind == assist.KindImport)
		if !ok {
			return assistSource{}, false
		}
		if s != nil {
			src.plan, src.scan, src.detail, src.preview = s.Plan, s.DriftScan, s.DriftDetail, s.PreviewImport
			src.changed = func(p *operations.Plan) bool { c, _ := s.ContextStatus(p); return c }
			// A scope evicted or replaced while the read context stays open dropped its plans
			// and scans; its packages are stale too.
			snapCurrent := src.current
			src.current = func() bool {
				now, err := hub.ExistingScope(cid)
				return err == nil && now == s && snapCurrent()
			}
		}
		return src, true
	}

	build := func(w http.ResponseWriter, r *http.Request, source func(string) (assistSource, bool)) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body packageRequest
		if !decodeBody(w, r, &body) {
			return
		}
		src, ok := source(body.Kind)
		if ok {
			buildPackage(w, r, sess, src, body)
		}
	}
	export := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		hash := r.PathValue("hash")
		kind, markdown, found, fresh := "", "", false, false
		if packageHash.MatchString(hash) {
			kind, markdown, found, fresh = sess.Get(key, hash)
		}
		switch {
		case !found:
			reasonJSON(w, 404, "package_not_found", "This context package expired or was not built here. Open the preview again.")
		case !fresh:
			reasonJSON(w, 409, "context_changed", "The workspace changed since this package was built. Open the preview again to share current context.")
		default:
			writeJSON(w, 200, map[string]string{"kind": kind, "sha256": hash, "markdown": markdown, "filename": "hclaro-" + kind + "-" + hash[:12] + ".md"})
		}
	}

	// run sends a previewed package to an assistant. The browser names only the hash, the
	// provider and the disclosure version it accepted; the bytes are the stored package.
	run := func(w http.ResponseWriter, r *http.Request, src assistSource) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct {
			Provider          string `json:"provider"`
			DisclosureVersion string `json:"disclosureVersion"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		hash := r.PathValue("hash")
		if !packageHash.MatchString(hash) {
			aiRefusal(w, &assist.Refusal{Code: assist.RefuseNotFound})
			return
		}
		scope := assist.RunScope{Project: src.scope.Project, Root: src.scope.Root, Workspace: src.scope.Workspace, Engine: src.scope.Engine}
		started, err := sess.Start(src.key, hash, body.Provider, body.DisclosureVersion, scope)
		if err != nil {
			aiRefusal(w, err)
			return
		}
		writeJSON(w, 202, started)
	}
	mux.HandleFunc("POST /api/v1/ai/packages/{hash}/run", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := launch(w); ok {
			run(w, r, src)
		}
	})

	mux.HandleFunc("POST /api/v1/ai/packages", func(w http.ResponseWriter, r *http.Request) {
		build(w, r, func(string) (assistSource, bool) { return launch(w) })
	})
	mux.HandleFunc("POST /api/v1/ai/packages/{hash}/export", func(w http.ResponseWriter, r *http.Request) {
		export(w, r, "launch")
	})
	if hub == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/packages", func(w http.ResponseWriter, r *http.Request) {
		build(w, r, func(kind string) (assistSource, bool) { return hubSource(w, r, kind) })
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/packages/{hash}/run", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := hubSource(w, r, assist.KindResource); ok {
			run(w, r, src)
		}
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/packages/{hash}/export", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := on(w); ok && openContext(w, r, hub) != nil {
			export(w, r, r.PathValue("cid"))
		}
	})
}

func buildPackage(w http.ResponseWriter, r *http.Request, sess *assist.Session, src assistSource, body packageRequest) {
	var p assist.Package
	var err error
	fresh := src.current
	switch body.Kind {
	case assist.KindPlan:
		var pl *operations.Plan
		found := false
		if src.plan != nil && idPattern.MatchString(body.PlanID) {
			pl, found = src.plan(body.PlanID)
		}
		if !found {
			errorJSON(w, 404, "plan_not_found")
			return
		}
		if src.changed(pl) {
			reasonJSON(w, 409, "context_changed", "The project context changed since this plan was made. Run a new plan, then ask again.")
			return
		}
		p, err = assist.Plan(src.scope, pl.Review)
		fresh = func() bool { return src.current() && !src.changed(pl) }
	case assist.KindDrift:
		var scan operations.DriftScanView
		found := false
		if src.scan != nil && idPattern.MatchString(body.ScanID) {
			scan, found = src.scan(body.ScanID)
		}
		switch {
		case !found:
			errorJSON(w, 404, "scan_not_found")
			return
		case !scan.Current:
			reasonJSON(w, 409, "scan_stale", "This scan no longer matches the current workspace or context. Scan again, then ask again.")
			return
		case scan.Status != operations.ScanCompleted && scan.Status != operations.ScanPartial:
			reasonJSON(w, 409, "scan_unavailable", "Only a finished scan with drift records can be shared.")
			return
		}
		res := drift.Result{EngineVersion: scan.EngineVersion, Errored: scan.Status == operations.ScanPartial, Incomplete: !scan.Exhaustive,
			Summary: scan.Summary, Resources: scan.Resources, Details: map[string]drift.Detail{}, Warnings: scan.Warnings}
		for _, c := range scan.Resources {
			d, derr := src.detail(scan.ID, c.ID)
			if errors.Is(derr, operations.ErrScanStale) {
				reasonJSON(w, 409, "scan_stale", "This scan no longer matches the current workspace or context. Scan again, then ask again.")
				return
			}
			if derr == nil {
				res.Details[c.ID] = d
			}
		}
		p, err = assist.Drift(src.scope, res)
		id := scan.ID
		fresh = func() bool { v, ok := src.scan(id); return src.current() && ok && v.Current }
	case assist.KindImport:
		if body.Import == nil {
			errorJSON(w, 400, "invalid_request")
			return
		}
		if src.preview == nil {
			reasonJSON(w, 409, "import_unavailable", "Imports are not available for this workspace.")
			return
		}
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		preview, perr := src.preview(ctx, *body.Import)
		if perr != nil {
			src.fail(w, perr)
			return
		}
		p, err = assist.Import(src.scope, *body.Import, preview)
	case assist.KindResource:
		res, found := src.snap.Index.ByID[body.ResourceID]
		if !found {
			errorJSON(w, 404, "resource_not_found")
			return
		}
		p, err = assist.Resource(src.scope, res)
	default:
		reasonJSON(w, 400, "invalid_kind", "Choose a plan, drift scan, import or resource.")
		return
	}
	var blocked *assist.Blocked
	switch {
	case errors.As(err, &blocked):
		// Field paths only, never values, and no package text or hash.
		writeJSON(w, 422, map[string]any{"error": "package_blocked", "reason": "Some values still look secret or local after redaction, so Hclaro will not share this context.", "blockedFields": blocked.Fields, "disclosure": sess.Settings().Disclosure})
		return
	case errors.Is(err, assist.ErrTooLarge):
		reasonJSON(w, 413, "package_too_large", "This context is too large to share, even without optional sections.")
		return
	case err != nil:
		errorJSON(w, 500, "package_failed")
		return
	}
	sess.Put(src.key, p, fresh)
	writeJSON(w, 200, struct {
		assist.Package
		Disclosure assist.Disclosure `json:"disclosure"`
	}{p, sess.Settings().Disclosure})
}

func aiRefusal(w http.ResponseWriter, err error) {
	var r *assist.Refusal
	if !errors.As(err, &r) {
		errorJSON(w, 500, "ai_failed")
		return
	}
	status := 409
	switch r.Code {
	case assist.RefuseNotFound, assist.RefuseNoRun:
		status = 404
	case assist.RefuseInvalidArgs:
		status = 400
	}
	reasonJSON(w, status, r.Code, r.Error())
}
