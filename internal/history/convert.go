package history

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"hclaro/internal/operations"
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
	workspaceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,90}$`)
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

func (s *Store) project(r operations.Record) (id, name string, registered bool) {
	if s.Project != nil && r.Root != "" {
		id, name, registered = s.Project(r.Root)
	}
	if r.Operation.ProjectID != "" && ValidID(r.Operation.ProjectID) {
		id, registered = r.Operation.ProjectID, true
	}
	if id == "" && r.Root != "" {
		id = rootID(r.Root)
	}
	if name == "" && r.Root != "" {
		name = filepath.Base(r.Root)
	}
	return id, clean(name, 120), registered
}

func status(op operations.Operation, final bool) string {
	switch op.Status {
	case operations.Succeeded:
		return StatusSucceeded
	case operations.Failed:
		return StatusFailed
	case operations.Cancelled:
		return StatusCancelled
	case operations.Unknown:
		return StatusUnknown
	}
	if final {
		// An operation reported as finished without a final status has no known outcome.
		return StatusUnknown
	}
	return StatusRunning
}

func kind(op operations.Operation, p *operations.Plan) string {
	switch op.Kind {
	case "plan":
		switch {
		case p != nil && slices.Contains([]string{"plan", "replace", "import"}, p.Kind):
			return p.Kind
		case op.Import != nil:
			return "import"
		case len(op.Replace) > 0:
			return "replace"
		}
		return "plan"
	case "apply", "drift", "graph", "init", "workspace", "tool":
		return op.Kind
	}
	return KindUnknown
}

// convert turns an operation record into a history entry. It copies only metadata that the
// operation already exposes, and passes every string through clean.
func (s *Store) convert(r operations.Record) HistoryEntry {
	op := r.Operation
	e := HistoryEntry{
		ID: op.ID, Kind: kind(op, r.Plan), Status: status(op, r.Final), StartedAt: op.StartedAt, DurationMs: op.DurationMs,
		Engine: word(r.Engine), Command: []string{}, Excerpt: []Line{},
	}
	if r.Final {
		e.FinishedAt = op.CompletedAt
		if op.ExitCode != nil {
			code := *op.ExitCode
			e.ExitCode = &code
		}
	} else {
		e.Session = s.session
		if s.Git != nil && r.Root != "" {
			e.Git = s.Git(r.Root)
		}
	}
	if versionRe.MatchString(r.EngineVersion) {
		e.EngineVersion = r.EngineVersion
	}
	e.ProjectID, e.ProjectName, e.Registered = s.project(r)
	if workspaceRe.MatchString(r.Workspace) {
		e.Workspace = r.Workspace
	}
	switch {
	case op.Tool != nil:
		e.Action = word(string(op.Tool.Tool))
	case op.Workspace != nil:
		e.Action = word(string(op.Workspace.Action))
	}
	if c := op.Context; c != nil {
		e.Context = &ContextSummary{Fingerprint: short(c.Fingerprint), ProfileName: clean(c.ProfileName, 120), ProfileRevision: c.Revision}
	}
	for i, arg := range op.Command {
		if i == maxCommand {
			e.Command = append(e.Command, "…")
			break
		}
		e.Command = append(e.Command, clean(arg, maxText))
	}
	if d := op.Diagnostic; d != nil && codePattern.MatchString(d.Code) {
		e.Diagnostic = &Diagnostic{Code: d.Code, Message: clean(d.Message, maxMessage)}
	}
	if r.Plan != nil {
		e.Plan = summarize(r.Plan)
	}
	e.Outcome = outcome(e, op, r)
	if r.Final {
		for _, event := range r.Excerpt {
			e.Excerpt = append(e.Excerpt, Line{Time: event.Time, Level: word(event.Level), Text: clean(event.Text, maxText)})
		}
		if len(r.Excerpt) > 0 {
			e.ExcerptOmitted = r.Excerpt[0].Seq - 1
		}
		if len(e.Excerpt) > maxExcerpt {
			e.ExcerptOmitted += int64(len(e.Excerpt) - maxExcerpt)
			e.Excerpt = e.Excerpt[len(e.Excerpt)-maxExcerpt:]
		}
	}
	return e
}

func summarize(p *operations.Plan) *PlanSummary {
	out := &PlanSummary{
		PlanID: p.ID, Kind: p.Kind, Counts: map[string]int{}, Changes: []PlannedChange{}, Errored: p.Errored, Destructive: p.Destructive,
		Drift: len(p.Drift), VarFiles: []string{}, Replace: []string{}, Fingerprint: short(p.Fingerprint),
	}
	for action, n := range p.Summary {
		if word(action) != "" && n > 0 && action != plan.NoOp {
			out.Counts[action] = n
		}
	}
	for _, c := range p.Changes {
		action := word(c.Action)
		switch {
		case c.Importing && c.Action == plan.NoOp:
			action = "import"
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
	for _, o := range p.Outputs {
		if o.Action != plan.NoOp {
			out.Outputs++
		}
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
	if p.Import != nil {
		out.Import = cleanAddress(p.Import.Address)
	}
	return out
}

func outcome(e HistoryEntry, op operations.Operation, r operations.Record) Outcome {
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
		o := Outcome{PlanID: op.PlanID, StateReloaded: op.StateReloaded}
		switch {
		case op.Outcome == "applied" && e.Status == StatusSucceeded:
			o.Result, o.Summary = "applied", "The engine reported that the apply finished. This is not proof that every resource is healthy; check them in your provider."
		case op.Outcome == "uncertain" || e.Status == StatusCancelled || e.Status == StatusUnknown:
			o.Result, o.Summary = "partial", "The apply did not finish cleanly. Infrastructure and state may be partly changed. Review state and run a new plan."
		default:
			o.Result, o.Summary = "not_applied", "Nothing was applied: the apply was refused or stopped before the engine changed anything."
		}
		return o
	case "drift":
		o := Outcome{Summary: "A refresh-only plan compared state with what providers report. It did not write state or change infrastructure."}
		if r.Drift != nil {
			o.Drift = &DriftCounts{Changed: r.Drift.Changed, Missing: r.Drift.Missing, CannotVerify: r.Drift.CannotVerify}
		}
		switch {
		case r.DriftStatus == operations.ScanCompleted && word(r.DriftOutcome) != "":
			o.Result = r.DriftOutcome
		case r.DriftStatus == operations.ScanPartial:
			o.Result = "partial"
			o.Summary = "The scan did not complete. Listed objects did drift; objects not listed were not necessarily checked."
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
		ran := op.LockfileChanged != nil
		switch {
		case e.Status == StatusSucceeded:
			return Outcome{Result: "completed", Summary: "Initialization finished.", Verified: true}
		case e.Status == StatusUnknown:
			return Outcome{Result: "unverified", Summary: "The engine exited successfully, but the initialized data directory was not found afterwards."}
		case ran:
			return Outcome{Result: "partial", Summary: "Initialization did not finish. Downloads under the data directory may be incomplete, and the lock file may already have been written."}
		}
		return Outcome{Result: "not_run", Summary: "Initialization was refused or stopped before the engine ran."}
	case "workspace":
		ran := op.Workspace != nil && op.Workspace.Ran
		switch {
		case e.Status == StatusSucceeded:
			return Outcome{Result: "completed", Summary: "The workspace command finished.", Verified: op.Workspace != nil && op.Workspace.Verified}
		case e.Status == StatusUnknown || ran:
			return Outcome{Result: "unverified", Summary: "The workspace command may or may not have taken effect. Check the workspace list with your CLI."}
		}
		return Outcome{Result: "not_run", Summary: "The workspace command was refused or stopped before the engine ran."}
	case "tool":
		if e.Status == StatusSucceeded && op.Tool != nil {
			return Outcome{Result: word(op.Tool.Outcome), Summary: "A read-only project tool ran."}
		}
		return Outcome{Result: "not_completed", Summary: "The project tool did not finish. It does not change infrastructure."}
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
	if !workspaceRe.MatchString(e.Workspace) {
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
		if !ValidID(c.PlanID) {
			c.PlanID = ""
		}
		e.Plan = &c
	}
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
