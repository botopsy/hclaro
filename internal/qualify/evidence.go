package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// M27.08: validation of run JSONs and the Markdown generated from them
// (docs/qualification/real-engine-<yyyymmdd>.md and the support-matrix rows).

// runFields are the run JSON keys every run must carry (RunResult minus omitempty fields).
var runFields = []string{"schemaVersion", "runId", "startedAt", "finishedAt", "partial", "hclaroCommit", "hclaroDirty",
	"hclaroVersion", "hclaroBinarySha256", "os", "arch", "osVersion", "runnerImage", "filesystemCaseSensitive",
	"engines", "providers", "isolation", "credentialFilesReachable", "environmentAllowlist", "goTestExit", "scenarios"}

// ValidateRun decodes raw and returns every reason the run cannot be used (M27.08 work items).
// rows is true when the run is a source of PASS rows, which also needs verified engines.
func ValidateRun(raw []byte, rows bool) (RunResult, []error) {
	var r RunResult
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return r, []error{err}
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, []error{err}
	}
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	// Checked on the raw keys: a missing hclaroDirty would decode as false.
	for _, k := range runFields {
		if v, ok := keys[k]; !ok || string(v) == "null" || string(v) == `""` {
			bad("missing run field %s", k)
		}
	}
	if r.SchemaVersion != 1 {
		bad("schemaVersion %d, want 1", r.SchemaVersion)
	}
	if r.Partial {
		bad("partial run")
	}
	if r.HclaroDirty {
		bad("hclaroDirty: true")
	}
	failed := false
	for _, s := range r.Scenarios {
		failed = failed || s.Status == Fail
		switch s.Status {
		case Pass, Fail, NotTested, Skipped:
		default:
			bad("scenario %s: status %q", s.ID, s.Status)
		}
		if s.Canary != "absent" && s.Canary != "not_checked" {
			bad("scenario %s: canary %q", s.ID, s.Canary)
		}
	}
	if r.GoTestExit != 0 && !failed {
		bad("goTestExit %d without a FAIL scenario", r.GoTestExit)
	}
	if rows {
		for _, e := range r.Engines {
			if e.Provenance != "verified" {
				bad("engine %s %s has provenance %q; not a source of PASS rows", e.Name, e.Version, e.Provenance)
			}
		}
	}
	if m := unsanitised.Find(raw); m != nil {
		bad("unsanitised path %q", m)
	}
	return r, errs
}

// Offline reports whether the run's isolation was enforced; anything else is "offline not verified".
func Offline(r RunResult) bool { return r.Isolation.Level == "enforced" }

// EvidenceHeading is the section heading of a run; its GitHub anchor is EvidenceAnchor.
func EvidenceHeading(label string, r RunResult) string {
	h := r.StartedAt[:10] + " " + label
	if !Offline(r) {
		h += " (offline not verified)"
	}
	return h
}

func EvidenceAnchor(heading string) string {
	return strings.ReplaceAll(regexp.MustCompile(`[^a-z0-9 -]`).ReplaceAllString(strings.ToLower(heading), ""), " ", "-")
}

var engineTitle = map[string]string{"terraform": "Terraform", "tofu": "OpenTofu"}

// cell escapes a value for a Markdown table cell.
func cell(s string) string {
	s = strings.NewReplacer("|", `\|`, "\n", " ", "\r", "").Replace(s)
	if strings.ContainsAny(s, "<>") {
		return "`" + strings.ReplaceAll(s, "`", "'") + "`"
	}
	return s
}

// Family is a scenario ID without its engine suffix.
func Family(id string) string {
	for _, suf := range []string{"-terraform", "-tofu"} {
		if i := strings.Index(id, suf); i > 0 {
			return id[:i]
		}
	}
	return id
}

// operationOf maps scenario families to matrix operations; an unmapped family is an error,
// so nothing is dropped silently.
var operationOf = []struct{ prefix, op string }{
	{"re01", "Plan"}, {"f", "Failure classes"}, {"l1", "State lock"}, {"l2", "State lock"},
	{"a", "Saved-plan apply"}, {"r1", "Saved-plan apply"}, {"win1", "Saved-plan apply"}, {"win3", "Saved-plan apply"},
	{"i", "Import"}, {"win4-crlf-import", "Import"}, {"win4-crlf-generated", "Import"},
	{"n1", "Init, workspaces"}, {"w", "Init, workspaces"},
	{"d", "Drift"}, {"g1", "Graph"}, {"h", "Hub execution"},
}

func operation(family string) (string, error) {
	for _, o := range operationOf {
		if o.prefix == family || strings.HasPrefix(family, o.prefix) && (len(o.prefix) == 1 || strings.HasPrefix(family, o.prefix+"-")) {
			return o.op, nil
		}
	}
	return "", fmt.Errorf("scenario family %q has no matrix operation", family)
}

// fakeOnly are scenarios that run fake engines (F-5); they never back a matrix row.
func fakeOnly(id string) bool {
	return strings.HasPrefix(id, "f5-hub-stop") || strings.HasPrefix(id, "f5-launch-apply-shutdown")
}

