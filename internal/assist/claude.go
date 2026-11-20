package assist

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
)

// Claude Code, headless-eligible at 2.1.326 (FEASIBILITY.md, 2026-11-17). The argv is the
// probe argv without --max-budget-usd and with --restricted (security review 2026-11-17). The
// CLI checks the budget only after a turn, so it billed requests and withheld their replies;
// Anthropic bills usage directly and Hclaro imposes no cost limit. --safe-mode alone still loads the
// user's installed plugins and user settings files (permission rules, env, apiKeyHelper);
// --restricted ignores those files and refuses bypassPermissions. --tools "" leaves no
// built-in tool and --strict-mcp-config without --mcp-config no MCP server (verified from the
// CLI's own init record: tools [] and mcp_servers []).
var Claude = Adapter{
	ID: "claude", Name: "Claude Code", Command: "claude",
	VersionArgs: []string{"--version"},
	RunArgs: []string{"-p", "--restricted", "--safe-mode", "--tools", "", "--strict-mcp-config", "--permission-mode", "dontAsk",
		"--permission-prompts", "none", "--no-session-persistence", "--output-format", "json"},
	AuthEnv:      []string{"ANTHROPIC_API_KEY"},
	ParseVersion: claudeVersion,
	Reply:        claudeReply,
	Disclosure:   "Anthropic bills Claude Code requests directly, according to your account, subscription and applicable usage limits. Hclaro does not cap, control or guarantee the cost of any request.",
}

var claudeVersionRe = regexp.MustCompile(`^([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,6}) \(Claude Code\)$`)

// claudeVersion accepts 2.1.x from the probed 2.1.326 on. shortcut: patch releases are
// trusted to keep the probed flags; re-run the probe and widen or pin this on a minor bump.
func claudeVersion(out []byte) (string, bool) {
	m := claudeVersionRe.FindSubmatch(bytes.TrimSpace(out))
	if m == nil {
		return "", false
	}
	patch, _ := strconv.Atoi(string(m[3]))
	return string(m[1]) + "." + string(m[2]) + "." + string(m[3]), string(m[1]) == "2" && string(m[2]) == "1" && patch >= 326
}

// claudeReply reads the single JSON result object. Failure is is_error or a non-zero exit;
// subtype stays "success" even when not logged in, so it is never used.
func claudeReply(stdout, stderr []byte, exitOK bool) (string, string) {
	var r struct {
		IsError *bool   `json:"is_error"`
		Result  *string `json:"result"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &r) != nil || r.IsError == nil {
		if !exitOK {
			return "", classify(string(stderr))
		}
		return "", KindMalformed
	}
	if !exitOK || *r.IsError {
		text := string(stderr)
		if r.Result != nil {
			text += *r.Result
		}
		return "", classify(text)
	}
	if r.Result == nil {
		return "", KindMalformed
	}
	return *r.Result, ""
}
