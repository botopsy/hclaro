// Package codexfake is a fake `codex` 0.166.0 for tests only: test binaries re-execute themselves
// as it (see TestMain in internal/assist and internal/api). Production code never imports it.
// It talks only to the loopback server named in a self-check argv and never to the network.
package codexfake

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SelfCheckModeFile, next to the test binary, makes the self-check client misbehave: "tools"
// offers a tool, "ran" reports a call as executed, "proxy" tries the proxy, "write" writes a file.
const SelfCheckModeFile = "codexfake-selfcheck-mode"

// IsSelfCheck reports whether args are Hclaro's self-check run: exec with a base URL override.
func IsSelfCheck(args []string) bool {
	return len(args) > 0 && args[0] == "exec" && baseURL(args) != ""
}

func baseURL(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "openai_base_url="); ok {
			s, err := strconv.Unquote(v)
			if err != nil {
				return v
			}
			return s
		}
	}
	return ""
}

// Features is what `codex features list` prints under Hclaro's flags.
var Features = map[string]bool{
	"shell_tool": false, "unified_exec": true, "apps": false, "plugins": false, "browser_use": false,
	"computer_use": false, "view_image": false, "hooks": false, "multi_agent": false, "multi_agent_v2": false,
	"sleep_tool": false, "goals": false, "code_mode_host": false, "image_generation": false,
	"tool_suggest": false, "skill_search": false, "shell_snapshot": false, "code_mode": false, "code_mode_only": false,
	"daemon_auto_start": true,
}

// Main acts as codex. mode: "" or "codex" answers; "codex-old" is 0.165.0; "codex-feature-on"
// lists image_generation on; "codex-feature-other" changes an unchecked feature; "codex-catalog-tools" shows a shell in the catalog; "codex-error"
// streams part of a reply, an error event, then hangs; "codex-failed" streams part of a reply
// and a failed turn; "codex-empty" completes with no message; "codex-garbage" prints a non-JSON
// line; "codex-hang" never answers. reply is the success text.
func Main(mode, reply string) int {
	args := os.Args[1:]
	switch {
	case len(args) == 1 && args[0] == "--version":
		if mode == "codex-old" {
			fmt.Println("codex-cli 0.165.0")
		} else {
			fmt.Println("codex-cli 0.166.0")
		}
		return 0
	case len(args) >= 2 && args[0] == "features" && args[1] == "list":
		names := make([]string, 0, len(Features))
		for name := range Features {
			names = append(names, name)
		}
		sort.Strings(names) // codex prints them sorted
		for _, name := range names {
			on := Features[name]
			if mode == "codex-feature-on" && name == "image_generation" {
				on = true
			}
			if mode == "codex-feature-other" && name == "daemon_auto_start" {
				on = false // a change Hclaro does not check, but one that alters the binding
			}
			fmt.Printf("%-40s %-18s %v\n", name, "stable", on)
		}
		return 0
	case len(args) == 4 && args[0] == "debug" && args[1] == "models" && args[2] == "-c":
		path, err := strconv.Unquote(strings.TrimPrefix(args[3], "model_catalog_json="))
		if err != nil {
			return 2
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return 2
		}
		if mode == "codex-catalog-tools" {
			b = bytes.Replace(b, []byte(`"shell_type": "disabled"`), []byte(`"shell_type": "unified_exec"`), 1)
		}
		os.Stdout.Write(b)
		return 0
	case IsSelfCheck(args):
		return selfCheck(args)
	case len(args) > 0 && args[0] == "exec":
		return run(mode, reply)
	}
	return 2
}

func event(v any) {
	b, _ := json.Marshal(v)
	os.Stdout.Write(append(b, '\n'))
}

func message(text string) map[string]any {
	return map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_0", "type": "agent_message", "text": text}}
}

