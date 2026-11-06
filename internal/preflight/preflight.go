package preflight

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"hclaro/internal/plan"
)

// Status of a check. Unknown is never treated as passed.
type Status string

const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Unknown Status = "unknown"
)

// Check is one preflight result. Evidence and remediation are display text built from
// sanitized facts; they never contain plan values, environment values or credentials.
type Check struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Severity    Severity `json:"severity"`
	Status      Status   `json:"status"`
	Evidence    string   `json:"evidence"`
	Remediation string   `json:"remediation,omitempty"`
	// Source is "built-in" for fixed checks and "policy" for configurable rules.
	Source string `json:"source"`
}

// Blocks reports whether the check prevents the apply.
func (c Check) Blocks() bool { return c.Severity == Block && c.Status != Pass }

// NeedsAcknowledgement reports whether the user must acknowledge the check individually.
func (c Check) NeedsAcknowledgement() bool { return c.Severity == Warn && c.Status != Pass }

// Check IDs. They are stable API values.
const (
	CheckContext     = "context.identity"
	CheckFingerprint = "context.fingerprint"
	CheckProfile     = "context.profile"
	CheckArtifact    = "plan.artifact"
	CheckTTL         = "plan.ttl"
	CheckEligible    = "plan.eligible"
	CheckEngine      = "engine.supported"
	CheckBackend     = "backend.execution"
	CheckConflict    = "operations.conflict"
	CheckPolicy      = "policy.valid"
	CheckGit         = "git.worktree"
	CheckProduction  = "context.production"
	CheckDestructive = "plan.destructive"
	CheckScope       = "plan.scope"
	CheckAWS         = "cloud.aws.identity"
)

// GitState is the result of a Git working tree probe.
type GitState string

const (
	GitClean       GitState = "clean"
	GitDirty       GitState = "dirty"
	GitNotRepo     GitState = "not_repository"
	GitUnavailable GitState = "unavailable"
)

// GitResult is a sanitized Git working tree status.
type GitResult struct {
	State     GitState `json:"state"`
	Modified  int      `json:"modified"`
	Untracked int      `json:"untracked"`
	// Paths lists up to ten changed paths, cleaned for display.
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
	// Incomplete is set when the status output was too large to read in full.
	Incomplete bool `json:"incomplete"`
}

// Identity is the result of an optional, explicitly requested cloud identity probe.
type Identity struct {
	// State is not_requested, verified, unavailable (no CLI) or failed.
	State   string `json:"state"`
	Account string `json:"account,omitempty"`
	ARN     string `json:"arn,omitempty"`
	// Message explains unavailable or failed probes; it never contains CLI output.
	Message string `json:"message,omitempty"`
	At      string `json:"verifiedAt,omitempty"`
}

// Recorded is the context a plan was created in.
type Recorded struct {
	Engine, Version, Workspace string
}

// Input is everything Evaluate needs. The caller collects it; Evaluate does no I/O.
type Input struct {
	Now      time.Time
	Recorded Recorded
	Current  Recorded
	// FingerprintMatch is false when configuration, variable files, the lock file or the
	// planning environment changed; ProfileReason explains a changed or removed profile.
	FingerprintMatch bool
	ProfileReason    string
	// ArtifactRetained is false when the saved plan file is gone. DigestMatch is nil when
	// the file could not be hashed.
	ArtifactRetained bool
	ArtifactNote     string
	DigestMatch      *bool
	Digest           string
	ExpiresAt        time.Time
	// Ineligible explains why the plan can never be applied ("" when it can).
	Ineligible string
	// Backend facts from the workspace snapshot.
	Backend, ExecutionMode string
	CanApply               bool
	ApplyReason            string
	EngineSupported        bool
	// Conflict facts.
	Running, Loading, Closed bool
	LockHolder               string
	// Plan facts.
	Kind        string
	Requested   []string
	Changes     []plan.Change
	Summary     map[string]int
	Destructive bool
	Imports     int
	// Labels.
	ProfileName string
	AWSProfile  string
	// Probes and policy.
	Git        GitResult
	Identity   Identity
	Policy     Policy
	PolicyInfo PolicyInfo
}

