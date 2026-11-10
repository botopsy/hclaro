package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// Tests for Hub-scoped import and replace (M24.08). The synthetic CLI (scripts/fake-engine)
// plans and "applies" in temporary roots only. Nothing contacts a real backend or provider.

type scopedImportPreview struct {
	imports.Preview
	Scope projects.ScopeView `json:"scope"`
}

const importRequest = `{"address":"aws_s3_bucket.logs","id":"synthetic-logs","file":"imports.tf"}`

func (s *scopedServer) importOptions(cid string) map[string]any {
	s.t.Helper()
	w := s.do("GET", "/api/v1/contexts/"+cid+"/imports/options", "")
	if w.Code != 200 {
		s.t.Fatalf("import options: %d %s", w.Code, w.Body)
	}
	return decode[map[string]any](s.t, w)
}

func (s *scopedServer) previewImport(cid, body string) scopedImportPreview {
	s.t.Helper()
	w := s.do("POST", "/api/v1/contexts/"+cid+"/imports/preview", body)
	if w.Code != 200 {
		s.t.Fatalf("import preview: %d %s", w.Code, w.Body)
	}
	return decode[scopedImportPreview](s.t, w)
}

func importStart(preview scopedImportPreview, fingerprint string) string {
	var req map[string]any
	_ = json.Unmarshal([]byte(importRequest), &req)
	req["previewSha256"], req["acknowledgeWrite"], req["acknowledgeBackendAccess"], req["contextFingerprint"] = preview.SHA256, true, true, fingerprint
	b, _ := json.Marshal(req)
	return string(b)
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s was written", path)
	}
}

