package assist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The local security self-check (OD-CODEX-2). Codex runs with the real argv and catalog against
// a fake Responses server inside Hclaro on 127.0.0.1, with an empty CODEX_HOME, a dummy API key
// and a proxy that refuses every connection. The fake answers the first turn with 14 tool calls
// covering every execution path found in 0.166.0. It passes only when:
//   - exactly two model requests arrive and nothing else (a WebSocket upgrade is answered 426);
//   - both requests use the pinned model and offer no tool: no `tools`, no additional_tools
//     item, no tool or agent section in the developer prompt;
//   - every one of the 14 calls comes back refused ("unsupported call" or "aborted");
//   - Codex exits cleanly with the fake's reply, the proxy saw nothing and no file appeared.
// It never contacts anything outside this machine.

const (
	selfCheckTimeout = 90 * time.Second
	selfCheckPrompt  = "Hclaro local security self-check."
	selfCheckReply   = "done"
	selfCheckKey     = "sk-hclaro-self-check-not-a-key"
)

// selfCheckCalls are the forged calls: shell, exec, apply_patch (both forms), code mode, a
// sub-agent, view_image, MCP, stdin, sleep, user input and a local shell call.
const selfCheckCalls = `[{"type":"function_call","id":"fc1","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"touch c_exec_command\"}"},
{"type":"function_call","id":"fc2","call_id":"call_2","name":"shell","arguments":"{\"command\":[\"/bin/sh\",\"-c\",\"touch c_shell\"]}"},
{"type":"function_call","id":"fc3","call_id":"call_3","name":"shell_command","arguments":"{\"command\":\"touch c_shell_command\"}"},
{"type":"custom_tool_call","id":"cc4","call_id":"call_4","name":"apply_patch","input":"*** Begin Patch\n*** Add File: c_patch.txt\n+x\n*** End Patch\n"},
{"type":"function_call","id":"fc5","call_id":"call_5","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\\n*** Add File: c_patch.txt\\n+x\\n*** End Patch\\n\"}"},
{"type":"custom_tool_call","id":"cc6","call_id":"call_6","name":"exec","input":"text('js ran')","namespace":"functions"},
{"type":"custom_tool_call","id":"cc7","call_id":"call_7","name":"exec","input":"text('js ran')"},
{"type":"function_call","id":"fc8","call_id":"call_8","name":"spawn_agent","arguments":"{\"message\":\"touch c_agent\",\"task_name\":\"x\"}","namespace":"collaboration"},
{"type":"function_call","id":"fc9","call_id":"call_9","name":"view_image","arguments":"{\"path\":\"/etc/hosts\"}"},
{"type":"function_call","id":"fc10","call_id":"call_10","name":"read_mcp_resource","arguments":"{\"server\":\"x\",\"uri\":\"file:///etc/hosts\"}"},
{"type":"function_call","id":"fc11","call_id":"call_11","name":"write_stdin","arguments":"{\"session_id\":1,\"chars\":\"id\\n\"}"},
{"type":"function_call","id":"fc12","call_id":"call_12","name":"sleep","arguments":"{\"duration_ms\":10}","namespace":"clock"},
{"type":"function_call","id":"fc13","call_id":"call_13","name":"request_user_input","arguments":"{\"questions\":[]}"},
{"type":"local_shell_call","id":"ls14","call_id":"call_14","status":"completed","action":{"type":"exec","command":["/usr/bin/touch","c_local_shell"]}}]`

const selfCheckCallCount = 14

var (
	refusedOutput = regexp.MustCompile(`^(unsupported (custom tool )?call: [A-Za-z0-9_.]+|aborted)$`)
	promptSection = regexp.MustCompile(`<[a-z_]+>`)
)

