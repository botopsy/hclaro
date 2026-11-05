// fixture-http exercises the real HTTP handler without a TCP listener or a real engine.
// Its stdin/stdout protocol is used only by the isolated browser test harness.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hclaro/fixtures"
	"hclaro/internal/api"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/ui"
	"hclaro/internal/workspace"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var request struct {
		Method, Path, Body string
		Headers            map[string]string
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 16384)).Decode(&request) != nil {
		os.Exit(1)
	}
	input := fixtures.State
	if os.Getenv("HCLARO_FIXTURE_LARGE") == "1" {
		resources := make([]map[string]any, 1000)
		for i := range resources {
			resources[i] = map[string]any{"address": fmt.Sprintf("aws_instance.app[%d]", i), "mode": "managed", "type": "aws_instance", "name": "app", "index": i, "provider_name": "registry.terraform.io/hashicorp/aws", "values": map[string]any{"id": fmt.Sprintf("demo-%04d", i), "password": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}}
		}
		input, _ = json.Marshal(map[string]any{"format_version": "1.0", "values": map[string]any{"root_module": map[string]any{"resources": resources}}})
	}
	store := workspace.New(workspace.Info{Project: "platform-infra", Initialized: true, Backend: "local", ExecutionMode: "local-cli", Warnings: []string{"Synthetic fixture. Recorded state, not live cloud inventory."}})
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: input, PlanJSON: fixtures.PlanAllActions, Lines: bytes.Split(bytes.TrimSpace(fixtures.PlanLog), []byte("\n"))}
	store.Load(context.Background(), fake, "terraform")
	// Each request runs in a fresh process, so a completed plan is recreated with
	// deterministic IDs before handling it.
	temp, err := os.MkdirTemp("", "hclaro-fixture-")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(temp)
	if resolved, err := filepath.EvalSymlinks(temp); err == nil {
		temp = resolved
	}
	// A root module with one resource block, so the import wizard can preview against it.
	_ = os.WriteFile(filepath.Join(temp, "main.tf"), []byte("resource \"aws_s3_bucket\" \"legacy_logs\" {}\n"), 0o644)
	manager := operations.NewManager(store, fake, temp)
	manager.TempDir = temp
	next := 0
	manager.NewID = func() string { next++; return fmt.Sprintf("%032x", next) }
	if op, err := manager.StartPlan(nil, nil); err == nil {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if current, _, _ := manager.Operation(op.ID); current.Status != operations.Running {
				break
			}
		}
	}
	defer manager.Close(5 * time.Second)
	handler := api.Handler(store, manager, ui.Assets(), "127.0.0.1:9273", strings.Repeat("a", 64), strings.Repeat("b", 64), "test")
	var body io.Reader
	if request.Body != "" {
		body = strings.NewReader(request.Body)
	}
	r := httptest.NewRequest(request.Method, "http://127.0.0.1:9273"+request.Path, body)
	for k, v := range request.Headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Status  int                 `json:"status"`
		Headers map[string][]string `json:"headers"`
		Body    []byte              `json:"body"`
	}{w.Code, w.Header(), w.Body.Bytes()})
}