// MatrixRow is one generated support-matrix row (without the ID).
type MatrixRow struct {
	Engine, Version, Platform, Status, Operation, Environment, Date, Evidence string
}

func (m MatrixRow) Line(id string) string {
	return "| " + strings.Join([]string{id, m.Engine, m.Version, m.Platform, "local", "local-cli", m.Operation, m.Environment, m.Date, m.Status, m.Evidence}, " | ") + " |"
}

// Rows groups a run's scenarios into matrix rows: per engine (launch-mode scenarios and the
// flag-less n1/w*), and per engine pair (scenarios with the Hub execution flag). Each group
// gives a PASS, a FAIL and a NOT_TESTED row (SKIPPED counts as NOT_TESTED), as present.
func Rows(r RunResult, evidence string) ([]MatrixRow, error) {
	env := fmt.Sprintf("Real engine, disposable projects, local backend, providers from a local mirror, isolation %s (%s)", r.Isolation.Level, r.Isolation.Mechanism)
	if !Offline(r) {
		env += ", offline not verified"
	}
	platform := r.OS + "/" + r.Arch
	type group struct{ engine, version string }
	type entry struct{ status, fam, id string }
	var order []group
	byGroup := map[group][]entry{}
	add := func(g group, status, fam, id string) {
		if byGroup[g] == nil {
			order = append(order, g)
		}
		if status == Skipped {
			status = NotTested
		}
		byGroup[g] = append(byGroup[g], entry{status, fam, id})
	}
	versions := map[string]string{}
	for _, e := range r.Engines {
		versions[e.Name] = e.Version
	}
	var pair group
	if versions["terraform"] != "" && versions["tofu"] != "" {
		pair = group{"Terraform + OpenTofu", versions["terraform"] + " + " + versions["tofu"]}
	}
	for _, s := range r.Scenarios {
		if fakeOnly(s.ID) {
			continue
		}
		fam := Family(s.ID)
		if _, err := operation(fam); err != nil {
			return nil, err
		}
		if s.HubExecutionFlag {
			if pair.engine == "" {
				return nil, fmt.Errorf("scenario %s: Hub scenario in a run without both engines", s.ID)
			}
			add(pair, s.Status, fam, s.ID)
			continue
		}
		v, ok := versions[s.Engine]
		if !ok {
			return nil, fmt.Errorf("scenario %s: engine %q not in the run", s.ID, s.Engine)
		}
		add(group{engineTitle[s.Engine], v}, s.Status, fam, s.ID)
	}
	var rows []MatrixRow
	for _, g := range order {
		// A family with more than one status in the group is listed by full scenario ID, so a
		// PASS row never names a family that also has a FAIL or NOT_TESTED scenario.
		statuses := map[string]map[string]bool{}
		for _, e := range byGroup[g] {
			if statuses[e.fam] == nil {
				statuses[e.fam] = map[string]bool{}
			}
			statuses[e.fam][e.status] = true
		}
		for _, status := range []string{Pass, Fail, NotTested} {
			var fams []string
			for _, e := range byGroup[g] {
				if e.status != status {
					continue
				}
				if len(statuses[e.fam]) > 1 {
					fams = append(fams, e.id)
				} else {
					fams = append(fams, e.fam)
				}
			}
			if len(fams) == 0 {
				continue
			}
			rows = append(rows, MatrixRow{Engine: g.engine, Version: g.version, Platform: platform, Status: status,
				Operation: operations(fams, g.engine == pair.engine), Environment: env, Date: r.StartedAt[:10], Evidence: evidence})
		}
	}
	return rows, nil
}

// operations lists families under their operation, in first-seen order, without duplicates.
func operations(fams []string, hub bool) string {
	var ops []string
	byOp := map[string][]string{}
	seen := map[string]bool{}
	for _, f := range fams {
		if seen[f] {
			continue
		}
		seen[f] = true
		op, _ := operation(Family(f))
		if byOp[op] == nil {
			ops = append(ops, op)
		}
		byOp[op] = append(byOp[op], f)
	}
	var parts []string
	for _, op := range ops {
		parts = append(parts, op+" ("+strings.Join(byOp[op], ", ")+")")
	}
	s := strings.Join(parts, "; ")
	if hub {
		s = "`--experimental-hub-execution`: " + s
	}
	return s
}