// selfCheck runs the check; callers hold c.mu. It returns "" or KindCodexCheck.
func (c *CodexCLI) selfCheck(ctx context.Context, bin, catalog string) string {
	ctx, cancel := context.WithTimeout(ctx, selfCheckTimeout)
	defer cancel()
	fake, err := startSelfCheckServer()
	if err != nil {
		return KindCodexCheck
	}
	defer fake.close()
	proxy, err := startDenyProxy()
	if err != nil {
		return KindCodexCheck
	}
	defer proxy.close()
	root, err := privateDir()
	if err != nil {
		return KindCodexCheck
	}
	defer os.RemoveAll(root)
	home, codexHome, work := filepath.Join(root, "home"), filepath.Join(root, "codex"), filepath.Join(root, "work")
	for _, d := range []string{home, codexHome, work} {
		if os.Mkdir(d, 0o700) != nil {
			return KindCodexCheck
		}
	}
	args := codexArgs(c.RunArgs, catalog)
	args = append(args[:len(args)-1:len(args)-1], "-c", "openai_base_url="+strconv.Quote(fake.url+"/v1"), "-")
	env := []string{}
	for _, kv := range c.Environ(os.Environ()) {
		if k, _, _ := strings.Cut(kv, "="); k != "HOME" {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+home, "CODEX_HOME="+codexHome, "OPENAI_API_KEY="+selfCheckKey, "GIT_CEILING_DIRECTORIES="+root, "NO_PROXY=127.0.0.1")
	env = append(env, proxy.env()...)
	r, err := runProc(ctx, proc{path: bin, args: args, dir: work, env: env, stdin: []byte(selfCheckPrompt), limit: maxReply, timeout: selfCheckTimeout, stop: codexStop})
	if err != nil || r.stopped || r.overflow {
		return KindCodexCheck
	}
	reply, kind := codexReply(r.stdout, r.stderr, r.exitOK)
	entries, derr := os.ReadDir(work)
	if kind != "" || reply != selfCheckReply || derr != nil || len(entries) != 0 || proxy.hits() != 0 || !fake.verify() {
		return KindCodexCheck
	}
	return ""
}

// selfCheckServer is the fake Responses endpoint. It records every request.
type selfCheckServer struct {
	url   string
	srv   *http.Server
	mu    sync.Mutex
	posts [][]byte
	other int
}

func startSelfCheckServer() (*selfCheckServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &selfCheckServer{url: "http://" + ln.Addr().String()}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return s, nil
}

func (s *selfCheckServer) close() { s.srv.Close() }

func (s *selfCheckServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && r.Method == http.MethodGet && r.URL.Path == "/v1/responses" {
		w.WriteHeader(http.StatusUpgradeRequired) // Codex falls back to HTTPS streaming
		return
	}
	s.mu.Lock()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || len(s.posts) >= 2 {
		s.other++
		s.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.posts = append(s.posts, body)
	turn := len(s.posts)
	s.mu.Unlock()
	items := []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","id":"m2","content":[{"type":"output_text","text":"` + selfCheckReply + `"}]}`)}
	if turn == 1 {
		_ = json.Unmarshal([]byte(selfCheckCalls), &items)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	id := fmt.Sprintf("resp_%d", turn)
	sse(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
	for i, it := range items {
		sse(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": i, "item": it})
	}
	sse(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": id,
		"usage": map[string]any{"input_tokens": 1, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 1,
			"output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 2}}})
}

func sse(w io.Writer, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// verify checks what Codex sent: two tool-free requests, the second refusing every call.
func (s *selfCheckServer) verify() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.other != 0 || len(s.posts) != 2 {
		return false
	}
	for i, raw := range s.posts {
		var req struct {
			Model string            `json:"model"`
			Tools []json.RawMessage `json:"tools"`
			Input []map[string]any  `json:"input"`
		}
		if json.Unmarshal(raw, &req) != nil || req.Model != codexModel || len(req.Tools) != 0 || len(req.Input) == 0 {
			return false
		}
		outputs := map[string]string{}
		for _, it := range req.Input {
			typ, _ := it["type"].(string)
			if typ == "additional_tools" {
				return false
			}
			if role, _ := it["role"].(string); role == "developer" && promptSection.MatchString(textOf(it["content"])) {
				return false
			}
			if typ == "function_call_output" || typ == "custom_tool_call_output" {
				id, _ := it["call_id"].(string)
				if _, dup := outputs[id]; dup {
					return false
				}
				outputs[id] = textOf(it["output"])
			}
		}
		if i == 0 && len(outputs) != 0 {
			return false
		}
		if i == 1 {
			if len(outputs) != selfCheckCallCount {
				return false
			}
			for n := 1; n <= selfCheckCallCount; n++ {
				out, ok := outputs["call_"+strconv.Itoa(n)]
				if !ok || !refusedOutput.MatchString(strings.TrimSpace(out)) {
					return false
				}
			}
		}
	}
	return true
}

// textOf flattens a string or a list of {text} parts.
func textOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, p := range t {
			if m, ok := p.(map[string]any); ok {
				s, _ := m["text"].(string)
				b.WriteString(s)
			} else if s, ok := p.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// denyProxy refuses every request, CONNECT included, and counts them. Probes and the self-check
// point every proxy variable at it, so any outbound attempt is seen and fails.
type denyProxy struct {
	addr string
	srv  *http.Server
	mu   sync.Mutex
	n    int
}

func startDenyProxy() (*denyProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &denyProxy{addr: ln.Addr().String()}
	p.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		p.n++
		p.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
	})}
	go p.srv.Serve(ln)
	return p, nil
}

func (p *denyProxy) close() { p.srv.Close() }

func (p *denyProxy) hits() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func (p *denyProxy) env() []string {
	u := "http://" + p.addr
	return []string{"HTTPS_PROXY=" + u, "HTTP_PROXY=" + u, "ALL_PROXY=" + u, "https_proxy=" + u, "http_proxy=" + u, "all_proxy=" + u}
}
