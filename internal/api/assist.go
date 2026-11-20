package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"time"

	"hclaro/internal/assist"
	"hclaro/internal/cost"
	"hclaro/internal/drift"
	"hclaro/internal/gitctx"
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
	// Action is a quick action prompt ID (AI-01.10) for a project package; Cost attaches the
	// session's existing cost estimate to a qa-cost package.
	Action string `json:"action,omitempty"`
	Cost   bool   `json:"cost,omitempty"`
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
	// git lists the configuration files Git does not ignore (AI-01.09).
	git *gitctx.Service
	// bound reports whether the user is still in the project and workspace this source
	// resolved: the launch workspace was not switched, or the Hub read context is still open.
	// Unlike current, re-reading the same workspace does not change it.
	bound func() bool
	// cost returns the summary of the launch project's latest usable cost estimate and a check
	// that it still applies; nil lines when there is none. Nil for Hub sources (M17 is launch-only).
	cost func() ([]string, func() bool)
}

// costSummary is the minimized cost section of a project package: aggregates of an existing
// estimate only, no line items or addresses.
func costSummary(e cost.CostEstimate) []string {
	b, t := e.Breakdown, e.Breakdown.Totals
	c := b.Coverage
	pct := "n/a"
	if c.Percent != nil {
		pct = fmt.Sprintf("%d%%", *c.Percent)
	}
	out := []string{
		// The pricing endpoint is not shared: a self-hosted one names an internal host.
		fmt.Sprintf("estimate of one reviewed plan of workspace %s by %s %s, basis %s, finished %s", e.Workspace, e.Provider, e.ProviderVersion, e.Basis, e.FinishedAt),
		"these figures are estimates, not a bill or verified pricing",
		fmt.Sprintf("currency %s; monthly change of the plan %s (added %s, removed %s, changed %s)", t.Currency, t.MonthlyDelta, t.AddedMonthly, t.RemovedMonthly, t.ChangedDelta),
		fmt.Sprintf("monthly cost of priced resources before the plan %s, after %s", t.PastMonthly, t.ProjectedMonthly),
		fmt.Sprintf("coverage %s: %d of %d cost-bearing changed instances estimated, %d partial, %d usage unknown, %d unsupported, %d free, %d not estimated", pct, c.Estimated, c.CostBearing, c.Partial, c.UsageUnknown, c.Unsupported, c.Free, c.NotEstimated),
	}
	for _, ts := range b.Types {
		delta := "n/a"
		if ts.Delta != nil {
			delta = *ts.Delta
		}
		out = append(out, fmt.Sprintf("type %s: %d changed, %d estimated, %d not estimated, monthly change %s", ts.ResourceType, ts.Count, ts.Estimated, ts.NotEstimated, delta))
	}
	for _, cv := range b.Caveats {
		out = append(out, "caveat: "+cv.Message)
	}
	return out
}

// gitFiles asks Git which configuration files under root it does not ignore.
func gitFiles(ctx context.Context, svc *gitctx.Service, root string) assist.GitFiles {
	if svc == nil {
		svc = gitctx.New()
	}
	listed, repo, ok := svc.ConfigFiles(ctx, root)
	// Asked later, after callers may have cancelled ctx; each Git call has its own timeout.
	ignored := func(rel string) (bool, error) { return svc.Ignored(context.WithoutCancel(ctx), root, rel) }
	return assist.GitFiles{Repo: repo, Listed: listed, Known: ok, Ignored: ignored}
}

