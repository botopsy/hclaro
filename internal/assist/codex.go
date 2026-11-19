package assist

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenAI Codex as a runnable provider (AI-01.14, OD-CODEX-1..5). Codex runs only when every
// gate below passes; any gate that cannot be checked counts as failed. The evidence for each
// gate is in tasks/AI/CODEX-INVESTIGATION.md §6–7.
//
//   - macOS only (Linux and Windows have no runtime verification).
//   - exactly codex-cli 0.166.0, through the verified argv (codexAdapter.RunArgs, unexported: only CodexCLI runs it).
//   - no AGENTS.md or AGENTS.override.md in CODEX_HOME: Codex sends either to OpenAI.
//   - no managed source: /etc/codex, a macOS managed preference for com.openai.codex (file or
//     forced value), or a managed or cloud-config cache in CODEX_HOME. Managed requirements pin
//     features over CLI flags and replace the model catalog; exec cannot ignore them.
//   - CODEX_HOME/auth.json is a ChatGPT sign-in on a personal plan (free, go, plus, pro) whose
//     access token is valid for at least 15 more minutes. Only personal plans never load cloud
//     config, and only a token refresh (within 5 minutes of expiry) can change the plan claim.
//     The plan and sign-in type are read, compared and dropped: never stored, logged or returned.
//   - `codex features list` and `codex debug models` show the effective tool features and model
//     catalog Hclaro verified.
//   - the local self-check (codex_selfcheck.go) passed for this binary hash, version, argv,
//     catalog and effective settings.
//
// A run then starts in a fresh empty folder with GIT_CEILING_DIRECTORIES at its parent, so Codex
// finds no repository to report, and is stopped at its first error event.

const (
	codexModel      = "gpt-5.6-luna"
	codexVerified   = "0.166.0"
	codexCatalogArg = "\x00catalog\x00"
	codexTokenSlack = 15 * time.Minute
	codexProbeLimit = 4 << 20
	codexProbeTime  = 30 * time.Second
	codexAuthLimit  = 64 << 10
)

//go:embed codex_catalog.json
var codexCatalog []byte

// codexPlans are the plan claims Codex 0.166.0 never fetches cloud config for. Anything else,
// including Team and the pro variants, is refused.
var codexPlans = []string{"free", "go", "plus", "pro"}

// codexFeatures are the feature states `codex features list` must report under the run's
// --disable flags. unified_exec stays on: Codex re-enables it unless a managed requirement pins
// it, and the catalog's shell_type "disabled" leaves no exec tool (the self-check proves it).
var codexFeatures = map[string]bool{
	"shell_tool": false, "unified_exec": true, "apps": false, "plugins": false, "browser_use": false,
	"computer_use": false, "view_image": false, "hooks": false, "multi_agent": false, "multi_agent_v2": false,
	"sleep_tool": false, "goals": false, "code_mode_host": false, "image_generation": false,
	"tool_suggest": false, "skill_search": false, "shell_snapshot": false, "code_mode": false, "code_mode_only": false,
}

// profileScript asks macOS the question Codex asks (CFPreferencesAppValueIsForced) for both keys
// Codex reads from a configuration profile. It is fixed text; nothing user-controlled reaches it.
const profileScript = `ObjC.import("Foundation");
var d = $.NSUserDefaults.alloc.initWithSuiteName("com.openai.codex");
["config_toml_base64", "requirements_toml_base64"].map(function (k) {
  return d.objectIsForcedForKey(k) ? "forced" : (d.objectForKey(k).isNil() ? "absent" : "set");
}).join(",")`

const profileClean = "absent,absent"

// CodexHost replaces machine facts in tests. The zero value is the real machine.
type CodexHost struct {
	GOOS    string                                    // "" means runtime.GOOS
	Managed []string                                  // paths that must not exist; nil means codexManagedPaths
	Profile func(ctx context.Context) (string, error) // nil runs profileScript with osascript
	Now     func() time.Time                          // nil means time.Now
}

// CodexCLI is the Codex Assistant: the generic CLI plus the gates, the effective-settings
// probes and the self-check. Results are kept in memory only.
type CodexCLI struct {
	CLI
	Host CodexHost

	mu       sync.Mutex
	hashKey  string // stat key of the binary last hashed
	hash     string
	passed   string // binding of the last passed self-check
	failedAt string // stat key of a binary that failed the self-check
}

// NewCodexCLI is the Codex provider; path is the --codex-path flag ("" looks codex up on PATH).
func NewCodexCLI(path string) *CodexCLI {
	return &CodexCLI{CLI: CLI{Adapter: codexAdapter, Path: path}}
}

