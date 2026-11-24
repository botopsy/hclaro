package history

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"hclaro/internal/ids"
	"hclaro/internal/plan"
	"hclaro/internal/state"
)

// Bounds for one entry.
const (
	maxCommand    = 64
	maxExcerpt    = 40
	maxChanges    = 300
	maxText       = 500
	maxMessage    = 1000
	maxListed     = 50
	withheld      = "[withheld: looks like a secret]"
	withheldAddr  = "[redacted address]"
	fingerprintSz = 12
)

var (
	ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	wordPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	hexPattern  = regexp.MustCompile(`^[0-9a-f]+$`)
	versionRe   = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)
)

// clean is the last redaction step before anything is written: control characters are
// removed, text is bounded and a value with a credential shape is withheld entirely. The
// values reaching it were already sanitized by the operation runner.
func clean(s string, limit int) string {
	s = ansiPattern.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f && r != utf8.RuneError {
			return r
		}
		return -1
	}, s)
	if state.Suspicious(s) {
		return withheld
	}
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

func cleanAddress(s string) string {
	if s == "" || len(s) > 512 || state.Suspicious(s) || strings.ContainsAny(s, "\n\r\t") {
		return withheldAddr
	}
	return clean(s, 512)
}

func word(s string) string {
	if wordPattern.MatchString(s) {
		return s
	}
	return ""
}

func short(fingerprint string) string {
	if len(fingerprint) < fingerprintSz || !hexPattern.MatchString(fingerprint) {
		return ""
	}
	return fingerprint[:fingerprintSz]
}

// rootID derives a stable ID for a project root that is not registered in the Hub. The path
// itself is never stored.
func rootID(root string) string {
	sum := sha256.Sum256([]byte("hclaro-history-root\x00" + root))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) project(ev Event) (id, name string, registered bool) {
	if s.sources.Project != nil && ev.Root != "" {
		id, name, registered = s.sources.Project(ev.Root)
	}
	if ev.ProjectID != "" && ids.Valid(ev.ProjectID) {
		id, registered = ev.ProjectID, true
	}
	if id == "" && ev.Root != "" {
		id = rootID(ev.Root)
	}
	if name == "" && ev.Root != "" {
		name = filepath.Base(ev.Root)
	}
	if ev.ProjectName != "" {
		name = ev.ProjectName
	}
	return id, clean(name, 120), registered
}

func status(ev Event) string {
	switch ev.Status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusUnknown:
		return ev.Status
	}
	if ev.Final {
		// An operation reported as finished without a final status has no known outcome.
		return StatusUnknown
	}
	return StatusRunning
}

// convert turns an event into a history entry, passing every string through clean.
func (s *Store) convert(ev Event) HistoryEntry {
	e := HistoryEntry{
		ID: ev.ID, Kind: kindOf(ev.Kind).Kind, Action: word(ev.Action), Status: status(ev), StartedAt: ev.StartedAt, DurationMs: ev.DurationMs,
		Engine: word(ev.Engine), Command: []string{}, Excerpt: []Line{}, Assist: scrubAssist(ev.Assist),
	}
	if e.ID == "" && e.Kind == "assist-change" {
		// A change decision is one event; operations carry their own ID for start and end.
		e.ID = ids.New()
	}
	if ev.Final {
		e.FinishedAt = ev.FinishedAt
		if ev.ExitCode != nil {
			code := *ev.ExitCode
			e.ExitCode = &code
		}
	} else {
		e.Session = s.session
		if s.sources.Git != nil && ev.Root != "" {
			e.Git = s.sources.Git(ev.Root)
		}
	}
	if versionRe.MatchString(ev.EngineVersion) {
		e.EngineVersion = ev.EngineVersion
	}
	e.ProjectID, e.ProjectName, e.Registered = s.project(ev)
	if ids.ValidWorkspace(ev.Workspace) {
		e.Workspace = ev.Workspace
	}
	if c := ev.Context; c != nil {
		e.Context = &ContextSummary{Fingerprint: short(c.Fingerprint), ProfileName: clean(c.ProfileName, 120), ProfileRevision: c.ProfileRevision}
	}
	for i, arg := range ev.Command {
		if i == maxCommand {
			e.Command = append(e.Command, "…")
			break
		}
		e.Command = append(e.Command, clean(arg, maxText))
	}
	if d := ev.Diagnostic; d != nil && codePattern.MatchString(d.Code) {
		e.Diagnostic = &Diagnostic{Code: d.Code, Message: clean(d.Message, maxMessage)}
	}
	if ev.Plan != nil {
		e.Plan = summarize(ev.Plan)
	}
	e.Outcome = outcome(&e, ev)
	if ev.Final {
		for _, l := range ev.Excerpt {
			e.Excerpt = append(e.Excerpt, Line{Time: l.Time, Level: word(l.Level), Text: clean(l.Text, maxText)})
		}
		e.ExcerptOmitted = ev.ExcerptOmitted
		if len(e.Excerpt) > maxExcerpt {
			e.ExcerptOmitted += int64(len(e.Excerpt) - maxExcerpt)
			e.Excerpt = e.Excerpt[len(e.Excerpt)-maxExcerpt:]
		}
	}
	return e
}

