package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/compare"
	"hclaro/internal/projects"
)

// maxComparisonRows bounds module and provider rows in one comparison response.
const maxComparisonRows = 2000

// comparisonNotice is shown with every comparison and in every report.
const comparisonNotice = "Different is not necessarily drift. Both sides are recorded state of separate environments; expected differences (names, regions, sizes) look exactly like unexpected ones. Drift is recorded state versus live provider data, which only a drift scan checks."

// comparisonRoutes serve workspace comparisons. Starting one is an explicit,
// CSRF-protected POST that opens (or reuses) two read-only Hub contexts, which read state
// with `show -json`; every GET is side-effect free and never runs a command.
func comparisonRoutes(mux *http.ServeMux, hub *projects.Hub) {
	mux.HandleFunc("GET /api/v1/comparisons/target", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		t := projects.ComparisonTarget{ProjectID: q.Get("projectId"), Workspace: q.Get("workspace"), ProfileID: q.Get("profileId")}
		if !idPattern.MatchString(t.ProjectID) || !validProfileParam(t.ProfileID) {
			errorJSON(w, 400, "invalid_request")
			return
		}
		status, err := hub.Target(t)
		if err != nil {
			projectError(w, hub, err)
			return
		}
		writeJSON(w, 200, status)
	})
	mux.HandleFunc("POST /api/v1/comparisons", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Left  *projects.ComparisonTarget `json:"left"`
			Right *projects.ComparisonTarget `json:"right"`
			Rules []compare.Rule             `json:"rules"`
			// Reload reads both sides again even when a read of them is open.
			Reload bool `json:"reload"`
			// The UI states that both sides' recorded state is read through the CLI.
			Acknowledge *bool `json:"acknowledgeStateRead"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Acknowledge == nil || !*body.Acknowledge {
			errorJSON(w, 400, "acknowledgement_required")
			return
		}
		if body.Left == nil || body.Right == nil {
			reasonJSON(w, 400, "invalid_target", "Choose a project and workspace for both sides.")
			return
		}
		c, err := hub.StartComparison(*body.Left, *body.Right, body.Rules, body.Reload)
		var rule *compare.RuleError
		switch {
		case errors.As(err, &rule):
			writeJSON(w, 400, map[string]any{"error": "invalid_rule", "index": rule.Index, "reason": "Rule " + strconv.Itoa(rule.Index+1) + ": " + rule.Reason})
		case errors.Is(err, projects.ErrSameTarget):
			reasonJSON(w, 400, "same_target", "Both sides name the same project, workspace and profile. Choose a different workspace, project or profile for one side.")
		case errors.Is(err, projects.ErrInvalidWorkspace):
			reasonJSON(w, 400, "invalid_workspace", "Use 1–90 letters, digits, - or _, starting with a letter or digit.")
		case errors.Is(err, projects.ErrNotFound):
			reasonJSON(w, 400, "invalid_target", "Choose a registered project and an execution profile of that project.")
		case err != nil:
			errorJSON(w, 500, "comparison_not_started")
		default:
			writeJSON(w, 202, comparisonBody(hub.View(c)))
		}
	})
	mux.HandleFunc("GET /api/v1/comparisons", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"comparisons": hub.Comparisons()})
	})
	lookup := func(w http.ResponseWriter, r *http.Request) (projects.ComparisonView, bool) {
		id := r.PathValue("id")
		if !idPattern.MatchString(id) {
			errorJSON(w, 404, "comparison_not_found")
			return projects.ComparisonView{}, false
		}
		c, err := hub.Comparison(id)
		if err != nil {
			reasonJSON(w, 404, "comparison_not_found", "This comparison is no longer kept. Compare again.")
			return projects.ComparisonView{}, false
		}
		return hub.View(c), true
	}
	// ready requires a current, complete result for resource-level reads.
	ready := func(w http.ResponseWriter, v projects.ComparisonView) bool {
		switch {
		case v.Status == "stale":
			reasonJSON(w, 409, "comparison_stale", v.StaleReason+" Compare again to use the current snapshots.")
		case v.Status == "partial":
			reasonJSON(w, 409, "comparison_partial", "One side could not be read, so nothing was compared.")
		case v.Result == nil:
			reasonJSON(w, 409, "comparison_not_ready", "Both sides are still being read.")
		default:
			return true
		}
		return false
	}
	mux.HandleFunc("GET /api/v1/comparisons/{id}", func(w http.ResponseWriter, r *http.Request) {
		if v, ok := lookup(w, r); ok {
			writeJSON(w, 200, comparisonBody(v))
		}
	})
	mux.HandleFunc("GET /api/v1/comparisons/{id}/resources", func(w http.ResponseWriter, r *http.Request) {
		v, ok := lookup(w, r)
		if !ok || !ready(w, v) {
			return
		}
		listMatches(w, r, v.Result)
	})
	mux.HandleFunc("GET /api/v1/comparisons/{id}/resources/{mid}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := lookup(w, r)
		if !ok || !ready(w, v) {
			return
		}
		d, found := v.Result.Detail(r.PathValue("mid"))
		if !found {
			errorJSON(w, 404, "match_not_found")
			return
		}
		writeJSON(w, 200, d)
	})
	mux.HandleFunc("GET /api/v1/comparisons/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		if format != "json" && format != "markdown" {
			errorJSON(w, 400, "invalid_format")
			return
		}
		v, ok := lookup(w, r)
		if !ok || !ready(w, v) {
			return
		}
		report := buildReport(v, time.Now().UTC())
		name := "hclaro-comparison-" + v.ID[:8]
		if format == "json" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
			writeJSON(w, 200, report)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.md"`)
		_, _ = w.Write([]byte(report.markdown()))
	})
}