func (c *CodexCLI) now() time.Time {
	if c.Host.Now != nil {
		return c.Host.Now()
	}
	return time.Now()
}

func handoff(st ProviderStatus, kind string) ProviderStatus {
	st.Mode, st.Reason, st.Message = ModeHandoff, kind, Message(kind)
	return st
}

// Detect reports the version and every gate that needs no Codex process beyond --version.
func (c *CodexCLI) Detect(ctx context.Context) ProviderStatus {
	st := c.CLI.Detect(ctx)
	if st.Mode != ModeHeadless {
		return st
	}
	if kind := c.gate(ctx); kind != "" {
		return handoff(st, kind)
	}
	if key, err := statKeyOf(st.Path); err != nil {
		return handoff(st, KindCodexCheck)
	} else {
		c.mu.Lock()
		failed := c.failedAt != "" && c.failedAt == key
		c.mu.Unlock()
		if failed {
			return handoff(st, KindCodexCheck)
		}
	}
	return st
}

// gate checks the platform, CODEX_HOME files, managed sources and the sign-in.
func (c *CodexCLI) gate(ctx context.Context) string {
	goos := c.Host.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		return KindCodexPlatform
	}
	home, err := codexHome()
	if err != nil {
		return KindCodexAccount
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md"} {
		if !absent(filepath.Join(home, name)) {
			return KindCodexAgents
		}
	}
	managed := c.Host.Managed
	if managed == nil {
		if managed, err = codexManagedPaths(); err != nil {
			return KindCodexManaged
		}
	}
	for _, p := range append(slices.Clone(managed), filepath.Join(home, "managed_config.toml"), filepath.Join(home, "cloud-config-bundle-cache.json")) {
		if !absent(p) {
			return KindCodexManaged
		}
	}
	if caches, err := filepath.Glob(filepath.Join(home, "managed-config-*.json")); err != nil || len(caches) != 0 {
		return KindCodexManaged
	}
	probe := c.Host.Profile
	if probe == nil {
		probe = osascriptProfile
	}
	if out, err := probe(ctx); err != nil || out != profileClean {
		return KindCodexManaged
	}
	return codexSignIn(filepath.Join(home, "auth.json"), c.now())
}

// absent is true only when path is confirmed missing; any other answer counts as present.
func absent(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// codexHome is the folder Codex reads, passed to it explicitly so both agree.
func codexHome() (string, error) {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		if !filepath.IsAbs(v) {
			return "", errors.New("relative CODEX_HOME")
		}
		return filepath.Clean(v), nil
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return "", errors.New("no HOME")
	}
	return filepath.Join(home, ".codex"), nil
}

// codexManagedPaths are the machine-managed sources Codex 0.166.0 reads on macOS.
func codexManagedPaths() ([]string, error) {
	u, err := user.Current()
	if err != nil || u.Username == "" || strings.ContainsAny(u.Username, "/\x00") {
		return nil, errors.New("no user")
	}
	return []string{
		"/etc/codex",
		"/Library/Managed Preferences/com.openai.codex.plist",
		filepath.Join("/Library/Managed Preferences", u.Username, "com.openai.codex.plist"),
	}, nil
}

const osascriptPath = "/usr/bin/osascript"

var osascriptArgs = []string{"-l", "JavaScript", "-e", profileScript}

func osascriptProfile(ctx context.Context) (string, error) {
	r, err := runProc(ctx, proc{path: osascriptPath, args: osascriptArgs, env: []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME")},
		limit: 4 << 10, timeout: 10 * time.Second})
	if err != nil {
		return "", err
	}
	if !r.exitOK || r.overflow {
		return "", errors.New("osascript failed")
	}
	return strings.TrimSpace(string(r.stdout)), nil
}

