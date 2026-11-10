package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
)

func TestDiscoveryAndLoad(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TF_DATA_DIR", ".terraform")
	if _, err := Root(root); err == nil {
		t.Fatal("non-project accepted")
	}
	_ = os.WriteFile(filepath.Join(root, "main.tf"), []byte("terraform {}"), 0600)
	if _, err := Root(root); err != nil {
		t.Fatal(err)
	}
	info := Inspect(root)
	if info.Initialized {
		t.Fatal("initialized")
	}
	mock := &engine.Mock{VersionValue: "1.9.8", WorkspaceValue: "demo", JSON: fixtures.State}
	s := New(info)
	s.Load(context.Background(), mock, "terraform")
	if s.Get().Diagnostic.Code != "uninitialized" || len(mock.Calls) != 1 {
		t.Fatal(s.Get())
	}
	_ = os.Mkdir(filepath.Join(root, ".terraform"), 0700)
	_ = os.WriteFile(filepath.Join(root, ".terraform", "terraform.tfstate"), []byte(`{"backend":{"type":"s3","config":{"secret":"DO_NOT_LEAK_HCLARO_TEST_SECRET"}}}`), 0600)
	info = Inspect(root)
	if info.Backend != "s3" || !info.Initialized || info.ExecutionMode != "local-cli" {
		t.Fatal(info)
	}
	s = New(info)
	s.Load(context.Background(), mock, "terraform")
	if s.Get().Status != "ready" || len(s.Get().Index.Resources) != 316 {
		t.Fatal(s.Get().Status)
	}
	mock.JSON = fixtures.Empty
	s.Load(context.Background(), mock, "terraform")
	if s.Get().Status != "empty" {
		t.Fatal(s.Get().Status)
	}
	mock.JSON = []byte(`{"format_version":"1.0"}`)
	s.Load(context.Background(), mock, "terraform")
	if s.Get().Diagnostic.Code != "state_absent" || s.Get().Info.Capabilities.CanReadState {
		t.Fatal("missing state was treated as an empty snapshot")
	}
	mock.JSON = fixtures.State
	s.Load(context.Background(), mock, "terraform")
	if s.Get().Diagnostic != nil || s.Get().Status != "ready" {
		t.Fatal("successful read retained an old error")
	}
	s = New(info)
	s.Load(context.Background(), nil, "")
	if s.Get().Diagnostic.Code != "missing_cli" {
		t.Fatal(s.Get())
	}
	mock.Err = errors.New("DO_NOT_LEAK_HCLARO_TEST_SECRET")
	s.Load(context.Background(), mock, "terraform")
	if s.Get().Diagnostic.Code != "engine_version" {
		t.Fatal(s.Get())
	}
}

type brokenState struct{ engine.Mock }

func (m *brokenState) State(context.Context) ([]byte, error) {
	return nil, errors.New("DO_NOT_LEAK_HCLARO_TEST_SECRET")
}
func TestUnavailableDistinctFromEmpty(t *testing.T) {
	s := New(Info{Initialized: true})
	m := &brokenState{engine.Mock{VersionValue: "1.9.8", WorkspaceValue: "demo"}}
	s.Load(context.Background(), m, "terraform")
	if s.Get().Status != "error" || s.Get().Diagnostic.Code != "state_unavailable" {
		t.Fatal(s.Get())
	}
}