// Section renders the evidence section of one validated run.
func Section(label string, r RunResult, runSha string, notes []string) string {
	var b bytes.Buffer
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("## %s\n\n", EvidenceHeading(label, r))
	var vs []string
	for _, e := range r.Engines {
		vs = append(vs, e.Version)
	}
	image := r.RunnerImage
	if r.RunnerImageVersion != "" {
		image += " " + r.RunnerImageVersion
	}
	cred := "unknown"
	if r.CredentialFilesReachable != nil {
		cred = fmt.Sprint(*r.CredentialFilesReachable)
	}
	w("| Field | Value |\n| --- | --- |\n")
	for _, kv := range [][2]string{
		{"Run ID", r.RunID}, {"Started / finished (UTC)", r.StartedAt + " / " + r.FinishedAt},
		{"Hclaro commit", "`" + r.HclaroCommit + "`"}, {"Hclaro version", r.HclaroVersion},
		{"Hclaro binary SHA-256", "`" + r.HclaroBinarySha256 + "`"}, {"run.json SHA-256", "`" + runSha + "`"},
		{"Engine versions", strings.Join(vs, " + ")},
		{"OS / arch / build", r.OS + " / " + r.Arch + " / " + r.OSVersion}, {"Runner image", image},
		{"Case-sensitive file system", fmt.Sprint(r.FilesystemCaseSensitive)},
		{"Isolation", r.Isolation.Mechanism + ", level **" + r.Isolation.Level + "**" + map[bool]string{true: "", false: " (" + r.Isolation.Reason + ")"}[r.Isolation.Reason == ""]},
		{"Credential files reachable", cred}, {"Mirror manifest SHA-256", "`" + r.MirrorManifestSha256 + "`"},
		{"go test exit", fmt.Sprint(r.GoTestExit)},
	} {
		w("| %s | %s |\n", kv[0], cell(kv[1]))
	}
	w("\n| Engine | Version | Binary SHA-256 | Archive SHA-256 | Provenance |\n| --- | --- | --- | --- | --- |\n")
	for _, e := range r.Engines {
		w("| %s | %s | `%s` | `%s` | %s |\n", engineTitle[e.Name], e.Version, e.BinarySha256, e.ArchiveSha256, e.Provenance)
	}
	if len(r.Providers) > 0 {
		w("\n| Mirror | Provider | Version | Platforms | Archive SHA-256 |\n| --- | --- | --- | --- | --- |\n")
		for _, p := range r.Providers {
			w("| %s | %s | %s | %s | `%s` |\n", p.Family, p.Name, p.Version, strings.Join(p.Platforms, ", "), strings.Join(p.ArchiveSha256, "`, `"))
		}
	}
	w("\n%s", probes(r))
	reviewed, direct := 0, map[string]bool{}
	for _, s := range r.Scenarios {
		if s.ReviewedPlanPath {
			reviewed++
		}
		for _, d := range s.DirectEngineCall {
			direct[d] = true
		}
	}
	var calls []string
	for d := range direct {
		calls = append(calls, "`"+d+"`")
	}
	sort.Strings(calls)
	mutating := "none of them applies, imports or replaces"
	for _, c := range calls {
		if directMutation.MatchString(c) {
			mutating = "**a direct call names apply, import or -replace**"
		}
	}
	w("\nReviewed saved-plan path: %d scenarios record `reviewedPlanPath`. Direct engine calls (%s): %s.\n", reviewed, mutating, strings.Join(calls, ", "))
	canary := map[string]int{}
	for _, s := range r.Scenarios {
		canary[s.Canary]++
	}
	w("\nCanary: %v.\n", canary)
	w("\n| Scenario | Engine | Mode | Status | Reason | Artifacts |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, s := range r.Scenarios {
		art := "—"
		if len(s.Artifacts) > 0 {
			art = fmt.Sprintf("%d in `artifacts/%s/`", len(s.Artifacts), s.ID)
		}
		w("| %s | %s | %s | %s | %s | %s |\n", s.ID, cell(s.Engine), s.Mode, s.Status, cell(s.Reason), art)
	}
	if len(notes) > 0 {
		w("\nDefects and notes:\n\n")
		for _, n := range notes {
			w("- %s\n", n)
		}
	}
	w("\n")
	return b.String()
}

// directMutation matches a direct engine call that would bypass the reviewed saved-plan path.
var directMutation = regexp.MustCompile(`\b(apply|import|destroy|-replace)\b`)

// probes summarises each isolation probe: per check, its outcomes and whether all matched.
func probes(r RunResult) string {
	if len(r.Isolation.Probes) == 0 {
		return "No isolation probes recorded.\n"
	}
	var keys []string
	for k := range r.Isolation.Probes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("| Probe | Check | Expected | Outcomes | As expected |\n| --- | --- | --- | --- | --- |\n")
	for _, k := range keys {
		var p struct {
			Checks []struct {
				Name, Expected, Outcome string
				OK                      bool
			}
		}
		if json.Unmarshal(r.Isolation.Probes[k], &p) != nil {
			fmt.Fprintf(&b, "| %s | (unreadable) | | | |\n", k)
			continue
		}
		type agg struct {
			expected string
			outcomes []string
			ok       bool
		}
		var names []string
		by := map[string]*agg{}
		for _, c := range p.Checks {
			a := by[c.Name]
			if a == nil {
				a = &agg{expected: c.Expected, ok: true}
				by[c.Name] = a
				names = append(names, c.Name)
			}
			if !contains(a.outcomes, c.Outcome) {
				a.outcomes = append(a.outcomes, c.Outcome)
			}
			a.ok = a.ok && c.OK
		}
		for _, n := range names {
			a := by[n]
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %v |\n", k, n, a.expected, strings.Join(a.outcomes, ", "), a.ok)
		}
	}
	return b.String()
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