// codexSignIn reads CODEX_HOME/auth.json strictly: a ChatGPT sign-in, a personal plan claim and
// an access token valid past now+15 minutes. Anything missing, malformed, unknown or unexpected
// is a refusal; nothing read is kept or returned.
func codexSignIn(path string, now time.Time) string {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return KindAuth
	}
	if err != nil {
		return KindCodexAccount
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, codexAuthLimit+1))
	if err != nil || len(raw) > codexAuthLimit {
		return KindCodexAccount
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || top == nil {
		return KindCodexAccount
	}
	// Exactly the fields a ChatGPT sign-in has in 0.166.0; a missing or extra one is unexpected.
	topKeys := []string{"auth_mode", "OPENAI_API_KEY", "tokens", "last_refresh"}
	if len(top) != len(topKeys) {
		return KindCodexAccount
	}
	for _, k := range topKeys {
		if _, ok := top[k]; !ok {
			return KindCodexAccount
		}
	}
	var mode string
	if json.Unmarshal(top["auth_mode"], &mode) != nil || mode != "chatgpt" {
		return KindCodexAccount
	}
	if string(bytes.TrimSpace(top["OPENAI_API_KEY"])) != "null" { // an API key is refused (OD-CODEX)
		return KindCodexAccount
	}
	var refreshed string
	if json.Unmarshal(top["last_refresh"], &refreshed) != nil || refreshed == "" {
		return KindCodexAccount
	}
	var tokens map[string]json.RawMessage
	tokenKeys := []string{"id_token", "access_token", "refresh_token", "account_id"}
	if json.Unmarshal(top["tokens"], &tokens) != nil || len(tokens) != len(tokenKeys) {
		return KindCodexAccount
	}
	strs := map[string]string{}
	for _, k := range tokenKeys {
		var s string
		if json.Unmarshal(tokens[k], &s) != nil || s == "" {
			return KindCodexAccount
		}
		strs[k] = s
	}
	id, ok1 := jwtClaims(strs["id_token"])
	access, ok2 := jwtClaims(strs["access_token"])
	if !ok1 || !ok2 {
		return KindCodexAccount
	}
	plan, ok := planClaim(id)
	if !ok || !slices.Contains(codexPlans, plan) {
		return KindCodexAccount
	}
	// A plan claim in the access token must agree; a mismatch is unexpected.
	if _, present := access["https://api.openai.com/auth"]; present {
		if ap, ok := planClaim(access); ok && ap != plan {
			return KindCodexAccount
		}
	}
	var exp float64
	if json.Unmarshal(access["exp"], &exp) != nil || exp <= 0 {
		return KindCodexToken
	}
	if time.Unix(int64(exp), 0).Before(now.Add(codexTokenSlack)) {
		return KindCodexToken
	}
	return ""
}

func jwtClaims(token string) (map[string]json.RawMessage, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(b, &claims) != nil || claims == nil {
		return nil, false
	}
	return claims, true
}

// planClaim is the claim Codex itself reads: "https://api.openai.com/auth".chatgpt_plan_type.
func planClaim(claims map[string]json.RawMessage) (string, bool) {
	var auth struct {
		Plan *string `json:"chatgpt_plan_type"`
	}
	if json.Unmarshal(claims["https://api.openai.com/auth"], &auth) != nil || auth.Plan == nil {
		return "", false
	}
	return *auth.Plan, true
}

// Run re-checks everything before every send and runs Codex once.
func (c *CodexCLI) Run(ctx context.Context, payload []byte) (string, error) {
	st := c.Detect(ctx)
	if st.Mode != ModeHeadless {
		return "", &Failure{Kind: st.Reason}
	}
	home, err := codexHome()
	if err != nil {
		return "", &Failure{Kind: KindCodexAccount}
	}
	bin, err := filepath.EvalSymlinks(st.Path)
	if err != nil {
		return "", &Failure{Kind: KindMissing}
	}
	key, sum, err := c.binaryHash(bin)
	if err != nil {
		return "", &Failure{Kind: KindCodexCheck}
	}
	catalog, cleanup, err := writeCatalog()
	if err != nil {
		return "", &Failure{Kind: KindFailed}
	}
	defer cleanup()
	features, models, kind := c.effective(ctx, bin, home, catalog)
	if kind != "" {
		return "", &Failure{Kind: kind}
	}
	binding := bindingOf(c.RunArgs, sum, st.Version, features, models)
	c.mu.Lock()
	if c.passed != binding {
		if kind := c.selfCheck(ctx, bin, catalog); kind != "" {
			if ctx.Err() == nil {
				c.failedAt = key
			}
			c.mu.Unlock()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", &Failure{Kind: kind}
		}
		c.passed, c.failedAt = binding, ""
	}
	c.mu.Unlock()

	dir, err := privateDir()
	if err != nil {
		return "", &Failure{Kind: KindFailed}
	}
	defer os.RemoveAll(dir)
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = runTimeout
	}
	r, err := runProc(ctx, proc{path: bin, args: codexArgs(c.RunArgs, catalog), dir: dir, env: c.codexEnv(home, dir),
		stdin: payload, limit: maxReply, timeout: timeout, stop: codexStop})
	if err != nil {
		return "", err
	}
	if r.overflow {
		return "", &Failure{Kind: KindOutputTooLarge}
	}
	reply, kind := codexReply(r.stdout, r.stderr, r.exitOK && !r.stopped)
	if kind == "" && r.stopped {
		kind = KindFailed
	}
	if kind != "" {
		return "", &Failure{Kind: kind}
	}
	return reply, nil
}