type comparisonResponse struct {
	projects.ComparisonView
	Notice    string                `json:"notice"`
	Summary   *compare.Summary      `json:"summary,omitempty"`
	Modules   []compare.ModuleRow   `json:"modules"`
	Providers []compare.ProviderRow `json:"providers"`
	Types     []string              `json:"types"`
	Warnings  []string              `json:"warnings"`
	// Truncated is set when module or provider rows were capped.
	Truncated bool `json:"truncated"`
}

func comparisonBody(v projects.ComparisonView) comparisonResponse {
	out := comparisonResponse{ComparisonView: v, Notice: comparisonNotice, Modules: []compare.ModuleRow{}, Providers: []compare.ProviderRow{}, Types: []string{}, Warnings: []string{}}
	res := v.Result
	out.Result = nil
	if res == nil {
		return out
	}
	out.Summary = &res.Summary
	out.Modules, out.Providers, out.Warnings = res.Modules, res.Providers, res.Warnings
	if len(out.Modules) > maxComparisonRows {
		out.Modules, out.Truncated = out.Modules[:maxComparisonRows], true
	}
	if len(out.Providers) > maxComparisonRows {
		out.Providers, out.Truncated = out.Providers[:maxComparisonRows], true
	}
	types := map[string]bool{}
	for _, m := range res.Matches {
		types[m.Type] = true
	}
	for t := range types {
		out.Types = append(out.Types, t)
	}
	sort.Strings(out.Types)
	if len(out.Types) > maxComparisonRows {
		out.Types, out.Truncated = out.Types[:maxComparisonRows], true
	}
	return out
}

var matchStatuses = map[string]bool{compare.Same: true, compare.Different: true, compare.OnlyLeft: true, compare.OnlyRight: true, compare.Unknown: true, compare.Uncomparable: true}