func check(id, title string, sev Severity, status Status, evidence, remediation string) Check {
	return Check{ID: id, Title: title, Severity: sev, Status: status, Evidence: evidence, Remediation: remediation, Source: "built-in"}
}

func policyCheck(id, title string, sev Severity, status Status, evidence, remediation string) Check {
	c := check(id, title, sev, status, evidence, remediation)
	c.Source = "policy"
	return c
}

// Evaluate returns the checks in a fixed order: blocking context checks first, then
// advisory checks.
func Evaluate(in Input) []Check {
	out := []Check{}
	rerun := "Run a new plan and review it again."

	// Context identity: engine, version and workspace.
	switch r, c := in.Recorded, in.Current; {
	case r.Engine != c.Engine || r.Version != c.Version || r.Workspace != c.Workspace:
		out = append(out, check(CheckContext, "Engine, version and workspace match the plan", Block, Fail,
			fmt.Sprintf("Planned with %s %s in workspace %s; now %s %s in workspace %s.", label(r.Engine), r.Version, r.Workspace, label(c.Engine), orUnknown(c.Version), orUnknown(c.Workspace)), rerun))
	default:
		out = append(out, check(CheckContext, "Engine, version and workspace match the plan", Block, Pass,
			fmt.Sprintf("%s %s, workspace %s.", label(r.Engine), r.Version, r.Workspace), ""))
	}
	if in.ProfileReason != "" {
		out = append(out, check(CheckProfile, "Execution profile is unchanged", Block, Fail, in.ProfileReason, rerun))
	} else if in.ProfileName != "" {
		out = append(out, check(CheckProfile, "Execution profile is unchanged", Block, Pass, "Profile "+in.ProfileName+" has the same revision and context fingerprint.", ""))
	}
	if in.FingerprintMatch {
		out = append(out, check(CheckFingerprint, "Configuration and planning environment are unchanged", Block, Pass, "Configuration, variable files, the lock file and the planning environment match the plan's context fingerprint.", ""))
	} else {
		out = append(out, check(CheckFingerprint, "Configuration and planning environment are unchanged", Block, Fail, "Configuration, variable files, the lock file, the execution profile or the planning environment changed since this plan was created.", rerun))
	}

	// The reviewed artifact.
	short := in.Digest
	if len(short) > 16 {
		short = short[:16]
	}
	switch {
	case in.Ineligible != "":
		out = append(out, check(CheckEligible, "The plan can be applied", Block, Fail, in.Ineligible, rerun))
	default:
		out = append(out, check(CheckEligible, "The plan can be applied", Block, Pass, "The engine marked the plan applyable and it is complete.", ""))
	}
	switch {
	case !in.ArtifactRetained:
		out = append(out, check(CheckArtifact, "Reviewed saved plan is intact", Block, Fail, orDefault(in.ArtifactNote, "The saved plan file is no longer kept."), rerun))
	case in.DigestMatch == nil:
		out = append(out, check(CheckArtifact, "Reviewed saved plan is intact", Block, Unknown, "The saved plan file could not be read to verify its SHA-256.", rerun))
	case !*in.DigestMatch:
		out = append(out, check(CheckArtifact, "Reviewed saved plan is intact", Block, Fail, "The saved plan file no longer matches the reviewed SHA-256 "+short+"….", rerun))
	default:
		out = append(out, check(CheckArtifact, "Reviewed saved plan is intact", Block, Pass, "SHA-256 "+short+"… matches the reviewed plan.", ""))
	}
	switch {
	case !in.ArtifactRetained || in.ExpiresAt.IsZero():
		out = append(out, check(CheckTTL, "Plan has not expired", Block, Unknown, "No retained plan file to time.", rerun))
	case !in.Now.Before(in.ExpiresAt):
		out = append(out, check(CheckTTL, "Plan has not expired", Block, Fail, "The plan expired at "+in.ExpiresAt.UTC().Format(time.RFC3339)+". Infrastructure may have changed since.", rerun))
	default:
		left := in.ExpiresAt.Sub(in.Now).Round(time.Minute)
		out = append(out, check(CheckTTL, "Plan has not expired", Block, Pass, fmt.Sprintf("Expires in about %s.", minutes(left)), ""))
	}

	// Engine and backend support.
	if in.EngineSupported {
		out = append(out, check(CheckEngine, "Engine is recognized and supported", Block, Pass, label(in.Current.Engine)+" "+in.Current.Version+" is in the supported range.", ""))
	} else {
		out = append(out, check(CheckEngine, "Engine is recognized and supported", Block, Fail, "The engine or its version is not one Hclaro supports for applying ("+orUnknown(in.Current.Engine)+" "+orUnknown(in.Current.Version)+").", "Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13."))
	}
	switch {
	case in.ExecutionMode == "remote-exec":
		out = append(out, check(CheckBackend, "Backend supports local saved-plan apply", Block, Fail, "Backend "+in.Backend+" uses remote execution. There is no verified contract that a plan reviewed here is what the remote run applies.", "Apply through the remote backend's own workflow."))
	case in.ExecutionMode != "local-cli" || in.Backend == "" || in.Backend == "unknown":
		out = append(out, check(CheckBackend, "Backend supports local saved-plan apply", Block, Unknown, "The backend type could not be verified from initialization metadata ("+orUnknown(in.Backend)+").", "Check the backend with the CLI and read the project again."))
	case !in.CanApply:
		out = append(out, check(CheckBackend, "Backend supports local saved-plan apply", Block, Fail, orDefault(in.ApplyReason, "Applying is unavailable for this workspace."), ""))
	default:
		out = append(out, check(CheckBackend, "Backend supports local saved-plan apply", Block, Pass, "Backend "+in.Backend+" with local CLI execution.", ""))
	}

	// Local operation conflicts.
	switch {
	case in.Closed:
		out = append(out, check(CheckConflict, "No other local operation is running", Block, Fail, "Hclaro is shutting down.", ""))
	case in.Running:
		out = append(out, check(CheckConflict, "No other local operation is running", Block, Fail, "Another plan, apply or scan is running in this project.", "Wait for it to finish, then run the checks again."))
	case in.LockHolder != "":
		out = append(out, check(CheckConflict, "No other local operation is running", Block, Fail, "This project folder is in use by another Hclaro operation ("+in.LockHolder+").", "Wait for it to finish, then run the checks again."))
	case in.Loading:
		out = append(out, check(CheckConflict, "No other local operation is running", Block, Fail, "Hclaro is still reading the workspace.", "Wait for the read to finish."))
	default:
		out = append(out, check(CheckConflict, "No other local operation is running", Block, Pass, "No plan, apply, scan or initialization is running for this project in Hclaro. Locks held by other tools are checked by the engine.", ""))
	}

	// Policy file.
	pi := in.PolicyInfo
	if !pi.Valid {
		out = append(out, check(CheckPolicy, "Preflight policy is valid", Block, Fail, "The policy file "+PolicyFile+" was refused: "+pi.Error, "Fix or remove "+PolicyFile+", then run the checks again."))
	} else if pi.Source == "default" {
		out = append(out, check(CheckPolicy, "Preflight policy is valid", Info, Pass, "No "+PolicyFile+" found; the default policy applies.", ""))
	} else {
		out = append(out, check(CheckPolicy, "Preflight policy is valid", Info, Pass, fmt.Sprintf("Loaded %s (version %d).", PolicyFile, pi.Version), ""))
	}
	rules := in.Policy.Rules

	// Git working tree.
	if sev := rules.GitDirty; sev != Off {
		title := "Git working tree is clean"
		switch in.Git.State {
		case GitClean:
			out = append(out, policyCheck(CheckGit, title, sev, Pass, "No uncommitted or untracked files.", ""))
		case GitDirty:
			ev := fmt.Sprintf("%d modified and %d untracked file(s)", in.Git.Modified, in.Git.Untracked)
			if in.Git.Incomplete {
				ev += " (or more; the status was too long to read in full)"
			}
			if len(in.Git.Paths) > 0 {
				ev += ", including " + strings.Join(in.Git.Paths, ", ")
			}
			out = append(out, policyCheck(CheckGit, title, sev, Fail, ev+". The applied plan was made from these files; they are not recorded in Git.", "Commit or stash the changes, or acknowledge applying from an uncommitted tree."))
		case GitNotRepo:
			s := Info
			if sev == Block {
				s = Block
			}
			out = append(out, policyCheck(CheckGit, title, s, Unknown, "The project is not in a Git repository, so uncommitted changes cannot be detected.", ""))
		default:
			out = append(out, policyCheck(CheckGit, title, sev, Unknown, "Git status could not be read (Git missing, timed out or refused the repository).", "Check the working tree with git status yourself."))
		}
	}

	// Production-like labels.
	if lr := rules.ProductionLabels; lr.Severity != Off {
		var hits []string
		if p := matchLabel(lr.Patterns, in.Current.Workspace); p != "" {
			hits = append(hits, "workspace "+in.Current.Workspace+" matches "+p)
		}
		if p := matchLabel(lr.Patterns, in.ProfileName); p != "" {
			hits = append(hits, "profile "+in.ProfileName+" matches "+p)
		}
		if p := matchLabel(lr.Patterns, in.AWSProfile); p != "" {
			hits = append(hits, "AWS profile name "+in.AWSProfile+" matches "+p)
		}
		if len(hits) > 0 {
			out = append(out, policyCheck(CheckProduction, "Not a production-like context", lr.Severity, Fail, "Production-like label: "+strings.Join(hits, "; ")+". Labels are names only; they do not prove which account is used.", "Confirm this is the environment you intend to change."))
		} else {
			out = append(out, policyCheck(CheckProduction, "Not a production-like context", lr.Severity, Pass, "No workspace or profile name matches a production-like pattern.", ""))
		}
	}

	// Destructive changes.
	impact := ImpactOf(in.Kind, in.Requested, in.Changes, in.Summary, in.Imports)
	n := impact.DestructiveCount
	switch {
	case rules.DestructiveLimit != nil && n > rules.DestructiveLimit.Max:
		out = append(out, policyCheck(CheckDestructive, "Destructive changes within policy limit", rules.DestructiveLimit.Severity, Fail,
			fmt.Sprintf("The plan deletes, replaces or forgets %d object(s); the policy limit is %d.", n, rules.DestructiveLimit.Max), "Split the change into smaller plans, or change the policy limit deliberately."))
	case rules.DestructiveLimit != nil:
		out = append(out, policyCheck(CheckDestructive, "Destructive changes within policy limit", rules.DestructiveLimit.Severity, Pass, fmt.Sprintf("%d destructive object(s); the policy limit is %d.", n, rules.DestructiveLimit.Max), ""))
	case in.Destructive:
		out = append(out, check(CheckDestructive, "No destructive changes", Info, Fail, fmt.Sprintf("The plan deletes, replaces or forgets %d object(s), or has unrecognized actions. Typed confirmation is required.", n), ""))
	default:
		out = append(out, check(CheckDestructive, "No destructive changes", Info, Pass, "The plan does not delete, replace or forget objects.", ""))
	}

	// Plan scope: changes beyond what the user selected.
	if in.Kind == "replace" || in.Kind == "import" {
		asked := len(in.Requested)
		if in.Kind == "import" {
			asked = in.Imports
		}
		if impact.Extra > 0 {
			out = append(out, check(CheckScope, "Plan changes only what was requested", Warn, Fail,
				fmt.Sprintf("Besides the %d requested %s, the plan also changes %d other resource instance(s). They are listed under Planned changes and will be applied too.", asked, noun(in.Kind), impact.Extra), "Review every listed change, or adjust configuration and plan again."))
		} else {
			out = append(out, check(CheckScope, "Plan changes only what was requested", Warn, Pass, "Every resource change in the plan was requested.", ""))
		}
	}

	// AWS caller identity.
	ar := rules.AWSAccounts
	if impact.UsesAWS || in.AWSProfile != "" || ar != nil {
		sev := Info
		if ar != nil {
			sev = ar.Severity
		}
		id := in.Identity
		title := "AWS caller identity verified"
		hint := "Hclaro does not infer the account from an AWS profile name."
		switch {
		case id.State == "verified" && ar != nil && !slices.Contains(ar.Allowed, id.Account):
			out = append(out, policyCheck(CheckAWS, "AWS account is allowed by policy", sev, Fail, "STS reports account "+id.Account+", which is not in the policy's allowed accounts.", "Switch to credentials for an allowed account, then plan again."))
		case id.State == "verified" && ar != nil:
			out = append(out, policyCheck(CheckAWS, "AWS account is allowed by policy", sev, Pass, "STS reports account "+id.Account+" ("+id.ARN+"), which the policy allows.", ""))
		case id.State == "verified":
			out = append(out, check(CheckAWS, title, sev, Pass, "STS reports account "+id.Account+" ("+id.ARN+"). This is the identity of the CLI credentials now; providers configured with other roles may use another account.", ""))
		case id.State == "unavailable" || id.State == "failed":
			c := check(CheckAWS, title, sev, Unknown, orDefault(id.Message, "The identity could not be verified.")+" "+hint, "Check your AWS credentials, then verify again.")
			if ar != nil {
				c.Source = "policy"
			}
			out = append(out, c)
		default:
			ev := "Not verified."
			if in.AWSProfile != "" {
				ev = "Not verified. The context names AWS profile " + in.AWSProfile + "."
			}
			c := check(CheckAWS, title, sev, Unknown, ev+" "+hint, "Choose Verify AWS identity to call STS GetCallerIdentity (read-only, contacts AWS).")
			if ar != nil {
				c.Source = "policy"
			}
			out = append(out, c)
		}
	}
	return out
}

