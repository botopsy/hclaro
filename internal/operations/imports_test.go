package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/imports"
	"hclaro/internal/rootlock"
	"hclaro/internal/workspace"
)

const legacyBlock = `resource "aws_s3_bucket" "legacy_logs" {
  bucket = "hclaro-legacy-logs"
}
`

// importSetup is setup with a symlink-resolved project root containing files, as
// workspace.Root provides at launch.
func importSetup(t *testing.T, fake *engine.Fake, files map[string]string) (*Manager, string) {
	t.Helper()
	fake.VersionValue, fake.WorkspaceValue, fake.StateJSON = "1.9.8", "demo", fixtures.State
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake.Root = root
	m := NewManager(store, fake, root)
	m.TempDir = t.TempDir()
	t.Cleanup(func() { m.Close(5 * time.Second) })
	return m, root
}

func reviewed(t *testing.T, m *Manager, req imports.Request) ImportRequest {
	t.Helper()
	p := m.PreviewImport(req)
	if p.Blocked {
		t.Fatalf("preview blocked: %+v", p.Issues)
	}
	return ImportRequest{Request: req, PreviewSHA256: p.SHA256}
}

func TestImportWritesReviewedBlockPlansAndApplies(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport}
	m, root := importSetup(t, fake, map[string]string{"main.tf": legacyBlock})
	req := imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"}
	preview := m.PreviewImport(req)
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview wrote the file")
	}
	op, err := m.StartImport(ImportRequest{Request: req, PreviewSHA256: preview.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "imports.tf")); err != nil || string(b) != preview.Content {
		t.Fatalf("written file differs from the preview: %q %v", b, err)
	}
	if op.Import == nil || op.Import.Address != req.Address || strings.Contains(strings.Join(op.Command, " "), "generate-config-out") {
		t.Fatalf("%+v", op)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded {
		t.Fatalf("%+v", done)
	}
	if r := fake.Requests(); len(r) != 1 || r[0].GenerateConfigOut != "" || len(r[0].Replace) != 0 {
		t.Fatalf("%+v", r)
	}
	p, _ := m.Plan(done.PlanID)
	if p.Kind != "import" || p.Imports != 1 || p.Import.File != "imports.tf" || len(p.Warnings) != 0 {
		t.Fatalf("%+v", p)
	}
	// An import is a no-op action that still writes state: it is applicable and needs the phrase.
	if _, status := m.ContextStatus(p); !status.Available {
		t.Fatalf("%+v", status)
	}
	approval, err := approve(m, p.ID)
	if err != nil || approval.Phrase != "apply demo" || approval.Imports != 1 || approval.Import.Address != req.Address {
		t.Fatalf("%+v %v", approval, err)
	}
	if _, err := m.StartApply(p.ID, approval.Token, ""); rejected(err) != "confirmation_mismatch" {
		t.Fatal(err)
	}
	applied := wait(t, m, approveAndApply(t, m, p.ID).ID)
	if applied.Outcome != "applied" || applied.Import == nil || len(fake.Applied()) != 1 {
		t.Fatalf("%+v", applied)
	}
	// The user's import file is kept after the apply; only app-owned temp files are removed.
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); err != nil {
		t.Fatal(err)
	}
	assertTempEmpty(t, m)
}

func TestImportGeneratesConfigurationForReview(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport, GeneratedConfig: fixtures.ImportGenerated}
	m, root := importSetup(t, fake, map[string]string{"main.tf": "terraform {}\n"})
	op, err := m.StartImport(reviewed(t, m, imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "legacy.tf", GenerateConfig: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Join(op.Command, " "), "-generate-config-out=legacy_generated.tf") {
		t.Fatalf("%q", op.Command)
	}
	done := wait(t, m, op.ID)
	if r := fake.Requests(); len(r) != 1 || r[0].GenerateConfigOut != "legacy_generated.tf" {
		t.Fatalf("%+v", r)
	}
	p, _ := m.Plan(done.PlanID)
	assertNoCanary(t, p, done)
	if !strings.Contains(p.GeneratedConfig, `resource "aws_s3_bucket" "legacy_logs"`) || !strings.Contains(p.GeneratedConfig, "[redacted]") {
		t.Fatalf("%q", p.GeneratedConfig)
	}
	// The file on disk is the engine's, unredacted; Hclaro only redacts its display copy.
	if b, _ := os.ReadFile(filepath.Join(root, "legacy_generated.tf")); !strings.Contains(string(b), canary) {
		t.Fatal("generated file was modified")
	}
	// Writing the generated file is expected and does not make the plan stale.
	if _, status := m.ContextStatus(p); !status.Available {
		t.Fatalf("%+v", status)
	}
	// Any later edit does.
	if err := os.WriteFile(filepath.Join(root, "legacy_generated.tf"), []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := approve(m, p.ID); rejected(err) != "context_changed" {
		t.Fatal(err)
	}
}

func TestImportEditDuringPlanIsAContextChange(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport, GeneratedConfig: fixtures.ImportGenerated, Lines: lines(fixtures.PlanLog), Delay: 20 * time.Millisecond}
	m, root := importSetup(t, fake, map[string]string{"main.tf": "terraform {}\n"})
	op, err := m.StartImport(reviewed(t, m, imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "legacy.tf", GenerateConfig: true}))
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, m, op.ID, "Planning")
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("terraform {}\n# edited while planning\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _ := m.Plan(wait(t, m, op.ID).PlanID)
	if _, err := approve(m, p.ID); rejected(err) != "context_changed" {
		t.Fatal(err)
	}
}

