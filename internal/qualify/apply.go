package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ApplyApproval is the part of POST /api/v1/plans/{id}/approval the harness reads.
type ApplyApproval struct {
	Token       string         `json:"confirmationToken"`
	Destructive bool           `json:"destructive"`
	Phrase      string         `json:"phrase"`
	Command     []string       `json:"command"`
	Summary     map[string]int `json:"summary"`
	Replace     []string       `json:"replace"`
	Imports     int            `json:"imports"`
}

// Approve runs the preflight and requests an approval, acknowledging each warning the
// preflight lists, as the apply dialog does. It returns the reply so refusals can be asserted.
func (s *Scenario) Approve(h *Hclaro, planID string) (ApplyApproval, Response) {
	var report struct {
		Blocked     bool     `json:"blocked"`
		Binding     string   `json:"binding"`
		Acknowledge []string `json:"acknowledge"`
		Command     []string `json:"command"`
		Checks      []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
			Status   string `json:"status"`
		} `json:"checks"`
	}
	r, err := h.client.Do("POST", "plans/"+planID+"/preflight", map[string]any{"verifyIdentity": false}, &report)
	if err != nil {
		s.Fatalf("preflight: %v", err)
	}
	if r.Status != 200 {
		return ApplyApproval{}, r
	}
	if report.Blocked {
		// Code names the failed blocking checks, e.g. "preflight_blocked:context.fingerprint".
		var failed []string
		for _, c := range report.Checks {
			if c.Severity == "block" && c.Status != "pass" {
				failed = append(failed, c.ID)
			}
		}
		code := "preflight_blocked:" + strings.Join(failed, ",")
		s.Observe("%s", code)
		return ApplyApproval{}, Response{Status: 409, Code: code, Body: r.Body}
	}
	ack := report.Acknowledge
	if ack == nil {
		ack = []string{}
	}
	var a ApplyApproval
	r, err = h.client.Do("POST", "plans/"+planID+"/approval", map[string]any{"preflightBinding": report.Binding, "acknowledged": ack}, &a)
	if err != nil {
		s.Fatalf("approval: %v", err)
	}
	return a, r
}

// StartApply sends the confirmation token and, for a destructive plan, the typed phrase.
func (s *Scenario) StartApply(h *Hclaro, planID, token, phrase string) (Operation, Response) {
	var op Operation
	r, err := h.client.Do("POST", "plans/"+planID+"/apply", map[string]string{"confirmationToken": token, "confirmation": phrase}, &op)
	if err != nil {
		s.Fatalf("apply: %v", err)
	}
	return op, r
}

// ApplyReviewed runs plan approval and apply through Hclaro, typing the phrase when the
// approval asks for one, and follows the apply to its end.
func (s *Scenario) ApplyReviewed(h *Hclaro, planID string) (ApplyApproval, Operation) {
	a, r := s.Approve(h, planID)
	if r.Status != 200 {
		s.Fatalf("approval refused: %d %s", r.Status, r.Code)
	}
	op, r := s.StartApply(h, planID, a.Token, a.Phrase)
	if r.Status != 202 {
		s.Fatalf("apply refused: %d %s", r.Status, r.Code)
	}
	done := s.FollowOp(h, op.ID, 15*time.Minute)
	return a, done
}

// FollowOp follows an operation's SSE stream to its end and reads the final record.
func (s *Scenario) FollowOp(h *Hclaro, id string, timeout time.Duration) Operation {
	if _, err := h.client.Follow("operations/"+id, timeout); err != nil {
		s.Fatalf("operation %s: %v", id, err)
	}
	var op Operation
	if r, err := h.client.Do("GET", "operations/"+id, nil, &op); err != nil || r.Status != 200 {
		s.Fatalf("GET operation: %v %d", err, r.Status)
	}
	s.assertScope(h, op.Scope)
	return op
}