func providerNames(snap workspace.Snapshot) []string {
	out := make([]string, 0, len(snap.Index.Providers))
	for p := range snap.Index.Providers {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func assistRoutes(mux *http.ServeMux, deps *Services, store *workspace.Store, manager *operations.Manager, hub *projects.Hub) {
	on := func(w http.ResponseWriter) (*assist.Session, bool) {
		if deps.Assist == nil || !deps.Assist.Enabled() {
			reasonJSON(w, 409, "ai_disabled", "AI assistance is off. Turn it on in Settings to build a context package.")
			return nil, false
		}
		return deps.Assist, true
	}
	mux.HandleFunc("GET /api/v1/ai/settings", func(w http.ResponseWriter, r *http.Request) {
		if deps.Assist == nil {
			writeJSON(w, 200, assist.Settings{})
			return
		}
		writeJSON(w, 200, deps.Assist.Settings())
	})
	// Detection on request (AI-01.14 review): the browser asks only when the user opens an AI
	// preview or the AI Assistant page, or clicks Check in Settings. GET never starts a process.
	mux.HandleFunc("POST /api/v1/ai/detect", func(w http.ResponseWriter, r *http.Request) {
		if !decodeBody(w, r, &struct{}{}) {
			return
		}
		if deps.Assist == nil {
			reasonJSON(w, 409, "ai_unavailable", "AI assistance is not available in this session.")
			return
		}
		writeJSON(w, 200, deps.Assist.Detect())
	})
	// Settings changes: the master switch and the assistant (both saved, AI-01.14; the assistant
	// must be one of the adapters) and accepting the disclosure for this session. Executable paths
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
		if deps.Assist == nil {
			reasonJSON(w, 409, "ai_unavailable", "AI assistance is not available in this session.")
			return
		}
		if body.Enabled != nil {
			if _, err := deps.Assist.SetEnabled(*body.Enabled); err != nil {
				aiRefusal(w, err)
				return
			}
		}
		if body.DefaultProvider != nil {
			if err := deps.Assist.SetDefault(*body.DefaultProvider); err != nil {
				aiRefusal(w, err)
				return
			}
		}
		if body.AcceptDisclosure != nil {
			if err := deps.Assist.Accept(*body.AcceptDisclosure); err != nil {
				aiRefusal(w, err)
				return
			}
		}
		writeJSON(w, 200, deps.Assist.Settings())
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
			git:   manager.Git,
			bound: func() bool { return store.Get().Info.Workspace == snap.Info.Workspace },
			cost: func() ([]string, func() bool) {
				if deps.Cost == nil {
					return nil, nil
				}
				// usable: still the session's newest estimate, completed, opted in, and current for
				// the launch workspace and its plan context.
				usable := func(id string) (cost.CostEstimate, bool) {
					e, b, ok := deps.Cost.LatestCompleted()
					if !ok || id != "" && e.ID != id {
						return e, false
					}
					stale, _ := costStale(manager, store, b)
					return e, !stale
				}
				e, ok := usable("")
				if !ok {
					return nil, nil
				}
				return costSummary(e), func() bool { _, ok := usable(e.ID); return ok }
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
			git:  hub.Git,
			bound: func() bool {
				_, _, err := hub.Context(cid)
				return err == nil
			},
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
			out := map[string]string{"kind": kind, "sha256": hash, "markdown": markdown, "filename": "hclaro-" + kind + "-" + hash[:12] + ".md"}
			if p, ok := assist.ReviewPrompt(kind); ok {
				out["prompt"], out["review"] = p.ID, assist.FirstTurn(markdown, p)
			}
			writeJSON(w, 200, out)
		}
	}

	// Conversations (AI-01.07). Start names a previewed package hash and the disclosure version the
	// user accepted; the server runs the chosen assistant, and refuses when the request names another one. The
	// bytes are the stored package. Every other route names the conversation ID under the scope it
	// started in; another scope answers conversation_not_found.
	start := func(w http.ResponseWriter, r *http.Request, src assistSource) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct {
			PackageSHA256     string `json:"packageSha256"`
			// Provider, when given, is the assistant the user saw; the server refuses the start
			// if its chosen assistant is another one. The server always picks the provider.
			Provider string `json:"provider"`
			DisclosureVersion string `json:"disclosureVersion"`
			Message           string `json:"message"`
			Prompt            string `json:"prompt"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		scope := runScope(src)
		var c assist.Conversation
		var err error
		switch {
		// shortcut: checked just before the start, not under the session lock; a choice changed in
		// that microsecond window is not caught. Move the check into Session.start if that matters.
		case body.Provider != "" && body.Provider != sess.Settings().DefaultProvider:
			err = &assist.Refusal{Code: assist.RefuseProvider}
		case body.PackageSHA256 == "" && body.Prompt == "":
			// Conversation only (AI-01.09): no package. A Hub conversation is bound to its read
			// context; a launch one to the workspace it started in.
			c, err = sess.StartChat(src.key, body.DisclosureVersion, scope, body.Message, src.bound)
		case !packageHash.MatchString(body.PackageSHA256):
			err = &assist.Refusal{Code: assist.RefuseNotFound}
		case body.Prompt == "":
			c, err = sess.StartConversation(src.key, body.PackageSHA256, body.DisclosureVersion, scope, body.Message)
		case body.Message != "": // a review's first message is the prompt itself
			err = &assist.Refusal{Code: assist.RefuseInvalidArgs}
		default:
			c, err = sess.StartReview(src.key, body.PackageSHA256, body.DisclosureVersion, scope, body.Prompt)
		}
		if err != nil {
			aiRefusal(w, err)
			return
		}
		writeJSON(w, 202, c)
	}
	conversationID := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id := r.PathValue("id")
		if !idPattern.MatchString(id) {
			aiRefusal(w, &assist.Refusal{Code: assist.RefuseNoChat})
			return "", false
		}
		return id, true
	}
	get := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		if id, ok := conversationID(w, r); ok {
			c, err := sess.Conversation(key, id)
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, c)
		}
	}
	message := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if id, ok := conversationID(w, r); ok {
			c, err := sess.Send(key, id, body.Text)
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 202, c)
		}
	}
	// attach adds a previewed project package to an open conversation as its next turn.
	attach := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct {
			PackageSHA256 string `json:"packageSha256"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if id, ok := conversationID(w, r); ok {
			if !packageHash.MatchString(body.PackageSHA256) {
				aiRefusal(w, &assist.Refusal{Code: assist.RefuseNotFound})
				return
			}
			c, err := sess.Attach(key, id, body.PackageSHA256)
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, c)
		}
	}
	list := func(w http.ResponseWriter, key, workspace string) {
		if sess, ok := on(w); ok {
			writeJSON(w, 200, map[string]any{"conversations": sess.List(key, workspace)})
		}
	}
	cancelTurn := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if id, ok := conversationID(w, r); ok {
			cancelled, err := sess.CancelTurn(key, id)
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, map[string]bool{"cancelled": cancelled})
		}
	}
	closeConversation := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if id, ok := conversationID(w, r); ok {
			if err := sess.CloseConversation(key, id); err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, map[string]bool{"closed": true})
		}
	}
	// Saved conversations (AI-01.11): the toggle, the project's list, reopen and delete. Every
	// file route names the project of the scope it is asked in; another project's file is refused.
	save := func(w http.ResponseWriter, r *http.Request, key string) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct {
			Saved *bool `json:"saved"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Saved == nil {
			errorJSON(w, 400, "invalid_request")
			return
		}
		if id, ok := conversationID(w, r); ok {
			c, err := sess.Save(key, id, *body.Saved)
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, c)
		}
	}
	savedList := func(w http.ResponseWriter, src assistSource) {
		if sess, ok := on(w); ok {
			out := map[string]any{"conversations": sess.SavedList(src.key, assist.ProjectKey(src.scope.Root)), "available": sess.Store != nil}
			if sess.Store != nil {
				out["retentionDays"] = sess.Store.RetentionDays()
			}
			writeJSON(w, 200, out)
		}
	}
	reopen := func(w http.ResponseWriter, r *http.Request, src assistSource) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if id, ok := conversationID(w, r); ok {
			c, err := sess.Reopen(src.key, id, assist.Resume{Scope: runScope(src), Bound: src.bound, Fingerprint: func() (string, bool) { return projectFingerprint(src) }})
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, c)
		}
	}
	deleteSaved := func(w http.ResponseWriter, r *http.Request, src assistSource) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		if id, ok := conversationID(w, r); ok {
			if err := sess.DeleteSaved(assist.ProjectKey(src.scope.Root), id); err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, map[string]bool{"deleted": true})
		}
	}
	deleteAll := func(w http.ResponseWriter, r *http.Request, src assistSource) {
		sess, ok := on(w)
		if !ok {
			return
		}
		var body struct{}
		if !decodeBody(w, r, &body) {
			return
		}
		n, err := sess.DeleteAllSaved(assist.ProjectKey(src.scope.Root))
		if err != nil {
			aiRefusal(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"deleted": n})
	}
	// Proposed changes (AI-01.12). Listing validates every proposal of the conversation against
	// the files now. Apply and undo run under the workspace's operation lock (exclusive) and
	// write only what the user reviewed; reject writes nothing. None of them runs the engine.
	listChanges := func(w http.ResponseWriter, r *http.Request, src assistSource) {
		sess, _ := on(w)
		if id, ok := conversationID(w, r); ok {
			out, err := sess.Changes(src.key, id, runScope(src), changeFiles(src))
			if err != nil {
				aiRefusal(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"changes": out})
		}
	}
	decideChange := func(w http.ResponseWriter, r *http.Request, src assistSource, action string, exclusive func(func() error) error) {
		sess, _ := on(w)
		var body assist.ChangeRequest
		if !decodeBody(w, r, &body) {
			return
		}
		id, ok := conversationID(w, r)
		if !ok {
			return
		}
		var v assist.ChangeView
		var err error
		switch action {
		case "apply":
			v, err = sess.ApplyChange(src.key, id, body, runScope(src), changeFiles(src), exclusive)
		case "reject":
			v, err = sess.RejectChange(src.key, id, body.Turn, body.Index, runScope(src), changeFiles(src))
		default:
			v, err = sess.UndoChange(src.key, id, body.Turn, body.Index, runScope(src), exclusive)
		}
		if err != nil {
			aiRefusal(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
	launchChange := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, ok := on(w); ok {
				if src, ok := launch(w); ok {
					decideChange(w, r, src, action, func(fn func() error) error { return manager.Exclusive("ai-change", fn) })
				}
			}
		}
	}
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/changes/apply", launchChange("apply"))
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/changes/reject", launchChange("reject"))
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/changes/undo", launchChange("undo"))
	mux.HandleFunc("GET /api/v1/ai/conversations/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := on(w); ok {
			if src, ok := launch(w); ok {
				listChanges(w, r, src)
			}
		}
	})
	if hub != nil {
		mux.HandleFunc("GET /api/v1/contexts/{cid}/ai/conversations/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := on(w); ok {
				if src, ok := hubSource(w, r, assist.KindResource); ok {
					listChanges(w, r, src)
				}
			}
		})
		hubChange := func(action string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if _, ok := on(w); !ok {
					return
				}
				src, ok := hubSource(w, r, assist.KindResource)
				if !ok {
					return
				}
				// Writes need the workspace's execution scope: its lock keeps plans, applies,
				// imports and inits of the same folder out while the file is written.
				scope, ok := resolveScope(w, r, hub, true)
				if !ok {
					return
				}
				if scope.Root() != src.scope.Root {
					aiRefusal(w, &assist.Refusal{Code: assist.RefuseStale})
					return
				}
				decideChange(w, r, src, action, func(fn func() error) error { return scope.Exclusive("ai-change", fn) })
			}
		}
		mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/changes/apply", hubChange("apply"))
		mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/changes/reject", hubChange("reject"))
		mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/changes/undo", hubChange("undo"))
	}
	withLaunch := func(handle func(http.ResponseWriter, *http.Request, assistSource)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, ok := on(w); ok {
				if src, ok := launch(w); ok {
					handle(w, r, src)
				}
			}
		}
	}
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/save", func(w http.ResponseWriter, r *http.Request) { save(w, r, "launch") })
	mux.HandleFunc("GET /api/v1/ai/saved", withLaunch(func(w http.ResponseWriter, _ *http.Request, src assistSource) { savedList(w, src) }))
	mux.HandleFunc("POST /api/v1/ai/saved/{id}/open", withLaunch(reopen))
	mux.HandleFunc("POST /api/v1/ai/saved/{id}/delete", withLaunch(deleteSaved))
	mux.HandleFunc("POST /api/v1/ai/saved/delete-all", withLaunch(deleteAll))

	mux.HandleFunc("POST /api/v1/ai/conversations", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := launch(w); ok {
			start(w, r, src)
		}
	})
	mux.HandleFunc("GET /api/v1/ai/conversations", func(w http.ResponseWriter, r *http.Request) {
		list(w, "launch", store.Get().Info.Workspace)
	})
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/context", func(w http.ResponseWriter, r *http.Request) { attach(w, r, "launch") })
	mux.HandleFunc("GET /api/v1/ai/conversations/{id}", func(w http.ResponseWriter, r *http.Request) { get(w, r, "launch") })
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) { message(w, r, "launch") })
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/cancel", func(w http.ResponseWriter, r *http.Request) { cancelTurn(w, r, "launch") })
	mux.HandleFunc("POST /api/v1/ai/conversations/{id}/close", func(w http.ResponseWriter, r *http.Request) { closeConversation(w, r, "launch") })

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
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := hubSource(w, r, assist.KindResource); ok {
			start(w, r, src)
		}
	})
	// The other Hub conversation routes need only the open read context: the conversation
	// re-checks its own freshness before every turn.
	hubConversation := func(handle func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, ok := on(w); ok && openContext(w, r, hub) != nil {
				handle(w, r, r.PathValue("cid"))
			}
		}
	}
	mux.HandleFunc("GET /api/v1/contexts/{cid}/ai/conversations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := on(w); ok {
			if c := openContext(w, r, hub); c != nil {
				list(w, r.PathValue("cid"), c.Workspace)
			}
		}
	})
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/context", hubConversation(attach))
	mux.HandleFunc("GET /api/v1/contexts/{cid}/ai/conversations/{id}", hubConversation(get))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/messages", hubConversation(message))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/cancel", hubConversation(cancelTurn))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/close", hubConversation(closeConversation))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/conversations/{id}/save", hubConversation(save))
	withHub := func(handle func(http.ResponseWriter, *http.Request, assistSource)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, ok := on(w); ok {
				if src, ok := hubSource(w, r, assist.KindResource); ok {
					handle(w, r, src)
				}
			}
		}
	}
	mux.HandleFunc("GET /api/v1/contexts/{cid}/ai/saved", withHub(func(w http.ResponseWriter, _ *http.Request, src assistSource) { savedList(w, src) }))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/saved/{id}/open", withHub(reopen))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/saved/{id}/delete", withHub(deleteSaved))
	mux.HandleFunc("POST /api/v1/contexts/{cid}/ai/saved/delete-all", withHub(deleteAll))
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
	prompt, _ := assist.ReviewPrompt(body.Kind)
	if body.Action != "" || body.Cost {
		action, ok := assist.LookupPrompt(body.Action)
		if !ok || body.Kind != assist.KindProject || !action.Fits(body.Kind) || body.Cost && action.Name != "qa-cost" {
			errorJSON(w, 400, "invalid_request")
			return
		}
		prompt = action
	}
	// A cost analysis offers the session's existing estimate; it is attached only on request.
	var costLines []string
	var costFresh func() bool
	if prompt.Name == "qa-cost" && src.cost != nil {
		costLines, costFresh = src.cost()
	}
	if body.Cost {
		if costLines == nil {
			reasonJSON(w, 409, "cost_unavailable", "There is no current cost estimate in this session to attach. Estimate a plan first, or start without cost data.")
			return
		}
		src.scope.Cost = costLines
	}
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
	case assist.KindProject:
		ctx, cancel := withDeadline(w, r, inspectTimeout)
		defer cancel()
		p, err = assist.Project(src.scope, gitFiles(ctx, src.git, src.scope.Root))
		// Before every turn: same project and workspace, and the files (and what Git ignores)
		// still give the fingerprint of the bytes this package was built from. A snapshot
		// re-read or a change outside the package scope does not make it stale.
		built := p.Fingerprint
		fresh = func() bool {
			fp, ok := projectFingerprint(src)
			return ok && fp == built && (!body.Cost || costFresh())
		}
		if body.Cost {
			// It depends on this session's estimate: a saved copy never continues after a restart.
			p.Fingerprint = ""
		}
	default:
		reasonJSON(w, 400, "invalid_kind", "Choose a plan, drift scan, import, resource or project.")
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
	// The review's or quick action's first turn (AI-01.08, AI-01.10): exactly what Send submits
	// and Copy or Download hand over.
	writeJSON(w, 200, struct {
		assist.Package
		Disclosure    assist.Disclosure `json:"disclosure"`
		Prompt        string            `json:"prompt"`
		Review        string            `json:"review"`
		CostAvailable bool              `json:"costAvailable,omitempty"`
	}{p, sess.Settings().Disclosure, prompt.ID, assist.FirstTurn(p.Markdown, prompt), costLines != nil})
}

// changeFiles lists the configuration files Git does not ignore, for the scope check of a
// proposed change (AI-01.12): the same allowlist the project package uses.
func changeFiles(src assistSource) func() assist.GitFiles {
	return func() assist.GitFiles {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return gitFiles(ctx, src.git, src.scope.Root)
	}
}

func runScope(src assistSource) assist.RunScope {
	return assist.RunScope{Project: src.scope.Project, Root: src.scope.Root, Workspace: src.scope.Workspace, Engine: src.scope.Engine, ProjectKey: assist.ProjectKey(src.scope.Root)}
}

// projectFingerprint is the current fingerprint of src's project files, false once the user
// left its project and workspace or the files cannot be read.
func projectFingerprint(src assistSource) (string, bool) {
	if !src.bound() {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fp, err := assist.ProjectFingerprint(src.scope.Root, gitFiles(ctx, src.git, src.scope.Root))
	return fp, err == nil
}

func aiRefusal(w http.ResponseWriter, err error) {
	var r *assist.Refusal
	if !errors.As(err, &r) {
		errorJSON(w, 500, "ai_failed")
		return
	}
	status := 409
	switch r.Code {
	case assist.RefuseNotFound, assist.RefuseNoChat, assist.RefuseChangeNotFound:
		status = 404
	case assist.RefuseInvalidArgs, assist.RefuseMessageEmpty:
		status = 400
	case assist.RefuseSaveFailed, assist.RefuseChangeFailed, assist.RefuseChangeNoBackup, assist.RefusePrefs:
		status = 500
	case assist.RefuseTooLong, assist.RefuseMessageLong:
		status = 413
	case assist.RefuseMessageUnsafe, assist.RefuseSaveUnsafe:
		status = 422
	}
	reasonJSON(w, status, r.Code, r.Error())
}