// Record is what a production exec run was given, written to HOME/codex-run-N.json.
type Record struct {
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`
	Entries int      `json:"entries"`
	Env     []string `json:"env"`
	Stdin   string   `json:"stdin"`
}

func run(mode, reply string) int {
	in, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	entries, _ := os.ReadDir(dir)
	rec, _ := json.Marshal(Record{Args: os.Args[1:], Dir: dir, Entries: len(entries), Env: os.Environ(), Stdin: string(in)})
	runs, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), "codex-run-*.json"))
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), fmt.Sprintf("codex-run-%d.json", len(runs)+1)), rec, 0o600)
	event(map[string]any{"type": "thread.started", "thread_id": "t"})
	event(map[string]any{"type": "turn.started"})
	switch mode {
	case "codex-error":
		event(message("PARTIAL-REPLY"))
		event(map[string]any{"type": "error", "message": "Reconnecting... 1/5 (stream disconnected before completion)"})
		time.Sleep(time.Minute)
		event(message("LATE-REPLY"))
		event(map[string]any{"type": "turn.completed"})
		return 0
	case "codex-failed":
		event(message("PARTIAL-REPLY"))
		event(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "stream disconnected"}})
		return 1
	case "codex-empty":
		event(map[string]any{"type": "turn.completed"})
		return 0
	case "codex-garbage":
		os.Stdout.WriteString("not json\n")
		return 0
	case "codex-hang":
		time.Sleep(time.Minute)
		return 0
	}
	event(message(reply))
	event(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	return 0
}

func selfCheckMode() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), SelfCheckModeFile))
	return strings.TrimSpace(string(b))
}

func model(args []string) string {
	for i, a := range args {
		if a == "-m" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// selfCheck is an honest Codex: no tools offered, every forged call refused. A mode file makes
// it misbehave the way a broken or tampered Codex would.
func selfCheck(args []string) int {
	mode := selfCheckMode()
	in, _ := io.ReadAll(os.Stdin)
	url := baseURL(args) + "/responses"
	switch mode {
	case "write":
		_ = os.WriteFile("written-by-codex", []byte("x"), 0o600)
	case "proxy":
		if p := os.Getenv("HTTPS_PROXY"); p != "" {
			if resp, err := http.Get(p + "/"); err == nil {
				resp.Body.Close()
			}
		}
	}
	input := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": string(in)}}}}
	tools := []any{}
	if mode == "tools" {
		tools = append(tools, map[string]any{"type": "function", "name": "shell"})
	}
	items, err := post(url, model(args), input, tools)
	if err != nil {
		return 1
	}
	for _, it := range items {
		input = append(input, it)
		b, _ := json.Marshal(it)
		var probe map[string]any
		_ = json.Unmarshal(b, &probe)
		id, _ := probe["call_id"].(string)
		typ, _ := probe["type"].(string)
		name, _ := probe["name"].(string)
		if id == "" {
			continue
		}
		out, outType := "unsupported call: "+name, "function_call_output"
		switch typ {
		case "custom_tool_call":
			out, outType = "unsupported custom tool call: "+name, "custom_tool_call_output"
		case "local_shell_call":
			out = "aborted"
		}
		if mode == "ran" {
			out = "ran"
		}
		input = append(input, map[string]any{"type": outType, "call_id": id, "output": out})
	}
	final, err := post(url, model(args), input, tools)
	if err != nil {
		return 1
	}
	text := ""
	for _, it := range final {
		var m struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		b, _ := json.Marshal(it)
		if json.Unmarshal(b, &m) == nil && m.Type == "message" && len(m.Content) > 0 {
			text = m.Content[0].Text
		}
	}
	event(map[string]any{"type": "thread.started", "thread_id": "t"})
	event(map[string]any{"type": "turn.started"})
	event(message(text))
	event(map[string]any{"type": "turn.completed"})
	return 0
}

func post(url, model string, input, tools []any) ([]any, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true, "input": input, "tools": tools})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var items []any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item any    `json:"item"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && ev.Type == "response.output_item.done" {
			items = append(items, ev.Item)
		}
	}
	return items, sc.Err()
}