// assertScope checks a scoped response's scope against the view: same context and workspace,
// and the engine and version the project must use (M27.05: on every operation response).
func (s *Scenario) assertScope(h *Hclaro, sc *OpScope) {
	v := h.view
	if v == nil {
		return
	}
	if sc == nil || sc.ContextID != v.ContextID || sc.Workspace != v.Workspace {
		s.Fatalf("scoped response names scope %+v, the view is %s/%s", sc, v.Workspace, v.ContextID)
	}
	s.AssertEngine(v.Project, v.Engine, sc.Engine, sc.Version)
}

// fingerprint returns the contextFingerprint a Hub view's options route returned ("" for the
// launch process, which sends none): the dialog sends it back unchanged with the start.
func (s *Scenario) fingerprint(h *Hclaro, options string) string {
	if h.view == nil {
		return ""
	}
	var o struct {
		ContextFingerprint string   `json:"contextFingerprint"`
		Scope              *OpScope `json:"scope"`
	}
	if r, err := h.client.Do("GET", options, nil, &o); err != nil || r.Status != 200 {
		s.Fatalf("%s: %v %d %s", options, err, r.Status, r.Code)
	}
	s.assertScope(h, o.Scope)
	return o.ContextFingerprint
}

// HistoryEntry is the part of a history list item the harness reads.
type HistoryEntry struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Action     string   `json:"action"`
	Status     string   `json:"status"`
	ExitCode   *int     `json:"exitCode"`
	Workspace  string   `json:"workspace"`
	Command    []string `json:"command"`
	Diagnostic *struct {
		Code string `json:"code"`
	} `json:"diagnostic"`
	Outcome struct {
		Result string `json:"result"`
		PlanID string `json:"planId"`
	} `json:"outcome"`
	// Excerpt is the kept, sanitised log tail (history detail and history.json only).
	Excerpt []struct {
		Text string `json:"text"`
	} `json:"excerpt"`
}

// History lists Hclaro's history, newest first.
func (s *Scenario) History(h *Hclaro) []HistoryEntry {
	var out struct {
		Entries []HistoryEntry `json:"entries"`
	}
	if r, err := h.client.Do("GET", "history?limit=200", nil, &out); err != nil || r.Status != 200 {
		s.Fatalf("history: %v %d", err, r.Status)
	}
	return out.Entries
}

// LastHistory returns the newest entry of kind, failing when there is none.
func (s *Scenario) LastHistory(h *Hclaro, kind string) HistoryEntry {
	for _, e := range s.History(h) {
		if e.Kind == kind {
			return e
		}
	}
	s.Fatalf("no %s history entry", kind)
	return HistoryEntry{}
}

// Resources maps managed resource addresses to the snapshot's opaque IDs (GET resources),
// which the plan dialog sends as -replace selections.
func (s *Scenario) Resources(h *Hclaro) map[string]string {
	var out struct {
		Resources []struct {
			ID      string `json:"id"`
			Address string `json:"address"`
			Mode    string `json:"mode"`
		} `json:"resources"`
	}
	if r, err := h.client.Do("GET", "resources?mode=managed&limit=200", nil, &out); err != nil || r.Status != 200 {
		s.Fatalf("resources: %v %d", err, r.Status)
	}
	m := map[string]string{}
	for _, x := range out.Resources {
		if x.Mode != "data" {
			m[x.Address] = x.ID
		}
	}
	return m
}