// codexEnv is the allowlisted environment plus CODEX_HOME and a Git ceiling at dir's parent.
func (c *CodexCLI) codexEnv(home, dir string) []string {
	return append(c.Environ(os.Environ()), "CODEX_HOME="+home, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
}

// privateDir is a fresh empty folder with symbolic links resolved, so the Git ceiling matches.
func privateDir() (string, error) {
	dir, err := os.MkdirTemp("", "hclaro-ai-")
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return real, nil
}

var selfCheckURL = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]{1,5}/v1$`)

// codexArgv reports whether args is one of the Codex shapes Hclaro runs: the run, the
// self-check (the run plus a loopback base URL), `features list` and `debug models`.
func codexArgv(args []string) bool {
	run := codexAdapter.RunArgs
	n := len(run) - 1 // the run's argv without its final "-"
	prefix := func() bool {
		for i, want := range run[:n] {
			if want == "model_catalog_json="+codexCatalogArg {
				if !catalogArg(args[i]) {
					return false
				}
			} else if args[i] != want {
				return false
			}
		}
		return true
	}
	switch {
	case len(args) == n+1:
		return prefix() && args[n] == "-"
	case len(args) == n+3:
		url, ok := quotedValue(args[n+1], "openai_base_url=")
		return prefix() && args[n] == "-c" && ok && selfCheckURL.MatchString(url) && args[n+2] == "-"
	case len(args) == 4 && args[0] == "debug":
		return slices.Equal(args[:3], []string{"debug", "models", "-c"}) && catalogArg(args[3])
	}
	return slices.Equal(args, codexFeatureArgs())
}

// quotedValue returns the value of a key="value" argument written by strconv.Quote.
func quotedValue(arg, key string) (string, bool) {
	q, ok := strings.CutPrefix(arg, key)
	if !ok {
		return "", false
	}
	v, err := strconv.Unquote(q)
	return v, err == nil && strconv.Quote(v) == q
}

// catalogArg accepts model_catalog_json= only with a catalog written by writeCatalog: an
// absolute catalog.json in a hclaro-ai- folder directly in the temporary directory.
func catalogArg(arg string) bool {
	p, ok := quotedValue(arg, "model_catalog_json=")
	if !ok || !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) || filepath.Base(p) != "catalog.json" {
		return false
	}
	dir := filepath.Dir(p)
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	return err == nil && strings.HasPrefix(filepath.Base(dir), "hclaro-ai-") && filepath.Dir(dir) == tmp
}

// codexFeatureArgs is `codex features list` with the run's --disable flags.
func codexFeatureArgs() []string {
	out := []string{"features", "list"}
	for i, a := range codexAdapter.RunArgs {
		if a == "--disable" {
			out = append(out, a, codexAdapter.RunArgs[i+1])
		}
	}
	return out
}

// codexArgs fills the catalog path into the allowlisted argv; nothing else varies.
func codexArgs(template []string, catalog string) []string {
	out := slices.Clone(template)
	for i, a := range out {
		out[i] = strings.ReplaceAll(a, codexCatalogArg, strconv.Quote(catalog))
	}
	return out
}

func writeCatalog() (string, func(), error) {
	dir, err := privateDir()
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, codexCatalog, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

// binaryHash is the SHA-256 of the binary, recomputed whenever its stat key changes.
func (c *CodexCLI) binaryHash(bin string) (string, string, error) {
	key, err := statKeyOf(bin)
	if err != nil {
		return "", "", err
	}
	c.mu.Lock()
	if c.hashKey == key {
		defer c.mu.Unlock()
		return key, c.hash, nil
	}
	c.mu.Unlock()
	f, err := os.Open(bin)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", err
	}
	if after, err := statKeyOf(bin); err != nil || after != key {
		return "", "", errors.New("binary changed while hashing")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	c.mu.Lock()
	c.hashKey, c.hash = key, sum
	c.mu.Unlock()
	return key, sum, nil
}

// bindingOf hashes the self-check inputs, including runArgs: the argv template the run uses.
func bindingOf(runArgs []string, parts ...string) string {
	h := sha256.New()
	for _, p := range append(parts, strings.Join(runArgs, "\x00"), string(codexCatalog)) {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// effective runs `codex features list` and `codex debug models` with the run's settings, behind a
// refusing proxy, and checks the tool features and catalog fields Hclaro verified.
func (c *CodexCLI) effective(ctx context.Context, bin, home, catalog string) (string, string, string) {
	proxy, err := startDenyProxy()
	if err != nil {
		return "", "", KindFailed
	}
	defer proxy.close()
	dir, err := privateDir()
	if err != nil {
		return "", "", KindFailed
	}
	defer os.RemoveAll(dir)
	env := append(c.codexEnv(home, dir), proxy.env()...)
	fr, err := runProc(ctx, proc{path: bin, args: codexFeatureArgs(), dir: dir, env: env, limit: codexProbeLimit, timeout: codexProbeTime})
	if err != nil {
		return "", "", KindOf(err)
	}
	mr, err := runProc(ctx, proc{path: bin, args: []string{"debug", "models", "-c", "model_catalog_json=" + strconv.Quote(catalog)},
		dir: dir, env: env, limit: codexProbeLimit, timeout: codexProbeTime})
	if err != nil {
		return "", "", KindOf(err)
	}
	if !fr.exitOK || fr.overflow || !mr.exitOK || mr.overflow || proxy.hits() != 0 || !featuresMatch(fr.stdout) {
		return "", "", KindCodexSettings
	}
	fields, ok := catalogTools(mr.stdout)
	if !ok {
		return "", "", KindCodexSettings
	}
	return string(fr.stdout), fields, ""
}

// featuresMatch requires every feature in codexFeatures to be listed exactly once with its state.
func featuresMatch(out []byte) bool {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		want, ok := codexFeatures[f[0]]
		if !ok {
			continue
		}
		state := f[len(f)-1]
		if seen[f[0]] || (state != "true" && state != "false") || (state == "true") != want {
			return false
		}
		seen[f[0]] = true
	}
	return len(seen) == len(codexFeatures)
}

// catalogTools returns the tool-relevant fields of the effective catalog when they are exactly
// those of codex_catalog.json: one model, no shell, no apply_patch, no code mode, no
// multi-agent, no tool search, no experimental tools.
func catalogTools(out []byte) (string, bool) {
	var cat struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(out, &cat) != nil || len(cat.Models) != 1 {
		return "", false
	}
	m := cat.Models[0]
	want := map[string]string{"slug": `"` + codexModel + `"`, "shell_type": `"disabled"`, "apply_patch_tool_type": "null",
		"tool_mode": "null", "experimental_supported_tools": "[]", "multi_agent_version": "null", "supports_search_tool": "false"}
	keys := make([]string, 0, len(want))
	for k, v := range want {
		got := strings.Join(strings.Fields(string(m[k])), "")
		if got == "" {
			got = "null" // an absent optional field is unset
		}
		if got != v {
			return "", false
		}
		keys = append(keys, k+"="+got)
	}
	slices.Sort(keys)
	return strings.Join(keys, ";"), true
}

// codexStop stops a run at its first error, failed turn, error item or unreadable line
// (OD-CODEX: a reconnect is a failed turn even if a later retry would succeed).
func codexStop(line []byte) bool {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return false
	}
	var e codexEvent
	if json.Unmarshal(line, &e) != nil || e.Type == "" {
		return true
	}
	return e.failed()
}

type codexEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
	} `json:"item"`
}

func (e codexEvent) failed() bool {
	return e.Type == "error" || e.Type == "turn.failed" || (e.Item != nil && e.Item.Type == "error")
}

// codexReply reads `codex exec --json` output. Success needs a clean exit, turn.completed, no
// error event, error item or failed turn, and a non-empty last agent_message. A failed or
// interrupted turn returns no text, even when part of a reply arrived.
func codexReply(stdout, stderr []byte, exitOK bool) (string, string) {
	completed, reply := false, ""
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e codexEvent
		if json.Unmarshal(line, &e) != nil || e.Type == "" {
			return "", KindMalformed
		}
		if e.failed() {
			text := e.Message
			if e.Error != nil {
				text += e.Error.Message
			}
			if e.Item != nil {
				text += e.Item.Message
			}
			return "", classify(text)
		}
		switch e.Type {
		case "turn.completed":
			completed = true
		case "item.completed":
			if e.Item != nil && e.Item.Type == "agent_message" {
				reply = e.Item.Text
			}
		}
	}
	if !exitOK {
		return "", classify(string(stderr))
	}
	if !completed || strings.TrimSpace(reply) == "" {
		return "", KindMalformed
	}
	return reply, ""
}
