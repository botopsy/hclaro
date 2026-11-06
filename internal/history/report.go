package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// MaxExport bounds how many entries one export contains.
const MaxExport = 1000

// Report is a redacted export of history entries. Log excerpts are never exported.
type Report struct {
	Format      string         `json:"format"`
	GeneratedAt string         `json:"generatedAt"`
	Version     string         `json:"hclaroVersion"`
	Filter      ReportFilter   `json:"filter"`
	Total       int            `json:"total"`
	Omitted     []string       `json:"omitted"`
	Entries     []HistoryEntry `json:"entries"`
}

type ReportFilter struct {
	ProjectID string `json:"projectId,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Status    string `json:"status,omitempty"`
	Query     string `json:"query,omitempty"`
}

// NewReport builds an export of the entries (newest first). It drops log excerpts and
// session markers and states what was left out.
func NewReport(entries []HistoryEntry, f Filter, version string, now time.Time) Report {
	r := Report{Format: "hclaro-history/1", GeneratedAt: now.UTC().Format(time.RFC3339), Version: clean(version, 60), Filter: ReportFilter(f), Total: len(entries), Omitted: []string{"Log excerpts are not exported."}, Entries: []HistoryEntry{}}
	r.Filter.Query = clean(r.Filter.Query, 200)
	if len(entries) > MaxExport {
		r.Omitted = append(r.Omitted, fmt.Sprintf("Only the newest %d of %d matching operations are included.", MaxExport, len(entries)))
		entries = entries[:MaxExport]
	}
	withheldValues, truncated := 0, 0
	for _, e := range entries {
		e.Excerpt, e.ExcerptOmitted, e.Session = []Line{}, 0, ""
		b, _ := json.Marshal(e)
		withheldValues += strings.Count(string(b), withheld) + strings.Count(string(b), withheldAddr)
		if e.Plan != nil && e.Plan.Omitted > 0 {
			truncated++
		}
		r.Entries = append(r.Entries, e)
	}
	if withheldValues > 0 {
		r.Omitted = append(r.Omitted, fmt.Sprintf("%d value(s) that looked like secrets were withheld when recorded.", withheldValues))
	}
	if truncated > 0 {
		r.Omitted = append(r.Omitted, fmt.Sprintf("%d plan(s) list only their first %d changes.", truncated, maxChanges))
	}
	return r
}

// Markdown renders the report. Every value already passed clean; Markdown control
// characters are escaped so a value cannot change the document structure.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Hclaro operation history\n\n")
	fmt.Fprintf(&b, "Generated %s by Hclaro %s. %d operation(s).\n\n", r.GeneratedAt, md(r.Version), r.Total)
	b.WriteString("> Local, redacted metadata only. Plan files, state, variable values, environment values and logs are not included. A plan in history cannot be applied.\n\n")
	if len(r.Omitted) > 0 {
		b.WriteString("**Omitted:**\n\n")
		for _, o := range r.Omitted {
			b.WriteString("- " + md(o) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("| Started (UTC) | Project | Workspace | Operation | Status | Result | Changes |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", md(e.StartedAt), md(e.ProjectName), md(dash(e.Workspace)), md(label(e)), md(e.Status), md(e.Outcome.Result), md(counts(e.Plan)))
	}
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "\n## %s · %s\n\n", md(label(e)), md(e.StartedAt))
		fmt.Fprintf(&b, "- ID: `%s`\n- Project: %s · workspace %s\n- Engine: %s %s\n- Status: %s · duration %s", e.ID, md(e.ProjectName), md(dash(e.Workspace)), md(e.Engine), md(e.EngineVersion), md(e.Status), (time.Duration(e.DurationMs) * time.Millisecond).String())
		if e.ExitCode != nil {
			fmt.Fprintf(&b, " · exit %d", *e.ExitCode)
		}
		b.WriteString("\n")
		if len(e.Command) > 0 {
			b.WriteString("- Command: " + code(strings.Join(e.Command, " ")) + "\n")
		}
		if e.Context != nil && e.Context.ProfileName != "" {
			fmt.Fprintf(&b, "- Execution profile: %s (revision %d)\n", md(e.Context.ProfileName), e.Context.ProfileRevision)
		}
		if e.Git != nil {
			fmt.Fprintf(&b, "- Git: `%s` %s\n", e.Git.Commit, md(e.Git.Branch))
		}
		if p := e.Plan; p != nil {
			fmt.Fprintf(&b, "- Planned: %s\n", md(counts(p)))
			for _, c := range p.Changes {
				fmt.Fprintf(&b, "  - %s %s\n", md(c.Action), code(c.Address))
			}
			if p.Omitted > 0 {
				fmt.Fprintf(&b, "  - … %d more not kept\n", p.Omitted)
			}
			if p.Execution != nil {
				fmt.Fprintf(&b, "- Executed by apply `%s`: %s\n", p.Execution.OperationID, md(p.Execution.Result))
			}
		}
		fmt.Fprintf(&b, "- Outcome: %s. %s\n", md(e.Outcome.Result), md(e.Outcome.Summary))
		if e.Diagnostic != nil {
			fmt.Fprintf(&b, "- Diagnostic: %s. %s\n", md(e.Diagnostic.Code), md(e.Diagnostic.Message))
		}
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func label(e HistoryEntry) string {
	names := map[string]string{"plan": "Plan", "replace": "Replace plan", "import": "Import plan", "apply": "Apply", "drift": "Drift scan", "graph": "Dependency graph", "init": "Initialize", "workspace": "Workspace", "tool": "Tool"}
	l := names[e.Kind]
	if e.Action != "" {
		l += " " + e.Action
	}
	return l
}

func counts(p *PlanSummary) string {
	if p == nil {
		return "—"
	}
	if len(p.Counts) == 0 {
		return "no changes"
	}
	keys := make([]string, 0, len(p.Counts))
	for k := range p.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", p.Counts[k], k))
	}
	return strings.Join(parts, ", ")
}

var mdEscaper = strings.NewReplacer("\\", "\\\\", "|", "\\|", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;", "#", "\\#", "\n", " ", "\r", " ")

func md(s string) string { return mdEscaper.Replace(s) }

func code(s string) string {
	s = strings.NewReplacer("`", "'", "\n", " ", "\r", " ").Replace(s)
	return "`" + s + "`"
}