func (s *Scenario) waitReady(h *Hclaro) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var ws struct {
			Status     string `json:"status"`
			Diagnostic *struct {
				Code string `json:"code"`
			} `json:"diagnostic"`
		}
		r, err := h.client.Do("GET", "workspace", nil, &ws)
		if err != nil {
			s.Fatalf("workspace: %v", err)
		}
		// A project with no state yet reads as state_absent, which can still be planned.
		absent := ws.Status == "error" && ws.Diagnostic != nil && (ws.Diagnostic.Code == "state_absent" || s.startCodes[ws.Diagnostic.Code])
		if ws.Status != "loading" {
			if ws.Status != "ready" && ws.Status != "empty" && !absent {
				s.Fatalf("workspace status %q after the read: %.2000s", ws.Status, r.Body)
			}
			return
		}
		if time.Now().After(deadline) {
			s.Fatalf("workspace still loading after 2 minutes")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// StateObject is one managed resource instance in `show -json`.
type StateObject struct {
	Address string
	ID      string
}

// ShowState rereads the project's state with the direct, read-only `show -json` and returns
// the managed instances by address. stateFile is relative to project ("" reads the current
// workspace's state as the engine resolves it).
func (s *Scenario) ShowState(dirs ProcessDirs, env []string, engine, project, stateFile string) map[string]StateObject {
	var extra []string
	if stateFile != "" {
		extra = []string{filepath.Join(project, filepath.FromSlash(stateFile))}
	}
	out, err := s.Direct(filepath.Join(dirs.Bin, exeName(engine)), project, env, "show -json", extra...)
	if err != nil {
		s.Fatalf("show -json: %v", err)
	}
	return parseShow(s, out)
}

func parseShow(s *Scenario, out []byte) map[string]StateObject {
	type module struct {
		Resources []struct {
			Address string         `json:"address"`
			Mode    string         `json:"mode"`
			Values  map[string]any `json:"values"`
		} `json:"resources"`
		Children []json.RawMessage `json:"child_modules"`
	}
	var doc struct {
		Values *struct {
			Root module `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		s.Fatalf("show -json: %v", err)
	}
	objs := map[string]StateObject{}
	if doc.Values == nil {
		return objs
	}
	var walk func(m module)
	walk = func(m module) {
		for _, r := range m.Resources {
			if r.Mode == "managed" {
				objs[r.Address] = StateObject{Address: r.Address, ID: fmt.Sprint(r.Values["id"])}
			}
		}
		for _, c := range m.Children {
			var child module
			if json.Unmarshal(c, &child) == nil {
				walk(child)
			}
		}
	}
	walk(doc.Values.Root)
	return objs
}

// ImportPreview is the part of POST /api/v1/imports/preview the harness reads.
type ImportPreview struct {
	Address       string `json:"address"`
	File          string `json:"file"`
	GeneratedFile string `json:"generatedFile"`
	SHA256        string `json:"sha256"`
	Blocked       bool   `json:"blocked"`
	Issues        []struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
	} `json:"issues"`
}

// ImportRequest is the import dialog's request.
func ImportRequest(address, id string, generate bool) map[string]any {
	return map[string]any{"address": address, "id": id, "provider": "", "file": "imports.tf", "generateConfig": generate}
}

// PreviewImport asks for the side-effect-free import preview.
func (s *Scenario) PreviewImport(h *Hclaro, req map[string]any) ImportPreview {
	var p ImportPreview
	if r, err := h.client.Do("POST", "imports/preview", req, &p); err != nil || r.Status != 200 {
		s.Fatalf("import preview: %v %d %s", err, r.Status, r.Code)
	}
	return p
}

// StartImport sends the reviewed import (both acknowledgements) and returns the reply.
func (s *Scenario) StartImport(h *Hclaro, req map[string]any, p ImportPreview, varFiles []string) (Operation, Response) {
	body := map[string]any{"varFiles": varFiles, "previewSha256": p.SHA256, "acknowledgeWrite": true, "acknowledgeBackendAccess": true}
	if h.view != nil {
		body["contextFingerprint"] = s.fingerprint(h, "imports/options")
	}
	for k, v := range req {
		body[k] = v
	}
	var op Operation
	r, err := h.client.Do("POST", "imports", body, &op)
	if err != nil {
		s.Fatalf("import: %v", err)
	}
	return op, r
}

// ImportPlan runs the import plan to its end and reads the plan and its change details.
func (s *Scenario) ImportPlan(h *Hclaro, project, engine string, req map[string]any, p ImportPreview, varFiles []string) PlanDetail {
	s.Guard(project)
	op, r := s.StartImport(h, req, p, varFiles)
	if r.Status != 202 {
		s.Fatalf("import refused: %d %s", r.Status, r.Code)
	}
	done := s.FollowOp(h, op.ID, 15*time.Minute)
	if done.Status != "succeeded" || done.PlanID == "" {
		code := ""
		if done.Diagnostic != nil {
			code = done.Diagnostic.Code
		}
		s.Fatalf("import plan ended %q (%s)", done.Status, code)
	}
	var detail PlanDetail
	r, err := h.client.Do("GET", "plans/"+done.PlanID, nil, &detail)
	if err != nil || r.Status != 200 {
		s.Fatalf("GET plan: %v %d", err, r.Status)
	}
	detail.Raw = r.Body
	s.AssertEngine(project, engine, detail.Engine, detail.EngineVersion)
	for _, c := range detail.Changes {
		if r, err := h.client.Do("GET", "plans/"+done.PlanID+"/changes/"+c.ID, nil, nil); err != nil || r.Status != 200 {
			s.Fatalf("change detail %s: %v %d", c.Address, err, r.Status)
		}
	}
	return detail
}

// WorkspaceReview is the part of POST projects/{id}/workspaces/review the harness reads.
type WorkspaceReview struct {
	Workspaces []string `json:"workspaces"`
	CLIDefault string   `json:"cliDefault"`
	Command    []string `json:"command"`
	Empty      *bool    `json:"empty"`
	CanRun     bool     `json:"canRun"`
	Reason     string   `json:"reason"`
	Code       string   `json:"reasonCode"`
	ContextID  string   `json:"contextId"`
	Launch     bool     `json:"launch"`
	Notices    []string `json:"notices"`
}

// ReviewWorkspace asks for the review of a workspace command without running it.
func (s *Scenario) ReviewWorkspace(h *Hclaro, projectID, action, name string) (WorkspaceReview, Response) {
	var rv WorkspaceReview
	r, err := h.client.Do("POST", "projects/"+projectID+"/workspaces/review", map[string]string{"action": action, "name": name, "profileId": ""}, &rv)
	if err != nil {
		s.Fatalf("workspace review: %v", err)
	}
	return rv, r
}

// HubWorkspace reviews a workspace command and, when the review allows it, runs it and
// follows it to its end. A review that cannot run returns a zero operation.
func (s *Scenario) HubWorkspace(h *Hclaro, projectID, action, name string) (WorkspaceReview, Operation, Response) {
	rv, r := s.ReviewWorkspace(h, projectID, action, name)
	if r.Status != 200 || !rv.CanRun {
		return rv, Operation{}, r
	}
	var op Operation
	r, err := h.client.Do("POST", "projects/"+projectID+"/operations/workspace", map[string]string{"action": action, "name": name, "expectedContextId": rv.ContextID, "profileId": ""}, &op)
	if err != nil {
		s.Fatalf("workspace %s: %v", action, err)
	}
	if r.Status != 202 {
		return rv, Operation{}, r
	}
	return rv, s.FollowOp(h, op.ID, 5*time.Minute), r
}

// InitStatus is the part of GET projects/{id}/init-status the harness reads.
type InitStatus struct {
	Initialized bool     `json:"initialized"`
	Lockfile    bool     `json:"lockfile"`
	Command     []string `json:"command"`
	CanInit     bool     `json:"canInit"`
	Reason      string   `json:"reason"`
	ContextID   string   `json:"contextId"`
}

// HubInit reviews the init preflight, starts the standard init and follows it to its end.
func (s *Scenario) HubInit(h *Hclaro, projectID string) (InitStatus, Operation) {
	var st InitStatus
	if r, err := h.client.Do("GET", "projects/"+projectID+"/init-status", nil, &st); err != nil || r.Status != 200 {
		s.Fatalf("init-status: %v %d %s", err, r.Status, r.Code)
	}
	if !st.CanInit {
		s.Fatalf("init unavailable: %s", st.Reason)
	}
	var op Operation
	r, err := h.client.Do("POST", "projects/"+projectID+"/operations/init", map[string]string{"mode": "standard", "expectedContextId": st.ContextID, "profileId": ""}, &op)
	if err != nil || r.Status != 202 {
		s.Fatalf("init: %v %d %s", err, r.Status, r.Code)
	}
	return st, s.FollowOp(h, op.ID, 10*time.Minute)
}

// OpenContext opens a Hub read context on a workspace, waits for its read and returns the
// managed addresses it shows.
func (s *Scenario) OpenContext(h *Hclaro, projectID, workspace string) []string {
	_, addrs := s.OpenView(h, projectID, workspace, "")
	return addrs
}

// OpenView is OpenContext with an execution profile ("" for none); it also returns the
// context ID.
func (s *Scenario) OpenView(h *Hclaro, projectID, workspace, profileID string) (string, []string) {
	var c struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace"`
	}
	if r, err := h.client.Do("POST", "contexts", map[string]any{"projectId": projectID, "workspace": workspace, "reload": true, "profileId": profileID}, &c); err != nil || r.Status != 200 {
		s.Fatalf("open context %s: %v %d %s", workspace, err, r.Status, r.Code)
	}
	if c.Workspace != workspace {
		s.Fatalf("context opened on %q, asked %q", c.Workspace, workspace)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var ws struct {
			Status string `json:"status"`
		}
		if r, err := h.client.Do("GET", "contexts/"+c.ID, nil, &ws); err != nil || r.Status != 200 {
			s.Fatalf("context: %v %d", err, r.Status)
		}
		if ws.Status != "loading" {
			s.Observe("context %s: %s", workspace, ws.Status)
			break
		}
		if time.Now().After(deadline) {
			s.Fatalf("context %s still loading after 2 minutes", workspace)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var out struct {
		Resources []struct {
			Address string `json:"address"`
		} `json:"resources"`
	}
	r, err := h.client.Do("GET", "contexts/"+c.ID+"/resources?mode=managed&limit=200", nil, &out)
	if err != nil {
		s.Fatalf("context resources: %v", err)
	}
	var addrs []string
	if r.Status == 200 {
		for _, x := range out.Resources {
			addrs = append(addrs, x.Address)
		}
	}
	return c.ID, addrs
}

// waitRunning polls the operation (60 s) until it is running.
func waitRunning(s *Scenario, h *Hclaro, id string) Operation {
	deadline := time.Now().Add(60 * time.Second)
	for {
		var op Operation
		if r, err := h.client.Do("GET", "operations/"+id, nil, &op); err != nil || r.Status != 200 {
			s.Fatalf("operation: %v %d", err, r.Status)
		}
		if op.Status == "running" {
			return op
		}
		if op.Status != "queued" && op.Status != "starting" || time.Now().After(deadline) {
			s.Fatalf("apply %s, not running (60 s)", op.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// shutdownNotice is the notice Hclaro prints when stopped during an apply.
const shutdownNotice = "An apply is running. Hclaro interrupts it once"

// engineAck is the engine's interrupt acknowledgement in its log (-json "log" message).
const engineAck = "Interrupt received"

// historyFile reads the final apply entry for planID from history.json in the config dir
// (read-only, after Hclaro exited).
func historyFile(s *Scenario, config, planID string) HistoryEntry {
	var found *HistoryEntry
	_ = filepath.WalkDir(config, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "history.json" {
			return nil
		}
		b, err := os.ReadFile(p)
		var doc struct {
			Entries []HistoryEntry `json:"entries"`
		}
		if err != nil || json.Unmarshal(b, &doc) != nil {
			return nil
		}
		for i := range doc.Entries {
			if e := doc.Entries[i]; e.Kind == "apply" && e.Outcome.PlanID == planID {
				found = &e
			}
		}
		return nil
	})
	if found == nil {
		s.Fatalf("history.json has no apply entry for the plan")
	}
	return *found
}

func exitCode(c *int) string {
	if c == nil {
		return "absent"
	}
	return fmt.Sprint(*c)
}

func stdoutHas(h *Hclaro, text string) bool {
	b, _ := os.ReadFile(h.Stdout)
	return bytes.Contains(b, []byte(text))
}

// excerptAcks counts the acknowledgement in a history entry's log excerpt.
func excerptAcks(e HistoryEntry) int {
	n := 0
	for _, l := range e.Excerpt {
		n += strings.Count(l.Text, engineAck)
	}
	return n
}