// summarize bounds and cleans the plan an event carries. Its changes still include no-op
// instances; an instance imported without changes has the action "import".
func summarize(p *PlanSummary) *PlanSummary {
	out := &PlanSummary{
		PlanID: p.PlanID, Kind: p.Kind, Counts: map[string]int{}, Changes: []PlannedChange{}, Errored: p.Errored, Destructive: p.Destructive,
		Drift: p.Drift, Outputs: p.Outputs, VarFiles: []string{}, Replace: []string{}, Fingerprint: short(p.Fingerprint),
	}
	for action, n := range p.Counts {
		if word(action) != "" && n > 0 && action != plan.NoOp {
			out.Counts[action] = n
		}
	}
	for _, c := range p.Changes {
		action := word(c.Action)
		switch {
		case c.Action == plan.NoOp:
			continue
		case action == "":
			action = plan.Unknown
		}
		if len(out.Changes) == maxChanges {
			out.Omitted++
			continue
		}
		out.Changes = append(out.Changes, PlannedChange{Address: cleanAddress(c.Address), Action: action})
	}
	for i, name := range p.VarFiles {
		if i == maxListed {
			break
		}
		out.VarFiles = append(out.VarFiles, clean(name, 256))
	}
	for i, address := range p.Replace {
		if i == maxListed {
			break
		}
		out.Replace = append(out.Replace, cleanAddress(address))
	}
	if p.Import != "" {
		out.Import = cleanAddress(p.Import)
	}
	return out
}

var changeSummaries = map[string]string{
	"applied":  "A file change an AI assistant proposed was applied after review. A backup of the replaced file was kept. Nothing was run.",
	"rejected": "A file change an AI assistant proposed was rejected. Nothing was written.",
	"undone":   "An applied AI-proposed file change was undone. Nothing was run.",
}