// An import in a Hub workspace writes the reviewed block into that project's root only,
// plans there with its own workspace and no -target, and is recorded only by applying the
// reviewed saved plan through the scoped approval.
func TestScopedImportWritesOnlyInItsRootAndAppliesTheReviewedPlan(t *testing.T) {
	s := newScopedServer(t)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("platform", "default", "")
	o := s.importOptions(a)
	if o["canImport"] != true || o["workspace"] != "dev" || o["scope"].(map[string]any)["contextId"] != a {
		t.Fatalf("options: %v", o)
	}
	preview := s.previewImport(a, importRequest)
	if preview.Blocked || preview.Scope.ContextID != a || preview.Content == "" {
		t.Fatalf("preview: %+v", preview)
	}
	file := filepath.Join(s.roots["network"], "imports.tf")
	absent(t, file)

	w := s.do("POST", "/api/v1/contexts/"+a+"/imports", importStart(preview, ""))
	if w.Code != 202 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	op := decode[scopedOp](t, w)
	if op.Scope.ContextID != a || op.Import == nil || op.Import.Address != "aws_s3_bucket.logs" {
		t.Fatalf("operation: %+v", op)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != preview.Content {
		t.Fatalf("written file differs from the preview: %q %v", got, err)
	}
	absent(t, filepath.Join(s.roots["platform"], "imports.tf"))
	absent(t, filepath.Join(s.launch.Root(), "imports.tf"))

	done := s.waitScoped(a, op.ID)
	if done.Status != operations.Succeeded || done.PlanID == "" {
		t.Fatalf("plan: %+v", done)
	}
	plans := s.calls("plan")
	if len(plans) != 1 {
		t.Fatalf("plan calls: %v", plans)
	}
	cwd, _ := filepath.EvalSymlinks(spyField(plans[0], "CWD="))
	if cwd != s.roots["network"] || spyField(plans[0], "TF_WORKSPACE=") != "dev" {
		t.Fatalf("plan ran in %s/%s", cwd, spyField(plans[0], "TF_WORKSPACE="))
	}
	for _, arg := range plans[0] {
		if strings.HasPrefix(arg, "-target") || strings.HasPrefix(arg, "-replace") || arg == "import" {
			t.Fatalf("unexpected plan argument %q", arg)
		}
	}
	detail := decode[scopedPlanDetail](t, s.do("GET", "/api/v1/contexts/"+a+"/plans/"+done.PlanID, ""))
	if detail.Imports == 0 || detail.Import == nil || !detail.Apply.Available {
		t.Fatalf("plan detail: imports=%d import=%+v apply=%+v", detail.Imports, detail.Import, detail.Apply)
	}
	// Not listed in the other context or the launch project.
	if w := s.do("GET", "/api/v1/contexts/"+b+"/plans/"+done.PlanID, ""); w.Code != 404 {
		t.Fatalf("plan visible in another context: %d", w.Code)
	}
	if len(s.calls("apply")) != 0 {
		t.Fatal("applied without approval")
	}
	approval := s.approve(a, done.PlanID)
	if approval.Phrase != "apply dev" {
		t.Fatalf("an import writes state; the phrase must be typed: %+v", approval)
	}
	w = s.do("POST", "/api/v1/contexts/"+a+"/plans/"+done.PlanID+"/apply", applyBody(approval))
	if w.Code != 202 {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	if applied := s.waitScoped(a, decode[scopedOp](t, w).ID); applied.Status != operations.Succeeded {
		t.Fatalf("apply: %+v", applied)
	}
	applies := s.calls("apply")
	if len(applies) != 1 {
		t.Fatalf("apply calls: %v", applies)
	}
	cwd, _ = filepath.EvalSymlinks(spyField(applies[0], "CWD="))
	if cwd != s.roots["network"] || spyField(applies[0], "TF_WORKSPACE=") != "dev" {
		t.Fatalf("apply ran in %s/%s", cwd, spyField(applies[0], "TF_WORKSPACE="))
	}

	// The file now exists: a second preview is blocked and a start writes nothing.
	again := s.previewImport(a, importRequest)
	if !again.Blocked || !hasIssue(again.Preview, "file_exists") {
		t.Fatalf("existing file not detected: %+v", again.Issues)
	}
}

func hasIssue(p imports.Preview, code string) bool {
	for _, i := range p.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

// A preview of one workspace cannot start an import in another workspace or project, and a
// project changed after the preview is refused. Nothing is written anywhere.
func TestScopedImportWrongContextOrChangedProjectWritesNothing(t *testing.T) {
	s := newScopedServer(t)
	a := s.openLoaded("network", "dev", "")
	prod := s.openLoaded("network", "prod", "")
	b := s.openLoaded("platform", "default", "")
	preview := s.previewImport(a, importRequest)

	for name, cid := range map[string]string{"other workspace, same root": prod, "other project": b} {
		w := s.do("POST", "/api/v1/contexts/"+cid+"/imports", importStart(preview, ""))
		if w.Code != 409 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The launch import route does not accept a scope's preview either.
	if w := s.do("POST", "/api/v1/imports", importStart(preview, "")); w.Code == 202 {
		t.Fatal("launch route accepted a scoped preview")
	}
	for _, root := range []string{s.roots["network"], s.roots["platform"], s.launch.Root()} {
		absent(t, filepath.Join(root, "imports.tf"))
	}

	// The project folder is replaced after the preview: refused, the context is closed and
	// neither folder gets the file.
	moved := s.roots["network"] + "-moved"
	if err := os.Rename(s.roots["network"], moved); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(s.roots["network"], os.DirFS(moved)); err != nil {
		t.Fatal(err)
	}
	w := s.do("POST", "/api/v1/contexts/"+a+"/imports", importStart(preview, ""))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "scope_changed") {
		t.Fatalf("changed project: %d %s", w.Code, w.Body)
	}
	absent(t, filepath.Join(s.roots["network"], "imports.tf"))
	absent(t, filepath.Join(moved, "imports.tf"))
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/imports/preview", importRequest), 404, "context_closed")
	if len(s.calls("plan")) != 0 {
		t.Fatal("engine planned")
	}
}

// The import start validates its body like the launch route and accepts no target choice.
func TestScopedImportRequestValidation(t *testing.T) {
	s := newScopedServer(t)
	a := s.openLoaded("network", "dev", "")
	preview := s.previewImport(a, importRequest)
	start := importStart(preview, "")
	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"no write ack":       {strings.Replace(start, `"acknowledgeWrite":true`, `"acknowledgeWrite":false`, 1), 400, "acknowledgement_required"},
		"no backend ack":     {strings.Replace(start, `"acknowledgeBackendAccess":true,`, ``, 1), 400, "acknowledgement_required"},
		"profile id":         {strings.Replace(start, `{`, `{"profileId":"x",`, 1), 400, "invalid_request"},
		"discovery token":    {strings.Replace(start, `{`, `{"discoveryToken":"x",`, 1), 409, "discovery_stale"},
		"root path":          {strings.Replace(start, `{`, `{"rootPath":"/tmp",`, 1), 400, "invalid_request"},
		"workspace":          {strings.Replace(start, `{`, `{"workspace":"prod",`, 1), 400, "invalid_request"},
		"wrong fingerprint":  {strings.Replace(start, `"contextFingerprint":""`, `"contextFingerprint":"x"`, 1), 409, "context_changed"},
		"edited after check": {strings.Replace(start, `synthetic-logs`, `synthetic-other`, 1), 409, "preview_changed"},
		"path traversal":     {strings.Replace(start, `imports.tf`, `../imports.tf`, 1), 409, "import_blocked"},
		"var file traversal": {strings.Replace(start, `{`, `{"varFiles":["../x.tfvars"],`, 1), 400, "invalid_var_files"},
	}
	for name, c := range cases {
		wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/imports", c.body), c.status, c.code)
		_ = name
	}
	absent(t, filepath.Join(s.roots["network"], "imports.tf"))
	absent(t, filepath.Join(filepath.Dir(s.roots["network"]), "imports.tf"))
	if len(s.calls("plan")) != 0 {
		t.Fatal("engine planned")
	}
	// Gate off: every import route answers scoped_execution_disabled first.
	s.hub.ScopedExecution = false
	wantError(t, s.do("GET", "/api/v1/contexts/"+a+"/imports/options", ""), 404, projects.CodeScopedDisabled)
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/imports/preview", importRequest), 404, projects.CodeScopedDisabled)
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/imports", start), 404, projects.CodeScopedDisabled)
}

// Replacement in a Hub workspace takes this context's resource IDs only: 0, 1 and 50 are
// accepted, 51 and IDs of another context are refused, and the plan is a full plan (one
// -replace per instance, never -target) that also shows unrelated pending changes.
func TestScopedReplaceUsesOnlyThisContextsResources(t *testing.T) {
	s := newScopedServer(t)
	// 60 managed instances in network/dev's synthetic state.
	type res = map[string]any
	many := []res{}
	for i := range 60 {
		many = append(many, res{"address": fmt.Sprintf("aws_s3_bucket.b%02d", i), "mode": "managed", "type": "aws_s3_bucket", "name": fmt.Sprintf("b%02d", i), "provider_name": "registry.terraform.io/hashicorp/aws", "values": res{"id": fmt.Sprintf("b%02d", i)}, "sensitive_values": res{}})
	}
	doc, _ := json.Marshal(res{"format_version": "1.0", "values": res{"root_module": res{"resources": many}}})
	if err := os.WriteFile(filepath.Join(s.roots["network"], ".fake-state", "dev.json"), doc, 0o644); err != nil {
		t.Fatal(err)
	}
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("platform", "default", "")
	ids := func(cid string, n int) []string {
		page := decode[struct {
			Resources []struct{ ID, Address string } `json:"resources"`
		}](t, s.do("GET", "/api/v1/contexts/"+cid+"/resources?limit=100", ""))
		out := []string{}
		for _, r := range page.Resources[:min(n, len(page.Resources))] {
			out = append(out, r.ID)
		}
		return out
	}
	start := func(cid string, replace []string) (int, scopedOp) {
		fp := s.options(cid).ContextFingerprint
		list, _ := json.Marshal(replace)
		w := s.do("POST", "/api/v1/contexts/"+cid+"/plans", fmt.Sprintf(`{"acknowledgeBackendAccess":true,"contextFingerprint":%q,"replace":%s}`, fp, list))
		if w.Code != 202 {
			return w.Code, scopedOp{}
		}
		return w.Code, decode[scopedOp](t, w)
	}
	all := ids(a, 60)
	if len(all) != 60 {
		t.Fatalf("resources: %d", len(all))
	}
	// 51 and another context's IDs: refused before anything runs.
	if code, _ := start(a, all[:51]); code != 400 {
		t.Fatalf("51 accepted: %d", code)
	}
	if code, _ := start(a, ids(b, 1)); code != 400 {
		t.Fatalf("another context's resource accepted: %d", code)
	}
	if len(s.calls("plan")) != 0 {
		t.Fatal("engine planned")
	}
	for i, n := range []int{0, 1, 50} {
		code, op := start(a, all[:n])
		if code != 202 || len(op.Replace) != n {
			t.Fatalf("%d: %d %+v", n, code, op)
		}
		done := s.waitScoped(a, op.ID)
		if done.Status != operations.Succeeded {
			t.Fatalf("%d: %+v", n, done)
		}
		argv := s.calls("plan")[i]
		replaced := 0
		for _, arg := range argv {
			if strings.HasPrefix(arg, "-target") {
				t.Fatalf("-target in %v", argv)
			}
			if strings.HasPrefix(arg, "-replace=aws_s3_bucket.b") {
				replaced++
			}
		}
		if replaced != n {
			t.Fatalf("%d -replace arguments, want %d: %v", replaced, n, argv)
		}
		// The review is the full plan: changes to instances not selected are included.
		detail := decode[scopedPlanDetail](t, s.do("GET", "/api/v1/contexts/"+a+"/plans/"+done.PlanID, ""))
		unrelated := 0
		for _, c := range detail.Review.Changes {
			if !strings.HasPrefix(c.Address, "aws_s3_bucket.b") {
				unrelated++
			}
		}
		if unrelated == 0 {
			t.Fatalf("%d: review hides unrelated changes", n)
		}
	}
}