func TestImplicitLocalBackend(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	for name, tc := range map[string]struct {
		file, content, backend string
	}{
		"no backend block":      {"main.tf", `resource "terraform_data" "cloud" {}`, "local"},
		"backend block":         {"backend.tf", "terraform {\n  backend \"s3\" {}\n}", "unknown"},
		"one-line backend":      {"main.tf", `terraform { backend "gcs" {} }`, "unknown"},
		"cloud block":           {"main.tf", "terraform {\n  cloud {\n    organization = \"x\"\n  }\n}", "unknown"},
		"JSON backend":          {"main.tf.json", `{"terraform": {"backend": {"s3": {}}}}`, "unknown"},
		"commented-out backend": {"main.tf", "# backend \"s3\" {}\nterraform {}", "unknown"},
	} {
		root := t.TempDir()
		_ = os.WriteFile(filepath.Join(root, tc.file), []byte(tc.content), 0o600)
		_ = os.Mkdir(filepath.Join(root, ".terraform"), 0o700)
		info := Inspect(root)
		want := map[string]string{"local": "local-cli", "unknown": "unsupported"}[tc.backend]
		if info.Backend != tc.backend || info.ExecutionMode != want {
			t.Errorf("%s: %s/%s", name, info.Backend, info.ExecutionMode)
		}
	}
	// Not initialized: no backend is assumed.
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.tf"), []byte(`terraform {}`), 0o600)
	if info := Inspect(root); info.Backend != "unknown" {
		t.Fatal(info.Backend)
	}
}

// TestBackendClassification pins how initialized backend metadata maps to an execution mode
// and to plan/apply/import capabilities, through an execution scope that would otherwise
// allow every operation. Only local-execution backends may plan; remote/cloud execution and
// anything unrecognized or unreadable stays read-only (support matrix, docs/support-matrix.md).
func TestBackendClassification(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	for _, tc := range []struct{ name, metadata, backend, mode string }{
		{"local", `{"backend":{"type":"local"}}`, "local", "local-cli"},
		{"s3", `{"backend":{"type":"s3","config":{"bucket":"b"}}}`, "s3", "local-cli"},
		{"azurerm", `{"backend":{"type":"azurerm"}}`, "azurerm", "local-cli"},
		{"gcs", `{"backend":{"type":"gcs"}}`, "gcs", "local-cli"},
		{"http", `{"backend":{"type":"http"}}`, "http", "local-cli"},
		{"remote", `{"backend":{"type":"remote"}}`, "remote", "remote-exec"},
		{"cloud", `{"backend":{"type":"cloud"}}`, "cloud", "remote-exec"},
		{"unlisted type", `{"backend":{"type":"artifactory"}}`, "unknown", "unsupported"},
		{"case variant", `{"backend":{"type":"Cloud"}}`, "unknown", "unsupported"},
		{"empty type", `{"backend":{"type":""}}`, "unknown", "unsupported"},
		{"no backend key", `{"version":3}`, "unknown", "unsupported"},
		{"malformed", `{"backend":`, "unknown", "unsupported"},
		{"directory", "", "unknown", "unsupported"},
	} {
		root := t.TempDir()
		_ = os.WriteFile(filepath.Join(root, "main.tf"), []byte(`terraform {}`), 0o600)
		meta := filepath.Join(root, ".terraform", "terraform.tfstate")
		_ = os.Mkdir(filepath.Dir(meta), 0o700)
		if tc.name == "directory" {
			_ = os.Mkdir(meta, 0o700) // not a regular file: never read, never treated as local
		} else {
			_ = os.WriteFile(meta, []byte(tc.metadata), 0o600)
		}
		info := Inspect(root)
		if info.Backend != tc.backend || info.ExecutionMode != tc.mode {
			t.Errorf("%s: classified %s/%s, want %s/%s", tc.name, info.Backend, info.ExecutionMode, tc.backend, tc.mode)
			continue
		}
		s := NewScoped(info, Scope{Workspace: "default", Executable: true})
		s.Load(context.Background(), &engine.Mock{VersionValue: "1.9.8", WorkspaceValue: "default", JSON: fixtures.State}, "terraform")
		caps := s.Get().Info.Capabilities
		local := tc.mode == "local-cli"
		if caps.CanPlan != local || caps.CanApply != local || caps.CanImport != local {
			t.Errorf("%s: plan=%v apply=%v import=%v, want all %v", tc.name, caps.CanPlan, caps.CanApply, caps.CanImport, local)
		}
		if !local && (caps.PlanReason == "" || caps.ApplyReason == "" || caps.ImportReason == "") {
			t.Errorf("%s: read-only without a reason: %+v", tc.name, caps)
		}
		if !caps.CanReadState {
			t.Errorf("%s: state should stay readable: %+v", tc.name, s.Get().Diagnostic)
		}
	}
}
