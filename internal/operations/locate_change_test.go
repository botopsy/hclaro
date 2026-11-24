package operations

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/gitctx"
	"hclaro/internal/plan"
	"hclaro/internal/workspace"
)

func putFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func changeID(t *testing.T, p *Plan, address string) string {
	t.Helper()
	for _, c := range append(append([]plan.Change{}, p.Changes...), p.Drift...) {
		if c.Address == address {
			return c.ID
		}
	}
	t.Fatalf("no change %s", address)
	return ""
}

func TestLocateChange(t *testing.T) {
	m, root := setup(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	m.Git = gitctx.New()
	putFile(t, filepath.Join(root, "main.tf"), "resource \"aws_s3_bucket\" \"logs\" {\n}\n\ndata \"aws_caller_identity\" \"current\" {\n}\n")
	putFile(t, filepath.Join(root, "mod", "main.tf"), "\n\nresource \"aws_subnet\" \"main\" {\n}\n")
	putFile(t, filepath.Join(root, ".terraform", "modules", "modules.json"), `{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"network","Source":"./mod","Dir":"mod"}]}`)
	p, _ := m.Plan(planned(t, m))

	// A planned create is not in state; it is found from the stored change.
	ref, err := m.LocateChange(p, changeID(t, p, "aws_s3_bucket.logs"))
	if err != nil || ref.State != "found" || ref.Path != "main.tf" || ref.Line != 1 || ref.FileID == "" {
		t.Fatalf("create %+v %v", ref, err)
	}
	// A data change uses the same lookup.
	if ref, _ := m.LocateChange(p, changeID(t, p, "data.aws_caller_identity.current")); ref.State != "found" || ref.BlockType != "data" || ref.Line != 4 {
		t.Fatalf("data %+v", ref)
	}
	// A module change resolves inside the module directory.
	if ref, _ := m.LocateChange(p, changeID(t, p, "module.network.aws_subnet.main[0]")); ref.State != "found" || ref.Path != "mod/main.tf" || ref.Line != 3 {
		t.Fatalf("module %+v", ref)
	}
	// A block that is not in the files is "not found" with its reason.
	if ref, err := m.LocateChange(p, changeID(t, p, "aws_iam_role.legacy")); err != nil || ref.State != "not_found" || ref.Reason == "" {
		t.Fatalf("missing %+v %v", ref, err)
	}
	if _, err := m.LocateChange(p, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrChangeNotFound) {
		t.Fatalf("unknown change: %v", err)
	}
	m.Git = nil
	if _, err := m.LocateChange(p, changeID(t, p, "aws_s3_bucket.logs")); !errors.Is(err, ErrNoGit) {
		t.Fatalf("no git: %v", err)
	}
}
