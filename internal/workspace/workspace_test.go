package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	// A provider means init must install something before the engine can plan.
	_ = os.WriteFile(filepath.Join(root, "main.tf"), []byte("provider \"aws\" {}"), 0600)
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

// TestNoInitNeeded: a root module with no backend or cloud block, no module call and only the
// built-in terraform provider has nothing for init to write, so a successful init leaves no
// .terraform directory (all five M27 engines take the "default local state only" path). It
// counts as initialized with the local backend (M27.03 D-1). Anything that needs init, or that
// the textual scan cannot classify, stays uninitialized.
func TestNoInitNeeded(t *testing.T) {
	t.Setenv("TF_DATA_DIR", "")
	builtin := "variable \"token\" {\n  sensitive = true\n}\n\nresource \"terraform_data\" \"a\" {\n  input = var.token\n}\n\ndata \"terraform_remote_state\" \"up\" {\n  backend = \"local\"\n}\n\noutput \"x\" {\n  value = 1\n}\n"
	for name, c := range map[string]struct {
		file, text string
		want       bool
	}{
		"built-in only":      {"main.tf", builtin, true},
		"empty terraform":    {"main.tofu", "terraform {\n  required_version = \">= 1.5\"\n}\n", true},
		"provider block":     {"main.tf", builtin + "provider \"random\" {}\n", false},
		"required_providers": {"main.tf", "terraform {\n  required_providers {\n    random = { source = \"hashicorp/random\" }\n  }\n}\n", false},
		"provider resource":  {"main.tf", builtin + "resource \"random_id\" \"b\" {\n  byte_length = 4\n}\n", false},
		"provider data":      {"main.tf", "data \"local_file\" \"f\" {\n  filename = \"x\"\n}\n", false},
		"module call":        {"main.tf", builtin + "module \"svc\" {\n  source = \"./svc\"\n}\n", false},
		"backend block":      {"main.tf", builtin + "terraform {\n  backend \"local\" {}\n}\n", false},
		"cloud block":        {"main.tf", "terraform {\n  cloud {}\n}\n", false},
		"json config":        {"main.tf.json", `{"resource":{"terraform_data":{"a":{}}}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_ = os.WriteFile(filepath.Join(root, c.file), []byte(c.text), 0o600)
			info := Inspect(root)
			if info.Initialized != c.want || NeedsInit(root) == c.want {
				t.Fatalf("initialized %v, want %v", info.Initialized, c.want)
			}
			if c.want && (info.Backend != "local" || info.ExecutionMode != "local-cli") {
				t.Fatalf("backend %s %s, want local local-cli", info.Backend, info.ExecutionMode)
			}
		})
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

type failingState struct {
	engine.Mock
	err error
}

func (m *failingState) State(context.Context) ([]byte, error) { return nil, m.err }

// A read blocked by another process's state lock is state_locked, never credentials or
// network advice, and planning stays off; other read failures keep state_unavailable.
func TestLockedStateRead(t *testing.T) {
	for err, code := range map[error]string{
		engine.ErrReadStateLocked:  "state_locked",
		engine.ErrReadCredentials:  "state_unavailable",
		engine.ErrReadNetwork:      "state_unavailable",
		engine.ErrReadInitRequired: "state_unavailable",
		errors.New("generic"):      "state_unavailable",
	} {
		s := New(Info{Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
		s.Load(context.Background(), &failingState{engine.Mock{VersionValue: "1.13.9", WorkspaceValue: "default"}, err}, "tofu")
		snap := s.Get()
		caps := snap.Info.Capabilities
		if snap.Status != "error" || snap.Diagnostic == nil || snap.Diagnostic.Code != code || caps.CanPlan || caps.CanReadState || caps.CanApply || caps.CanImport || snap.Locked() != (code == "state_locked") {
			t.Fatalf("%v: %+v", err, snap)
		}
		msg := snap.Diagnostic.Message
		if code == "state_locked" && (!strings.Contains(msg, "holds the state lock") || strings.Contains(msg, "credentials") || strings.Contains(msg, "VPN") || !strings.Contains(caps.PlanReason, "state lock") || !strings.Contains(caps.ApplyReason, "state lock")) {
			t.Errorf("locked read: %q / %q", msg, caps.PlanReason)
		}
	}
	// A read-only Hub scope keeps its own read-only reasons.
	s := NewScoped(Info{Initialized: true, Backend: "local", ExecutionMode: "local-cli"}, Scope{Workspace: "default", ReadOnlyReason: "read only"})
	s.Load(context.Background(), &failingState{engine.Mock{VersionValue: "1.13.9", WorkspaceValue: "default"}, engine.ErrReadStateLocked}, "tofu")
	if snap := s.Get(); !snap.Locked() || snap.Info.Capabilities.PlanReason != "read only" {
		t.Errorf("read-only scope: %+v", snap)
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
	// Not initialized (a provider needs init): no backend is assumed. A root that needs no
	// init at all is covered by TestNoInitNeeded.
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.tf"), []byte(`provider "aws" {}`), 0o600)
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