// PlanComparison is the difference between two redacted plan summaries.
type PlanComparison struct {
	Left     string          `json:"left"`
	Right    string          `json:"right"`
	Notices  []string        `json:"notices"`
	Added    []PlannedChange `json:"added"`
	Removed  []PlannedChange `json:"removed"`
	Changed  []ActionChange  `json:"changed"`
	Same     int             `json:"same"`
	Complete bool            `json:"complete"`
}

// ActionChange is an address both plans change, with different actions.
type ActionChange struct {
	Address string `json:"address"`
	Left    string `json:"left"`
	Right   string `json:"right"`
}

// ErrIncompatible refuses to compare plans of different projects, workspaces or engines.
type ErrIncompatible struct{ Reason string }

func (e *ErrIncompatible) Error() string { return e.Reason }

// Compatible reports whether two entries hold plan summaries that may be compared.
func Compatible(a, b HistoryEntry) error {
	switch {
	case a.Plan == nil || b.Plan == nil:
		return &ErrIncompatible{"Only operations that produced a plan can be compared."}
	case a.ID == b.ID:
		return &ErrIncompatible{"Choose two different plans."}
	case a.ProjectID != b.ProjectID:
		return &ErrIncompatible{"These plans belong to different projects."}
	case a.Workspace != b.Workspace:
		return &ErrIncompatible{"These plans are for different workspaces."}
	case a.Engine != b.Engine:
		return &ErrIncompatible{"These plans were made with different engines."}
	}
	return nil
}

// Compare lists how the planned changes of b differ from a. a is the older plan.
func Compare(a, b HistoryEntry) (PlanComparison, error) {
	if err := Compatible(a, b); err != nil {
		return PlanComparison{}, err
	}
	if b.StartedAt < a.StartedAt {
		a, b = b, a
	}
	out := PlanComparison{Left: a.ID, Right: b.ID, Notices: []string{}, Added: []PlannedChange{}, Removed: []PlannedChange{}, Changed: []ActionChange{}, Complete: a.Plan.Omitted == 0 && b.Plan.Omitted == 0}
	if a.EngineVersion != b.EngineVersion {
		out.Notices = append(out.Notices, fmt.Sprintf("Engine versions differ (%s and %s).", dash(a.EngineVersion), dash(b.EngineVersion)))
	}
	if a.Plan.Fingerprint != b.Plan.Fingerprint {
		out.Notices = append(out.Notices, "The plan context differs: configuration files, variable files, the data directory or the environment changed between these plans.")
	}
	if !out.Complete {
		out.Notices = append(out.Notices, fmt.Sprintf("At least one plan lists only its first %d changes, so this comparison is incomplete.", maxChanges))
	}
	if slices.Contains([]string{a.Plan.Fingerprint, b.Plan.Fingerprint}, "") {
		out.Notices = append(out.Notices, "A plan context fingerprint is missing.")
	}
	left := map[string]string{}
	for _, c := range a.Plan.Changes {
		if c.Address != withheldAddr {
			left[c.Address] = c.Action
		}
	}
	right := map[string]string{}
	for _, c := range b.Plan.Changes {
		if c.Address == withheldAddr {
			continue
		}
		right[c.Address] = c.Action
		before, ok := left[c.Address]
		switch {
		case !ok:
			out.Added = append(out.Added, c)
		case before != c.Action:
			out.Changed = append(out.Changed, ActionChange{c.Address, before, c.Action})
		default:
			out.Same++
		}
	}
	for _, c := range a.Plan.Changes {
		if _, ok := right[c.Address]; !ok && c.Address != withheldAddr {
			out.Removed = append(out.Removed, c)
		}
	}
	return out, nil
}

// IsIncompatible reports whether err refuses a comparison.
func IsIncompatible(err error) bool {
	var e *ErrIncompatible
	return errors.As(err, &e)
}
