// fixture-http exercises the real HTTP handler without a TCP listener or a real engine.
// Its stdin/stdout protocol is used only by the isolated browser test harness.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hclaro/fixtures"
	"hclaro/internal/api"
	"hclaro/internal/engine"
	"hclaro/internal/ui"
	"hclaro/internal/workspace"
	"io"
	"net/http/httptest"
	"os"
	"strings"
)

func main() {
	var request struct {
		Method, Path string
		Headers      map[string]string
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
	store.Load(context.Background(), &engine.Mock{VersionValue: "1.9.8", WorkspaceValue: "demo", JSON: input}, "terraform")
	handler := api.Handler(store, ui.Assets(), "127.0.0.1:9273", strings.Repeat("a", 64), "test")
	r := httptest.NewRequest(request.Method, "http://127.0.0.1:9273"+request.Path, nil)
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
