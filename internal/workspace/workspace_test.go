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