// outcome derives the confirmed result. It may lower e.Status of an assist-change Hclaro refused.
func outcome(e *HistoryEntry, ev Event) Outcome {
	if e.Status == StatusRunning {
		return Outcome{Result: "running", Summary: "In progress."}
	}
	switch e.Kind {
	case "plan", "replace", "import":
		switch e.Status {
		case StatusSucceeded:
			summary := "A plan was created for review. Planning does not change infrastructure. A plan in history cannot be applied; run a new plan and review it."
			if e.Plan != nil && e.Plan.Errored {
				summary = "The engine reported errors while planning; the plan is incomplete. Planning does not change infrastructure."
			}
			return Outcome{Result: "planned", Summary: summary}
		case StatusCancelled:
			return Outcome{Result: "not_planned", Summary: "The plan was cancelled before it finished and no plan was kept. Planning does not change infrastructure; if the engine was stopped forcefully, check for a leftover state lock."}
		}
		return Outcome{Result: "not_planned", Summary: "The plan did not finish and no plan was kept. Planning does not change infrastructure."}
	case "apply":
		o := Outcome{PlanID: ev.PlanID, StateReloaded: ev.StateReloaded}
		switch {
		case ev.Outcome == "applied" && e.Status == StatusSucceeded:
			o.Result, o.Summary = "applied", "The engine reported that the apply finished. This is not proof that every resource is healthy; check them in your provider."
		case ev.Outcome == "uncertain" || e.Status == StatusCancelled || e.Status == StatusUnknown:
			o.Result, o.Summary = "partial", "The apply did not finish cleanly. Infrastructure and state may be partly changed. Review state and run a new plan."
		default:
			o.Result, o.Summary = "not_applied", "Nothing was applied: the apply was refused or stopped before the engine changed anything."
		}
		return o
	case "drift":
		o := Outcome{Summary: "A refresh-only plan compared state with what providers report. It did not write state or change infrastructure."}
		if ev.Drift != nil {
			d := *ev.Drift
			o.Drift = &d
		}
		switch {
		case ev.Outcome == "partial":
			o.Result = "partial"
			o.Summary = "The scan did not complete. Listed objects did drift; objects not listed were not necessarily checked."
		case word(ev.Outcome) != "":
			o.Result = ev.Outcome
		default:
			o.Result = "not_completed"
		}
		return o
	case "graph":
		if e.Status == StatusSucceeded {
			return Outcome{Result: "completed", Summary: "The dependency graph was read. It did not plan, refresh or change infrastructure."}
		}
		return Outcome{Result: "not_completed", Summary: "The dependency graph was not read. Nothing was changed."}
	case "init":
		switch {
		case e.Status == StatusSucceeded:
			return Outcome{Result: "completed", Summary: "Initialization finished.", Verified: true}
		case e.Status == StatusUnknown:
			return Outcome{Result: "unverified", Summary: "The engine exited successfully, but the initialized data directory was not found afterwards."}
		case ev.Ran:
			return Outcome{Result: "partial", Summary: "Initialization did not finish. Downloads under the data directory may be incomplete, and the lock file may already have been written."}
		}
		return Outcome{Result: "not_run", Summary: "Initialization was refused or stopped before the engine ran."}
	case "workspace":
		switch {
		case e.Status == StatusSucceeded:
			return Outcome{Result: "completed", Summary: "The workspace command finished.", Verified: ev.Verified}
		case e.Status == StatusUnknown || ev.Ran:
			return Outcome{Result: "unverified", Summary: "The workspace command may or may not have taken effect. Check the workspace list with your CLI."}
		}
		return Outcome{Result: "not_run", Summary: "The workspace command was refused or stopped before the engine ran."}
	case "branch":
		from, to := orUnknown(cleanBranch(ev.From), "a detached HEAD"), orUnknown(cleanBranch(ev.To), "a branch that was not found")
		switch {
		case e.Status == StatusSucceeded:
			return Outcome{Result: "completed", Summary: "Switched the Git working tree from " + from + " to " + to + ". No Terraform/OpenTofu command ran; plans made before the switch must pass their checks again.", Verified: true}
		case ev.Ran:
			return Outcome{Result: "unverified", Summary: "Git was asked to switch from " + from + " to " + to + " but did not confirm it. Check the working tree with git status. It did not change infrastructure."}
		}
		return Outcome{Result: "not_run", Summary: "The switch to " + to + " was refused before Git changed anything."}
	case "discard":
		return Outcome{Result: "completed", Summary: "A saved plan was discarded at the user's request. It can no longer be applied. Nothing ran against infrastructure or state.", Verified: true}
	case "tool":
		if e.Status == StatusSucceeded && ev.Action != "" {
			return Outcome{Result: word(ev.Outcome), Summary: "A read-only project tool ran."}
		}
		return Outcome{Result: "not_completed", Summary: "The project tool did not finish. It does not change infrastructure."}
	case "assist":
		summary := "A sanitized context package was sent to an AI assistant. The package and the reply are not kept. Nothing was run, applied or approved."
		if e.Assist != nil && e.Assist.ContextKind == "conversation" {
			summary = "A conversation without project data was sent to an AI assistant. The messages and the reply are not kept. Nothing was run, applied or approved."
		}
		return Outcome{Result: map[bool]string{true: "answered", false: "not_answered"}[e.Status == StatusSucceeded], Summary: summary}
	case "assist-change":
		summary, ok := changeSummaries[ev.Outcome]
		if !ok {
			e.Status, summary = StatusFailed, "A file change an AI assistant proposed was refused by Hclaro's checks. Nothing was written."
		}
		return Outcome{Result: word(ev.Outcome), Summary: summary}
	}
	return Outcome{Result: "unknown", Summary: "This version of Hclaro does not know this kind of operation. It may have changed infrastructure or state; check state with your CLI and run a new plan."}
}

