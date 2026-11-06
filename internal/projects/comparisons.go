package projects

import (
	"errors"
	"slices"
	"sync"
	"time"

	"hclaro/internal/compare"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// Comparison limits: records are kept in memory only, never written to disk.
const (
	maxComparisons = 8
	comparisonTTL  = 12 * time.Hour
)

var (
	ErrComparisonNotFound = errors.New("comparison not found")
	ErrSameTarget         = errors.New("both sides name the same target")
)

// ComparisonTarget names one side: a registered project, a CLI workspace and an optional
// execution profile. The server resolves all three; nothing else is accepted.
type ComparisonTarget struct {
	ProjectID string `json:"projectId"`
	Workspace string `json:"workspace"`
	ProfileID string `json:"profileId,omitempty"`
}

// SideError is why one side could not be opened or read. It is never treated as empty.
type SideError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ComparisonSide is the identity and read status of one side.
type ComparisonSide struct {
	Target ComparisonTarget `json:"target"`
	// Status: reading, ready, empty, error or stale.
	Status        string                       `json:"status"`
	ContextID     string                       `json:"contextId,omitempty"`
	Alias         string                       `json:"alias,omitempty"`
	RootPath      string                       `json:"rootPath,omitempty"`
	Engine        string                       `json:"engine,omitempty"`
	Version       string                       `json:"version,omitempty"`
	Backend       string                       `json:"backend,omitempty"`
	ExecutionMode string                       `json:"executionMode,omitempty"`
	CapturedAt    string                       `json:"capturedAt,omitempty"`
	Execution     *operations.ExecutionContext `json:"executionContext,omitempty"`
	Error         *SideError                   `json:"error,omitempty"`
}

// ComparisonView is a comparison as the API returns it. Result is nil unless both sides
// are current and read.
type ComparisonView struct {
	ID        string         `json:"id"`
	CreatedAt string         `json:"createdAt"`
	Status    string         `json:"status"`
	Rules     []compare.Rule `json:"rules"`
	Left      ComparisonSide `json:"left"`
	Right     ComparisonSide `json:"right"`
	// Current is false once either context changed; StaleReason says why and every
	// resource-level result is withheld.
	Current     bool            `json:"current"`
	StaleReason string          `json:"staleReason,omitempty"`
	Withheld    bool            `json:"withheld"`
	Result      *compare.Result `json:"result,omitempty"`
}

type comparisonSide struct {
	target    ComparisonTarget
	contextID string
	err       *SideError
	// alias and root are kept so a failed or stale side still shows what it was.
	alias, root string
}

// Comparison is one stored comparison definition with its two read contexts.
type Comparison struct {
	ID          string
	CreatedAt   time.Time
	Rules       []compare.Rule
	left, right comparisonSide
	mu          sync.Mutex
	// result is computed once both snapshots are read, and only for those snapshots.
	result *compare.Result
	key    string
}

// StartComparison validates both targets and the rules, then opens (or reuses) the two read
// contexts. Opening a context starts the read-only state read; reload starts fresh reads.
// A side that cannot be opened keeps its error, so the comparison shows a partial state
// instead of an empty environment. Rule and request errors refuse the whole comparison.
func (h *Hub) StartComparison(left, right ComparisonTarget, rules []compare.Rule, reload bool) (*Comparison, error) {
	if _, err := compare.Validate(rules); err != nil {
		return nil, err
	}
	for _, t := range []ComparisonTarget{left, right} {
		if !idPattern.MatchString(t.ProjectID) || t.ProfileID != "" && !idPattern.MatchString(t.ProfileID) {
			return nil, ErrNotFound
		}
		if !engine.ValidWorkspace(t.Workspace) {
			return nil, ErrInvalidWorkspace
		}
	}
	if left == right {
		return nil, ErrSameTarget
	}
	c := &Comparison{ID: NewID(), CreatedAt: h.Now().UTC(), Rules: slices.Clone(rules)}
	c.left = h.openSide(left, reload)
	c.right = h.openSide(right, reload)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.comparisons == nil {
		h.comparisons = map[string]*Comparison{}
	}
	h.pruneComparisonsLocked()
	for len(h.comparisonOrder) >= maxComparisons {
		delete(h.comparisons, h.comparisonOrder[0])
		h.comparisonOrder = h.comparisonOrder[1:]
	}
	h.comparisons[c.ID] = c
	h.comparisonOrder = append(h.comparisonOrder, c.ID)
	return c, nil
}

func (h *Hub) openSide(t ComparisonTarget, reload bool) comparisonSide {
	side := comparisonSide{target: t}
	if p, err := h.Registry.Get(t.ProjectID); err == nil {
		side.alias, side.root = p.Alias, p.RootPath
	}
	ctx, _, err := h.OpenWith(t.ProjectID, t.Workspace, t.ProfileID, reload)
	if err != nil {
		side.err = sideError(err)
		return side
	}
	side.contextID = ctx.ID
	return side
}

// sideError maps an open failure to a sanitized code and message.
func sideError(err error) *SideError {
	var path *PathError
	var refusal *ProfileRefusal
	switch {
	case errors.As(err, &refusal):
		return &SideError{refusal.Code, refusal.Message}
	case errors.As(err, &path):
		return &SideError{"project_unavailable", path.Message}
	case errors.Is(err, ErrNotFound):
		return &SideError{"project_not_found", "This project is no longer registered in the Hub."}
	case errors.Is(err, ErrBusy):
		return &SideError{"project_busy", "Initialization or another operation is running in this project. Compare again when it has finished."}
	case errors.Is(err, ErrSharedDataDir):
		return &SideError{"shared_data_dir", "TF_DATA_DIR is an absolute path, so every project would share one data directory. Restart Hclaro without it, or with a relative TF_DATA_DIR."}
	case errors.Is(err, ErrRegistryUnavailable):
		return &SideError{"registry_unavailable", "The project registry cannot be read."}
	}
	return &SideError{"open_failed", "This workspace could not be opened."}
}

func (h *Hub) pruneComparisonsLocked() {
	cutoff := h.Now().Add(-comparisonTTL)
	h.comparisonOrder = slices.DeleteFunc(h.comparisonOrder, func(id string) bool {
		if c := h.comparisons[id]; c != nil && c.CreatedAt.Before(cutoff) {
			delete(h.comparisons, id)
			return true
		}
		return false
	})
}

// Comparisons lists stored comparisons, newest first, without results.
func (h *Hub) Comparisons() []ComparisonView {
	h.mu.Lock()
	h.pruneComparisonsLocked()
	list := make([]*Comparison, 0, len(h.comparisonOrder))
	for i := len(h.comparisonOrder) - 1; i >= 0; i-- {
		list = append(list, h.comparisons[h.comparisonOrder[i]])
	}
	h.mu.Unlock()
	out := make([]ComparisonView, 0, len(list))
	for _, c := range list {
		v := h.View(c)
		v.Result = nil
		out = append(out, v)
	}
	return out
}

// Comparison returns one stored comparison.
func (h *Hub) Comparison(id string) (*Comparison, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneComparisonsLocked()
	if c, ok := h.comparisons[id]; ok {
		return c, nil
	}
	return nil, ErrComparisonNotFound
}

// contextReasons explain why a side's read context is no longer usable.
var contextReasons = map[string]string{
	"project_removed":   "was removed from the Hub",
	"project_changed":   "changed folder or engine",
	"reloaded":          "was read again",
	"evicted":           "was released to save memory after other workspaces were opened",
	"initialized":       "was initialized after it was read",
	"workspace_deleted": "had its workspace deleted",
	"profile_changed":   "had its execution profile edited",
	"profile_removed":   "had its execution profile deleted",
}

// side resolves one side now. It never runs a command: it looks at the open context's
// published snapshot and re-resolves the profile fingerprint.
func (h *Hub) side(s comparisonSide) (ComparisonSide, *Context, workspace.Snapshot, string) {
	out := ComparisonSide{Target: s.target, Alias: s.alias, RootPath: s.root}
	if s.err != nil {
		out.Status, out.Error = "error", s.err
		return out, nil, workspace.Snapshot{}, ""
	}
	out.ContextID = s.contextID
	c, reason, err := h.Context(s.contextID)
	if err != nil {
		why := contextReasons[reason]
		if why == "" {
			why = "is no longer open"
		}
		out.Status = "stale"
		return out, nil, workspace.Snapshot{}, "The " + s.target.Workspace + " workspace of " + s.alias + " " + why + "."
	}
	out.Alias, out.RootPath, out.Engine, out.Execution = c.Alias, c.RootPath, c.Engine, c.Execution.Clone()
	if p, err := h.Registry.Get(c.ProjectID); err != nil || p.RootPath != c.RootPath {
		out.Status = "stale"
		return out, nil, workspace.Snapshot{}, "The project " + c.Alias + " changed in the Hub."
	}
	if c.Execution != nil {
		resolved, _, err := h.Resolve(c.ProjectID, s.target.ProfileID)
		if err != nil || resolved.Fingerprint != c.Execution.Fingerprint {
			out.Status = "stale"
			return out, nil, workspace.Snapshot{}, "The execution profile of " + c.Alias + " / " + s.target.Workspace + " changed."
		}
	}
	snap := c.Snapshot()
	out.Version, out.Backend, out.ExecutionMode, out.CapturedAt = snap.Info.Version, snap.Info.Backend, snap.Info.ExecutionMode, snap.CapturedAt
	switch snap.Status {
	case "loading":
		out.Status = "reading"
	case "ready", "empty":
		out.Status = snap.Status
	default:
		out.Status = "error"
		out.Error = &SideError{"read_failed", "The state could not be read."}
		if snap.Diagnostic != nil {
			out.Error = &SideError{snap.Diagnostic.Code, snap.Diagnostic.Message}
		}
	}
	return out, c, snap, ""
}

// View returns the comparison as it stands now. The result is computed once both sides are
// read and is recomputed only when a snapshot is replaced; a side that changed makes the
// comparison stale and withholds every resource-level result.
func (h *Hub) View(c *Comparison) ComparisonView {
	v := ComparisonView{ID: c.ID, CreatedAt: c.CreatedAt.Format(time.RFC3339), Rules: c.Rules}
	if v.Rules == nil {
		v.Rules = []compare.Rule{}
	}
	var lc, rc *Context
	var ls, rs workspace.Snapshot
	var lStale, rStale string
	v.Left, lc, ls, lStale = h.side(c.left)
	v.Right, rc, rs, rStale = h.side(c.right)
	v.Current = lStale == "" && rStale == ""
	switch {
	case !v.Current:
		v.Status, v.Withheld = "stale", true
		v.StaleReason = lStale
		if v.StaleReason == "" {
			v.StaleReason = rStale
		}
	case v.Left.Status == "reading" || v.Right.Status == "reading":
		// Both sides settle before the outcome is reported, so each shows its final state.
		v.Status = "reading"
		v.Withheld = v.Left.Status == "error" || v.Right.Status == "error"
	case v.Left.Status == "error" || v.Right.Status == "error":
		// One side failed: never compare against an empty stand-in.
		v.Status, v.Withheld = "partial", true
	default:
		v.Status = "ready"
		v.Result = c.compute(lc, rc, ls, rs)
	}
	return v
}

func (c *Comparison) compute(lc, rc *Context, ls, rs workspace.Snapshot) *compare.Result {
	key := lc.ID + "\x00" + ls.CapturedAt + "\x00" + rc.ID + "\x00" + rs.CapturedAt
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.result != nil && c.key == key {
		return c.result
	}
	rules, err := compare.Validate(c.Rules)
	if err != nil {
		return nil
	}
	c.result, c.key = compare.Compare(ls.Index, rs.Index, rules), key
	return c.result
}

// TargetStatus describes a target before it is compared: the last read of an open context
// with the same project, workspace and profile, if any. It runs no command.
type TargetStatus struct {
	Summary    Summary                      `json:"project"`
	Open       bool                         `json:"open"`
	Status     string                       `json:"status,omitempty"`
	CapturedAt string                       `json:"capturedAt,omitempty"`
	Engine     string                       `json:"engine,omitempty"`
	Version    string                       `json:"version,omitempty"`
	Execution  *operations.ExecutionContext `json:"executionContext,omitempty"`
}

// Target returns the status of a comparison target without reading anything.
func (h *Hub) Target(t ComparisonTarget) (TargetStatus, error) {
	p, err := h.Registry.Get(t.ProjectID)
	if err != nil {
		return TargetStatus{}, err
	}
	out := TargetStatus{Summary: h.summary(p)}
	h.mu.Lock()
	var newest *Context
	for _, cid := range h.order {
		c := h.contexts[cid]
		if c != nil && c.ProjectID == t.ProjectID && c.Workspace == t.Workspace && c.profileID == t.ProfileID {
			newest = c
		}
	}
	h.mu.Unlock()
	if newest != nil {
		snap := newest.Snapshot()
		out.Open, out.Status, out.CapturedAt, out.Engine, out.Version, out.Execution = true, snap.Status, snap.CapturedAt, newest.Engine, snap.Info.Version, newest.Execution.Clone()
	}
	return out, nil
}