func TestImportRefusalsWriteNothingAndRunNothing(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport}
	m, root := importSetup(t, fake, map[string]string{"main.tf": legacyBlock})
	good := imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"}
	sha := m.PreviewImport(good).SHA256
	for name, c := range map[string]struct {
		req  ImportRequest
		code string
	}{
		"stale preview":    {ImportRequest{Request: good, PreviewSHA256: strings.Repeat("0", 64)}, "preview_changed"},
		"missing preview":  {ImportRequest{Request: good}, "preview_changed"},
		"changed id":       {ImportRequest{Request: imports.Request{Address: good.Address, ID: "other", File: good.File}, PreviewSHA256: sha}, "preview_changed"},
		"already in state": {ImportRequest{Request: imports.Request{Address: "aws_s3_bucket.artifacts", ID: "x", File: "a.tf"}, PreviewSHA256: sha}, "import_blocked"},
		"no configuration": {ImportRequest{Request: imports.Request{Address: "aws_s3_bucket.other", ID: "x", File: "a.tf"}, PreviewSHA256: sha}, "import_blocked"},
		"traversal":        {ImportRequest{Request: imports.Request{Address: good.Address, ID: good.ID, File: "../a.tf"}, PreviewSHA256: sha}, "import_blocked"},
	} {
		if _, err := m.StartImport(c.req); rejected(err) != c.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := m.StartImport(ImportRequest{Request: good, PreviewSHA256: sha, VarFiles: []string{"missing.tfvars"}}); !errors.Is(err, ErrVarFiles) {
		t.Fatal(err)
	}
	// A file created after the preview is never overwritten.
	if err := os.WriteFile(filepath.Join(root, "imports.tf"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartImport(ImportRequest{Request: good, PreviewSHA256: sha}); rejected(err) != "import_blocked" {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "imports.tf")); string(b) != "# mine\n" {
		t.Fatal("existing file changed")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 2 {
		t.Fatalf("unexpected files: %v", entries)
	}
	if strings.Contains(strings.Join(fake.Calls(), ","), "plan") || len(m.Operations()) != 0 {
		t.Fatalf("engine ran: %v", fake.Calls())
	}
}

func TestImportUnavailableWithoutLocalPlanning(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport}
	fake.VersionValue, fake.WorkspaceValue, fake.StateJSON = "1.9.8", "demo", fixtures.State
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "remote", ExecutionMode: "remote-exec"})
	store.Load(context.Background(), fake, "terraform")
	caps := store.Get().Info.Capabilities
	if caps.CanImport || !caps.SupportsImportBlocks || caps.ImportReason == "" {
		t.Fatalf("%+v", caps)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	m := NewManager(store, fake, root)
	t.Cleanup(func() { m.Close(time.Second) })
	// The preview and recipe still work; starting does not.
	p := m.PreviewImport(imports.Request{Address: "aws_s3_bucket.x", ID: "x", File: "a.tf"})
	if p.Recipe == nil || len(p.Recipe.Commands) == 0 {
		t.Fatalf("%+v", p)
	}
	if _, err := m.StartImport(ImportRequest{Request: imports.Request{Address: "aws_s3_bucket.x", ID: "x", File: "a.tf"}, PreviewSHA256: p.SHA256}); rejected(err) != "import_unavailable" {
		t.Fatal(err)
	}
}

func TestImportPlanThatDoesNotImportWarnsAndFailedPlanKeepsFile(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanCollateral}
	m, root := importSetup(t, fake, map[string]string{"main.tf": legacyBlock})
	op, err := m.StartImport(reviewed(t, m, imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"}))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := m.Plan(wait(t, m, op.ID).PlanID)
	if p.Imports != 0 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "does not import aws_s3_bucket.legacy_logs") {
		t.Fatalf("%+v", p.Warnings)
	}
	// A failed plan never applies and never removes the user's file.
	fake.PlanErr = &engine.ExitError{Code: 1}
	fake.PlanJSON = fixtures.PlanImport
	if err := os.Remove(filepath.Join(root, "imports.tf")); err != nil {
		t.Fatal(err)
	}
	op, err = m.StartImport(reviewed(t, m, imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"}))
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, m, op.ID); done.Status != Failed || done.PlanID != "" || done.Import == nil {
		t.Fatalf("%+v", done)
	}
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); err != nil {
		t.Fatal("import file removed after a failed plan")
	}
	if applyCalls(fake) != 0 {
		t.Fatal("apply ran")
	}
}