// scrubEntry applies clean to every string of an entry read from disk, so a hand-edited
// file cannot bring a secret-shaped value into the API.
func scrubEntry(e HistoryEntry) HistoryEntry {
	// An unrecognised kind is kept, marked unknown, rather than dropped or shown as given.
	e.Kind, e.Action, e.Engine = kindOf(e.Kind).Kind, word(e.Action), word(e.Engine)
	if !versionRe.MatchString(e.EngineVersion) {
		e.EngineVersion = ""
	}
	if !ids.ValidWorkspace(e.Workspace) {
		e.Workspace = ""
	}
	e.ProjectName = clean(e.ProjectName, 120)
	if e.Context != nil {
		c := *e.Context
		c.Fingerprint, c.ProfileName = short(c.Fingerprint), clean(c.ProfileName, 120)
		e.Context = &c
	}
	command := make([]string, 0, len(e.Command))
	for _, arg := range e.Command {
		command = append(command, clean(arg, maxText))
	}
	e.Command = command
	if e.Diagnostic != nil {
		if !codePattern.MatchString(e.Diagnostic.Code) {
			e.Diagnostic = nil
		} else {
			e.Diagnostic = &Diagnostic{Code: e.Diagnostic.Code, Message: clean(e.Diagnostic.Message, maxMessage)}
		}
	}
	if p := e.Plan; p != nil {
		c := *p
		c.Changes = make([]PlannedChange, 0, min(len(p.Changes), maxChanges))
		for i, ch := range p.Changes {
			if i == maxChanges {
				break
			}
			action := word(ch.Action)
			if action == "" {
				action = plan.Unknown
			}
			c.Changes = append(c.Changes, PlannedChange{Address: cleanAddress(ch.Address), Action: action})
		}
		counts := map[string]int{}
		for k, v := range p.Counts {
			if word(k) != "" && v > 0 {
				counts[k] = v
			}
		}
		c.Counts, c.Kind, c.Fingerprint, c.Import = counts, word(p.Kind), short(p.Fingerprint), ""
		if p.Import != "" {
			c.Import = cleanAddress(p.Import)
		}
		c.VarFiles, c.Replace = cleanList(p.VarFiles, 256), []string{}
		for _, a := range p.Replace {
			c.Replace = append(c.Replace, cleanAddress(a))
		}
		if !ids.Valid(c.PlanID) {
			c.PlanID = ""
		}
		e.Plan = &c
	}
	e.Assist = scrubAssist(e.Assist)
	e.Outcome.Result, e.Outcome.Summary = word(e.Outcome.Result), clean(e.Outcome.Summary, maxMessage)
	if e.Git != nil && (!hexPattern.MatchString(e.Git.Commit) || len(e.Git.Commit) > 64) {
		e.Git = nil
	} else if e.Git != nil {
		e.Git = &GitIdentity{Commit: e.Git.Commit, Branch: cleanBranch(e.Git.Branch)}
	}
	excerpt := make([]Line, 0, len(e.Excerpt))
	for _, l := range e.Excerpt {
		excerpt = append(excerpt, Line{Time: clean(l.Time, 40), Level: word(l.Level), Text: clean(l.Text, maxText)})
	}
	e.Excerpt = excerpt
	return e
}

func cleanList(list []string, limit int) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, clean(v, limit))
	}
	return out
}

func orUnknown(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return "branch " + s
}
