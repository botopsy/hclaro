// Package claudefake is a fake `claude` 2.1.326 for tests only: test binaries re-execute themselves
// as it when HOME holds Marker (see TestMain in internal/assist and internal/api). Production code
// never imports it.
package claudefake

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Marker is the file in HOME that makes a test binary act as an assistant; its text is the mode.
const Marker = "hclaro-fake-assistant-mode"

// Record is what one start was given.
type Record struct {
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`
	Entries int      `json:"entries"`
	Env     []string `json:"env"`
	Stdin   string   `json:"stdin"`
}

// Main acts as claude and records every start in HOME: starts.log (argv), version.json for
// --version, and run.json plus run-N.json (N counts runs) for a run. mode: "old" is 2.0.9,
// "garbage" an unreadable version; for a run, "auth" is signed
// out, "fail" exits 3 echoing stdin to stderr, "big" exceeds the reply limit, "malformed" is not
// JSON, "hang" never answers, "spawn" starts a hanging grandchild (pid in grandchild.pid) and
// hangs. Anything else answers reply as a successful result.
func Main(mode, reply string) int {
	home := os.Getenv("HOME")
	if f, err := os.OpenFile(filepath.Join(home, "starts.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
		f.Close()
	}
	in, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	entries, _ := os.ReadDir(dir)
	rec, _ := json.Marshal(Record{Args: os.Args[1:], Dir: dir, Entries: len(entries), Env: os.Environ(), Stdin: string(in)})
	if slices.Equal(os.Args[1:], []string{"--version"}) {
		_ = os.WriteFile(filepath.Join(home, "version.json"), rec, 0o600)
		switch mode {
		case "old":
			fmt.Println("2.0.9 (Claude Code)")
		case "garbage":
			fmt.Println("something else")
		default:
			fmt.Println("2.1.326 (Claude Code)")
		}
		return 0
	}
	runs, _ := filepath.Glob(filepath.Join(home, "run-*.json"))
	_ = os.WriteFile(filepath.Join(home, fmt.Sprintf("run-%d.json", len(runs)+1)), rec, 0o600)
	_ = os.WriteFile(filepath.Join(home, "run.json"), rec, 0o600)
	switch mode {
	case "auth":
		os.Stdout.WriteString(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`)
		return 1
	case "fail":
		os.Stderr.WriteString("boom: " + string(in))
		return 3
	case "big":
		os.Stdout.WriteString(`{"is_error":false,"result":"` + strings.Repeat("x", 1<<20) + `"}`)
		return 0
	case "malformed":
		os.Stdout.WriteString("not json")
		return 0
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "spawn":
		// A grandchild that hangs; cancellation must kill it with the assistant.
		child := filepath.Join(home, "child")
		_ = os.MkdirAll(child, 0o700)
		_ = os.WriteFile(filepath.Join(child, Marker), []byte("hang"), 0o600)
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "--version-not")
		cmd.Env = append(os.Environ(), "HOME="+child)
		if cmd.Start() == nil {
			_ = os.WriteFile(filepath.Join(home, "grandchild.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		}
		time.Sleep(time.Minute)
		return 0
	}
	b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": reply, "session_id": "s", "total_cost_usd": 0.01})
	os.Stdout.Write(b)
	return 0
}