// A Hub workspace's manager revalidates its scope with the root claim held, immediately
// before writing: a refusal writes nothing, runs nothing and releases the root. Its preview
// digests are bound to the context, so a launch or other-context digest is refused.
func TestImportVerifiedBeforeWritingAndBoundToItsContext(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport}
	m, root := importSetup(t, fake, map[string]string{"main.tf": legacyBlock})
	m.Locks = rootlock.New()
	req := imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"}
	unbound := m.PreviewImport(req).SHA256
	m.ImportBinding = "context-a"
	bound := m.PreviewImport(req).SHA256
	if bound == unbound || bound == "" {
		t.Fatal("preview digest not bound to the context")
	}
	m.ImportBinding = "context-b"
	if _, err := m.StartImport(ImportRequest{Request: req, PreviewSHA256: bound}); !rejectedWith(err, "preview_changed") {
		t.Fatalf("other context's preview accepted: %v", err)
	}
	m.ImportBinding = "context-a"
	if _, err := m.StartImport(ImportRequest{Request: req, PreviewSHA256: unbound}); !rejectedWith(err, "preview_changed") {
		t.Fatalf("unbound preview accepted: %v", err)
	}
	kinds := []string{}
	m.Verify = func(ctx context.Context, kind string) *Diagnostic {
		kinds = append(kinds, kind)
		if _, held := m.Locks.Holder(root); !held {
			t.Error("verified without the root claim")
		}
		return &Diagnostic{Code: "scope_changed", Message: "The project folder was moved."}
	}
	if _, err := m.StartImport(ImportRequest{Request: req, PreviewSHA256: bound}); !rejectedWith(err, "scope_changed") {
		t.Fatalf("scope change not refused: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "import" {
		t.Fatalf("verify kinds %v", kinds)
	}
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file written after a refused revalidation")
	}
	if len(fake.Requests()) != 0 {
		t.Fatal("engine ran")
	}
	if _, held := m.Locks.Holder(root); held {
		t.Fatal("root claim not released")
	}
	m.Verify = nil
	op, err := m.StartImport(ImportRequest{Request: req, PreviewSHA256: bound})
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, m, op.ID); done.Status != Succeeded {
		t.Fatalf("%+v", done)
	}
}

func rejectedWith(err error, code string) bool {
	var r *RejectError
	return errors.As(err, &r) && r.Code == code
}

// M26.08: an import's scope revalidation (engine version and workspace list, up to minutes)
// runs without the manager lock, so a Hub reading Running is not stalled; the manager reports
// busy meanwhile, and a close during it refuses the import without writing.
func TestImportVerifiesWithoutTheManagerLock(t *testing.T) {
	for _, closeDuring := range []bool{false, true} {
		fake := &engine.Fake{PlanJSON: fixtures.PlanImport}
		m, root := importSetup(t, fake, map[string]string{"main.tf": legacyBlock})
		req := reviewed(t, m, imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"})
		entered, proceed := make(chan struct{}), make(chan struct{})
		m.Verify = func(ctx context.Context, kind string) *Diagnostic {
			if kind == "import" {
				close(entered)
				<-proceed
			}
			return nil
		}
		started := make(chan error, 1)
		go func() {
			_, err := m.StartImport(req)
			started <- err
		}()
		<-entered
		busy := make(chan bool, 1)
		go func() { busy <- m.Running() }()
		select {
		case b := <-busy:
			if !b {
				t.Fatal("not busy while revalidating an import")
			}
		case <-time.After(5 * time.Second):
			close(proceed)
			t.Fatal("Running blocked while an import was revalidated")
		}
		if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrBusy) {
			t.Fatalf("plan during import revalidation: %v", err)
		}
		if closeDuring {
			go m.Close(0)
			for !m.closedNow() {
				time.Sleep(time.Millisecond)
			}
		}
		close(proceed)
		err := <-started
		_, statErr := os.Stat(filepath.Join(root, "imports.tf"))
		if closeDuring {
			if !errors.Is(err, ErrClosed) || !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("closed during revalidation: %v %v", err, statErr)
			}
			continue
		}
		if err != nil || statErr != nil {
			t.Fatalf("%v %v", err, statErr)
		}
		wait(t, m, m.Operations()[0].ID)
	}
}

func (m *Manager) closedNow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