func noun(kind string) string {
	if kind == "import" {
		return "import(s)"
	}
	return "replacement(s)"
}

func label(engine string) string {
	switch engine {
	case "tofu":
		return "OpenTofu"
	case "terraform":
		return "Terraform"
	}
	return orUnknown(engine)
}

func orUnknown(s string) string { return orDefault(s, "unknown") }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func minutes(d time.Duration) string {
	m := int(d.Minutes())
	if m <= 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// Resource is one planned change in the impact summary.
type Resource struct {
	Address   string `json:"address"`
	Action    string `json:"action"`
	Reason    string `json:"reason,omitempty"`
	Deposed   bool   `json:"deposed,omitempty"`
	Requested bool   `json:"requested,omitempty"`
	Importing bool   `json:"importing,omitempty"`
}

// Impact summarizes the full saved plan, not only the selected resources.
type Impact struct {
	Summary          map[string]int `json:"summary"`
	Total            int            `json:"total"`
	DestructiveCount int            `json:"destructive"`
	Imports          int            `json:"imports"`
	Requested        []string       `json:"requested"`
	// Extra counts resource changes that were not requested (replace and import plans).
	Extra    int        `json:"extra"`
	HighRisk []Resource `json:"highRisk"`
	// Changes lists every resource change other than no-op, in plan order.
	Changes   []Resource `json:"changes"`
	Truncated bool       `json:"truncated"`
	UsesAWS   bool       `json:"-"`
}

const maxImpactRows = 5000

func destructive(action string) bool {
	return action == plan.Delete || action == plan.Replace || action == plan.Forget || action == plan.Unknown
}

// ImpactOf builds the impact summary from the stored plan review.
func ImpactOf(kind string, requested []string, changes []plan.Change, summary map[string]int, imports int) Impact {
	im := Impact{Summary: map[string]int{}, Requested: slices.Clone(requested), Imports: imports, HighRisk: []Resource{}, Changes: []Resource{}}
	if im.Requested == nil {
		im.Requested = []string{}
	}
	for k, v := range summary {
		im.Summary[k] = v
	}
	want := map[string]bool{}
	for _, a := range requested {
		want[a] = true
	}
	for _, c := range changes {
		if strings.Contains(c.Provider, "hashicorp/aws") || strings.HasPrefix(c.Type, "aws_") {
			im.UsesAWS = true
		}
		if c.Action == plan.NoOp && !c.Importing {
			continue
		}
		if c.Action == plan.Read {
			continue
		}
		r := Resource{Address: c.Address, Action: c.Action, Reason: c.Reason, Deposed: c.Deposed != "", Importing: c.Importing}
		r.Requested = want[c.Address] && c.Deposed == "" || kind == "import" && c.Importing
		im.Total++
		if !r.Requested && (kind == "replace" || kind == "import") {
			im.Extra++
		}
		if destructive(c.Action) {
			im.DestructiveCount++
			if len(im.HighRisk) < maxImpactRows {
				im.HighRisk = append(im.HighRisk, r)
			}
		}
		if len(im.Changes) < maxImpactRows {
			im.Changes = append(im.Changes, r)
		} else {
			im.Truncated = true
		}
	}
	return im
}