func listMatches(w http.ResponseWriter, r *http.Request, res *compare.Result) {
	q := r.URL.Query()
	statuses := map[string]bool{}
	if s := q.Get("status"); s != "" {
		for _, part := range strings.Split(s, ",") {
			if !matchStatuses[part] {
				errorJSON(w, 400, "invalid_status")
				return
			}
			statuses[part] = true
		}
	}
	if mode := q.Get("mode"); mode != "" && mode != "managed" && mode != "data" {
		errorJSON(w, 400, "invalid_mode")
		return
	}
	offset, limit := 0, 100
	var err error
	if q.Has("offset") {
		if offset, err = strconv.Atoi(q.Get("offset")); err != nil || offset < 0 || offset > len(res.Matches) {
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
	query := strings.ToLower(q.Get("query"))
	matches := []compare.Match{}
	for _, m := range res.Matches {
		if len(statuses) > 0 && !statuses[m.Status] || q.Get("mode") != "" && m.Mode != q.Get("mode") || q.Get("type") != "" && m.Type != q.Get("type") || q.Has("module") && m.Module != q.Get("module") {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(m.LeftAddress+" "+m.RightAddress+" "+m.Type+" "+m.Provider), query) {
			continue
		}
		matches = append(matches, m)
	}
	total := len(matches)
	start := min(offset, total)
	end := min(start+limit, total)
	writeJSON(w, 200, map[string]any{"matches": matches[start:end], "total": total})
}

// report is the exported summary. It carries identities, rules, counts and per-instance
// statuses with differing attribute paths, never attribute values or local folder paths.
type report struct {
	Report      string           `json:"report"`
	GeneratedAt string           `json:"generatedAt"`
	Notice      string           `json:"notice"`
	Source      string           `json:"source"`
	Left        reportSide       `json:"left"`
	Right       reportSide       `json:"right"`
	Rules       []compare.Rule   `json:"rules"`
	Summary     compare.Summary  `json:"summary"`
	Warnings    []string         `json:"warnings"`
	Resources   []reportResource `json:"resources"`
}

type reportSide struct {
	Project    string `json:"project"`
	Workspace  string `json:"workspace"`
	Profile    string `json:"profile,omitempty"`
	Engine     string `json:"engine"`
	Version    string `json:"version"`
	Backend    string `json:"backend"`
	CapturedAt string `json:"capturedAt"`
}

type reportResource struct {
	Status       string   `json:"status"`
	Mode         string   `json:"mode"`
	Type         string   `json:"type"`
	LeftAddress  string   `json:"leftAddress,omitempty"`
	RightAddress string   `json:"rightAddress,omitempty"`
	Rule         int      `json:"rule,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Differences  int      `json:"differences"`
	NotCompared  int      `json:"notCompared"`
	Ignored      int      `json:"ignored"`
	Paths        []string `json:"paths"`
}

func sideOf(s projects.ComparisonSide) reportSide {
	out := reportSide{Project: s.Alias, Workspace: s.Target.Workspace, Engine: s.Engine, Version: s.Version, Backend: s.Backend, CapturedAt: s.CapturedAt}
	if s.Execution != nil {
		out.Profile = s.Execution.ProfileName
	}
	return out
}

func buildReport(v projects.ComparisonView, now time.Time) report {
	res := v.Result
	out := report{Report: "Hclaro workspace comparison", GeneratedAt: now.Format(time.RFC3339), Notice: comparisonNotice, Source: "Recorded state of each workspace, read with show -json through the local CLI. Attribute values are redacted and not included in this report.", Left: sideOf(v.Left), Right: sideOf(v.Right), Rules: v.Rules, Summary: res.Summary, Warnings: res.Warnings, Resources: make([]reportResource, 0, len(res.Matches))}
	for _, m := range res.Matches {
		out.Resources = append(out.Resources, reportResource{m.Status, m.Mode, m.Type, m.LeftAddress, m.RightAddress, m.Rule, m.Reason, m.Differences, m.NotCompared, m.Ignored, m.Paths})
	}
	return out
}

// code formats text as a Markdown code span that cannot be closed by the text itself.
func code(s string) string {
	if s == "" {
		return "—"
	}
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	s = strings.NewReplacer("\n", " ", "\r", " ", "|", "\\|").Replace(s)
	if len(fence) > 1 {
		// A longer fence needs padding so a leading or trailing backtick is kept.
		s = " " + s + " "
	}
	return fence + s + fence
}

var statusTitles = map[string]string{
	compare.Different:    "Attribute differences",
	compare.OnlyLeft:     "Only in the left workspace",
	compare.OnlyRight:    "Only in the right workspace",
	compare.Unknown:      "Unknown (redacted values not compared)",
	compare.Uncomparable: "Uncomparable",
	compare.Same:         "Same",
}

func (r report) markdown() string {
	var b strings.Builder
	side := func(label string, s reportSide) {
		profile := s.Profile
		if profile == "" {
			profile = "none"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s %s | %s | %s |\n", label, code(s.Project), code(s.Workspace), code(profile), s.Engine, s.Version, s.Backend, s.CapturedAt)
	}
	fmt.Fprintf(&b, "# %s\n\nGenerated %s.\n\n> %s\n\n%s\n\n", r.Report, r.GeneratedAt, r.Notice, r.Source)
	b.WriteString("| Side | Project | Workspace | Profile | Engine | Backend | State read |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	side("Left", r.Left)
	side("Right", r.Right)
	b.WriteString("\n## Summary\n\n| | Managed | Data sources |\n| --- | --- | --- |\n")
	row := func(label string, a, d int) { fmt.Fprintf(&b, "| %s | %d | %d |\n", label, a, d) }
	m, d := r.Summary.Managed, r.Summary.Data
	row("Same", m.Same, d.Same)
	row("Attribute differences", m.Different, d.Different)
	row("Only left", m.OnlyLeft, d.OnlyLeft)
	row("Only right", m.OnlyRight, d.OnlyRight)
	row("Unknown", m.Unknown, d.Unknown)
	row("Uncomparable", m.Uncomparable, d.Uncomparable)
	fmt.Fprintf(&b, "\nMatched through mapping rules: %d. Attribute values ignored by rules: %d. Redacted values not compared: %d.\n", r.Summary.Mapped, r.Summary.IgnoredAttributes, r.Summary.NotCompared)
	if len(r.Rules) > 0 {
		b.WriteString("\n## Rules\n\nIgnore and substitution rules can hide meaningful divergence.\n\n")
		for i, rule := range r.Rules {
			hits := 0
			if i < len(r.Summary.RuleHits) {
				hits = r.Summary.RuleHits[i]
			}
			switch rule.Kind {
			case compare.RuleMap:
				fmt.Fprintf(&b, "%d. Map %s → %s (%d matched)\n", i+1, code(rule.Left), code(rule.Right), hits)
			case compare.RuleIgnore:
				fmt.Fprintf(&b, "%d. Ignore %s%s (%d values)\n", i+1, code(rule.Path), forType(rule.ResourceType), hits)
			case compare.RuleSubstitute:
				fmt.Fprintf(&b, "%d. Treat %s as %s%s%s (%d values)\n", i+1, code(rule.Left), code(rule.Right), inPath(rule.Path), forType(rule.ResourceType), hits)
			}
		}
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\n**Warning:** %s\n", w)
	}
	for _, status := range compare.Statuses {
		if status == compare.Same {
			continue
		}
		first := true
		for _, res := range r.Resources {
			if res.Status != status {
				continue
			}
			if first {
				fmt.Fprintf(&b, "\n## %s\n\n", statusTitles[status])
				first = false
			}
			address := code(res.RightAddress)
			if res.RightAddress == "" {
				address = code(res.LeftAddress)
			} else if res.LeftAddress != "" && res.LeftAddress != res.RightAddress {
				address = code(res.LeftAddress) + " → " + code(res.RightAddress)
			}
			fmt.Fprintf(&b, "- %s", address)
			if res.Mode == "data" {
				b.WriteString(" (data source)")
			}
			if len(res.Paths) > 0 {
				paths := make([]string, len(res.Paths))
				for i, p := range res.Paths {
					paths[i] = code(p)
				}
				fmt.Fprintf(&b, ": %d differing (%s", res.Differences, strings.Join(paths, ", "))
				if res.Differences > len(res.Paths) {
					b.WriteString(", …")
				}
				b.WriteString(")")
			}
			if res.Reason != "" {
				b.WriteString(" — " + res.Reason)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\n%d instances are the same on both sides.\n", r.Summary.Managed.Same+r.Summary.Data.Same)
	return b.String()
}

func forType(t string) string {
	if t == "" {
		return ""
	}
	return " for " + code(t)
}

func inPath(p string) string {
	if p == "" {
		return ""
	}
	return " in " + code(p)
}
