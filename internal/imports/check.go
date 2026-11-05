package imports

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"hclaro/internal/state"
)

// Request is what the user entered in the wizard. File names are bare names in the root module.
type Request struct {
	Address        string `json:"address"`
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	File           string `json:"file"`
	GenerateConfig bool   `json:"generateConfig"`
}

// Issue is one validation result. Errors block the import; warnings are shown with it.
type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Recipe is a manual CLI procedure, shown for copying. Hclaro never runs it.
type Recipe struct {
	Block    string   `json:"block"`
	Commands []string `json:"commands"`
	Legacy   string   `json:"legacy"`
}

// Preview is the complete, side-effect-free result of checking a request.
type Preview struct {
	Address       string  `json:"address"`
	File          string  `json:"file"`
	GeneratedFile string  `json:"generatedFile,omitempty"`
	Content       string  `json:"content"`
	SHA256        string  `json:"sha256"`
	Config        string  `json:"config"`
	ConfigFile    string  `json:"configFile,omitempty"`
	CanGenerate   bool    `json:"canGenerate"`
	Issues        []Issue `json:"issues"`
	Blocked       bool    `json:"blocked"`
	Recipe        *Recipe `json:"recipe,omitempty"`
}

// Env is the read-only context a request is checked against.
type Env struct {
	Root, DataDir, Engine string
	// Snapshot is the status of the state read; collisions can be ruled out only when it
	// is "ready" or "empty".
	Snapshot         string
	Index            state.Index
	SupportsGenerate bool
}

const (
	maxID         = 2048
	maxConfigFile = 16 << 20
	maxScanFiles  = 2000
)

var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}\.tf$`)

// Check validates a request without writing anything or running an engine.
func Check(env Env, req Request) (p Preview) {
	p = Preview{File: req.File, Config: "unknown", Issues: []Issue{}}
	fail := func(code, message string) { p.Issues = append(p.Issues, Issue{code, "error", message}) }
	warn := func(code, message string) { p.Issues = append(p.Issues, Issue{code, "warning", message}) }
	defer func() {
		for _, issue := range p.Issues {
			if issue.Severity == "error" {
				p.Blocked = true
			}
		}
	}()

	address, err := ParseAddress(strings.TrimSpace(req.Address))
	if err != nil {
		fail("invalid_address", `Enter a managed resource instance address such as aws_instance.web, aws_instance.web[0] or module.network.aws_subnet.private["a"]. Data sources cannot be imported. String keys may not contain quotes, backslashes or "${".`)
	}
	if validID(req.ID) != nil {
		fail("invalid_id", "Enter the provider's import ID for the existing object (1–2048 characters, no line breaks or control characters).")
	} else if state.Suspicious(req.ID) {
		fail("id_credential", "The import ID looks like a credential. Import IDs are written into configuration in plain text, so Hclaro will not write this one. Check the provider documentation for the expected ID format.")
	}
	if req.Provider != "" && !providerRef.MatchString(req.Provider) {
		fail("invalid_provider", "Provider must be a provider local name with an optional alias, such as aws or aws.west.")
	}
	if !fileName.MatchString(req.File) || strings.HasSuffix(req.File, "override.tf") {
		fail("invalid_file", "Choose a new file name in the root module ending in .tf, using letters, digits, - and _. Override files (*override.tf) are not allowed.")
	} else if exists(env.Root, req.File) {
		fail("file_exists", fmt.Sprintf("%s already exists. Hclaro never overwrites or appends to existing files; choose a new name.", req.File))
	}
	if err != nil {
		return p
	}
	p.Address = address.String()
	if req.Provider != "" && providerRef.MatchString(req.Provider) && !strings.HasPrefix(address.Type, strings.SplitN(req.Provider, ".", 2)[0]+"_") {
		warn("provider_mismatch", fmt.Sprintf("Provider %s does not look like the provider for %s resources. Check the provider reference.", req.Provider, address.Type))
	}

	// State collisions: the address must be unbound, and the object should not be managed elsewhere.
	switch env.Snapshot {
	case "ready", "empty":
		for _, r := range env.Index.Resources {
			if r.Address == p.Address {
				fail("address_in_state", fmt.Sprintf("%s is already in state. Importing again would bind a second object to it. Choose a different address, or remove the existing binding with your own reviewed workflow.", p.Address))
				break
			}
		}
		if req.ID != "" {
			for _, r := range env.Index.Resources {
				if r.Mode != "managed" || r.Type != address.Type {
					continue
				}
				if attrs, ok := r.Attributes.(map[string]any); ok && attrs["id"] == req.ID {
					fail("id_in_state", fmt.Sprintf("An object with this ID is already managed as %s. Importing it again would let two addresses manage one object.", r.Address))
					break
				}
			}
		}
	default:
		fail("state_unverified", "The state snapshot is not readable, so Hclaro cannot rule out that this address is already managed. Resolve the workspace error first.")
	}

	// Configuration: a resource block must exist, or the engine must generate one.
	dir, dirErr := moduleDir(env, address)
	if dirErr == nil {
		found, err := findResource(dir, address)
		switch {
		case err != nil:
			warn("config_unverified", "The configuration could not be read completely, so Hclaro could not confirm the resource block. The plan reports it if the block is missing.")
		case found != nil:
			p.Config = "found"
			p.ConfigFile = found.file
			if rel, err := filepath.Rel(env.Root, filepath.Join(dir, found.file)); err == nil {
				p.ConfigFile = filepath.ToSlash(rel)
			}
			keyIssues(address, found, warn)
		default:
			p.Config = "missing"
		}
	} else {
		warn("config_unverified", "The module source directory for this address was not found in the module manifest, so Hclaro could not confirm the resource block. The plan reports it if the block is missing.")
	}
	p.CanGenerate = env.SupportsGenerate && address.InRoot() && address.Key == nil && p.Config == "missing"
	switch {
	case req.GenerateConfig && p.Config == "found":
		fail("generate_with_config", "A resource block already exists, so there is nothing to generate. Turn off configuration generation.")
	case req.GenerateConfig && !p.CanGenerate:
		fail("generate_unsupported", generateReason(env, address))
	case !req.GenerateConfig && p.Config == "missing":
		hint := ""
		if p.CanGenerate {
			hint = " or let the engine generate one during the plan"
		}
		fail("config_missing", fmt.Sprintf("No resource block for %s was found in %s. Add the block yourself%s. The engine cannot import into an address without configuration.", address.Resource(), moduleLabel(address), hint))
	}
	if req.GenerateConfig && p.CanGenerate && fileName.MatchString(req.File) {
		p.GeneratedFile = strings.TrimSuffix(req.File, ".tf") + "_generated.tf"
		if exists(env.Root, p.GeneratedFile) {
			fail("file_exists", fmt.Sprintf("%s already exists. The engine refuses to overwrite it, and so does Hclaro; choose another file name.", p.GeneratedFile))
		}
	}
	if existing := importTargets(env.Root, p.Address); existing != "" {
		fail("import_exists", fmt.Sprintf("%s already contains an import or moved block targeting %s. Review that block instead of adding another.", existing, p.Address))
	}

	if validID(req.ID) == nil && !state.Suspicious(req.ID) && (req.Provider == "" || providerRef.MatchString(req.Provider)) {
		p.Content = fileContent(address, req.ID, req.Provider)
		p.SHA256 = Digest(p.File, p.GeneratedFile, p.Content)
		p.Recipe = recipe(env.Engine, address, req.ID, req.Provider, req.File, p.GeneratedFile)
	}
	return p
}

// Digest binds a preview to its destination files and exact content.
func Digest(file, generated, content string) string {
	sum := sha256.Sum256([]byte(file + "\x00" + generated + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

func validID(id string) error {
	if id == "" || len(id) > maxID || strings.TrimSpace(id) != id {
		return errors.New("invalid")
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return errors.New("invalid")
		}
	}
	return nil
}

func exists(root, name string) bool {
	_, err := os.Lstat(filepath.Join(root, name))
	return !errors.Is(err, fs.ErrNotExist)
}

func moduleLabel(a Address) string {
	if a.InRoot() {
		return "the root module"
	}
	return "module." + a.ModuleKey()
}

func generateReason(env Env, a Address) string {
	switch {
	case !env.SupportsGenerate:
		return "This engine version cannot generate configuration for imports."
	case !a.InRoot():
		return "The engine generates configuration only for resources in the root module. Add the resource block to the module yourself."
	case a.Key != nil:
		return "The engine cannot generate configuration for an instance with a count index or for_each key. Add the resource block yourself."
	}
	return "Configuration generation is unavailable for this address."
}

// moduleDir finds the source directory that declares the address's module, using the
// module manifest written by init. Directories outside the project and data directory are
// not read.
func moduleDir(env Env, a Address) (string, error) {
	if a.InRoot() {
		return env.Root, nil
	}
	b, err := readBounded(filepath.Join(env.DataDir, "modules", "modules.json"), 4<<20)
	if err != nil {
		return "", err
	}
	var manifest struct {
		Modules []struct{ Key, Dir string }
	}
	if json.Unmarshal(b, &manifest) != nil {
		return "", errors.New("manifest")
	}
	for _, m := range manifest.Modules {
		if m.Key != a.ModuleKey() {
			continue
		}
		dir := m.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(env.Root, dir)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return "", err
		}
		for _, base := range []string{env.Root, env.DataDir} {
			if real, err := filepath.EvalSymlinks(base); err == nil && within(real, resolved) {
				return resolved, nil
			}
		}
		return "", errors.New("outside")
	}
	return "", errors.New("not found")
}

func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("not a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// resourceBlock describes a declared resource. Count and ForEach report the meta-arguments
// found at the top level of the block.
type resourceBlock struct {
	file           string
	count, forEach bool
}

func configFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && (strings.HasSuffix(n, ".tf") || strings.HasSuffix(n, ".tofu") || strings.HasSuffix(n, ".tf.json") || strings.HasSuffix(n, ".tofu.json")) {
			names = append(names, n)
		}
		if len(names) > maxScanFiles {
			return nil, errors.New("too many files")
		}
	}
	return names, nil
}

// findResource scans a module directory textually for the resource block. The scan is a
// heuristic, not an HCL parser; the plan is the authority.
func findResource(dir string, a Address) (*resourceBlock, error) {
	names, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	header := regexp.MustCompile(`(?m)^[ \t]*resource[ \t]+"?` + regexp.QuoteMeta(a.Type) + `"?[ \t]+"?` + regexp.QuoteMeta(a.Name) + `"?[ \t]*\{`)
	for _, name := range names {
		b, err := readBounded(filepath.Join(dir, name), maxConfigFile)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, ".json") {
			if block := jsonResource(b, a); block != nil {
				block.file = name
				return block, nil
			}
			continue
		}
		if loc := header.FindIndex(b); loc != nil {
			count, forEach := metaArguments(b[loc[1]:])
			return &resourceBlock{file: name, count: count, forEach: forEach}, nil
		}
	}
	return nil, nil
}

var (
	countArg   = regexp.MustCompile(`^\s*count\s*=`)
	forEachArg = regexp.MustCompile(`^\s*for_each\s*=`)
	heredocArg = regexp.MustCompile(`<<-?([A-Za-z_][A-Za-z0-9_]*)\s*$`)
)

// metaArguments reads a block body (after its opening brace) line by line and reports
// count/for_each assignments at depth one. Strings, comments and heredocs are skipped.
func metaArguments(body []byte) (count, forEach bool) {
	depth := 1
	heredocEnd := ""
	comment := false
	for _, line := range strings.Split(string(body), "\n") {
		if heredocEnd != "" {
			if strings.TrimSpace(line) == heredocEnd {
				heredocEnd = ""
			}
			continue
		}
		if depth == 1 && !comment {
			count = count || countArg.MatchString(line)
			forEach = forEach || forEachArg.MatchString(line)
		}
		inString := false
		for i := 0; i < len(line); i++ {
			c := line[i]
			switch {
			case comment:
				if c == '*' && i+1 < len(line) && line[i+1] == '/' {
					comment = false
					i++
				}
			case inString:
				if c == '\\' {
					i++
				} else if c == '"' {
					inString = false
				}
			case c == '"':
				inString = true
			case c == '#' || c == '/' && i+1 < len(line) && line[i+1] == '/':
				i = len(line)
			case c == '/' && i+1 < len(line) && line[i+1] == '*':
				comment = true
				i++
			case c == '{':
				depth++
			case c == '}':
				depth--
				if depth == 0 {
					return count, forEach
				}
			}
		}
		if m := heredocArg.FindStringSubmatch(line); m != nil && !comment {
			heredocEnd = m[1]
		}
	}
	return count, forEach
}

func jsonResource(b []byte, a Address) *resourceBlock {
	var doc struct {
		Resource map[string]map[string]json.RawMessage `json:"resource"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	raw, ok := doc.Resource[a.Type][a.Name]
	if !ok {
		return nil
	}
	var bodies []map[string]json.RawMessage
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		_ = json.Unmarshal(raw, &bodies)
	} else {
		var body map[string]json.RawMessage
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
	}
	block := &resourceBlock{}
	for _, body := range bodies {
		_, c := body["count"]
		_, f := body["for_each"]
		block.count = block.count || c
		block.forEach = block.forEach || f
	}
	return block
}

// keyIssues compares the instance key with the block's count/for_each. These are warnings:
// the scan is textual, and the plan reports the definitive error.
func keyIssues(a Address, block *resourceBlock, warn func(code, message string)) {
	key := a.Key
	switch {
	case key == nil && (block.count || block.forEach):
		meta := "count"
		if block.forEach {
			meta = "for_each"
		}
		warn("key_missing", fmt.Sprintf("The resource block in %s uses %s, so the address needs an instance key, for example %s[%s].", block.file, meta, a.String(), map[bool]string{true: `"key"`, false: "0"}[block.forEach]))
	case key != nil && !block.count && !block.forEach:
		warn("key_unexpected", fmt.Sprintf("The resource block in %s has no count or for_each, so the address should not have an instance key.", block.file))
	case key != nil && key.Number != nil && block.forEach:
		warn("key_type", fmt.Sprintf("The resource block in %s uses for_each, which needs a string key such as [\"name\"], not a number.", block.file))
	case key != nil && key.Text != nil && block.count:
		warn("key_type", fmt.Sprintf("The resource block in %s uses count, which needs a number index such as [0], not a string key.", block.file))
	}
}

var targetLine = regexp.MustCompile(`(?m)^[ \t]*to[ \t]*=[ \t]*([^\s#]+)`)

// importTargets returns the first root module file whose import or moved blocks target
// address. Import blocks are only valid in the root module.
func importTargets(root, address string) string {
	names, err := configFiles(root)
	if err != nil {
		return ""
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := readBounded(filepath.Join(root, name), maxConfigFile)
		if err != nil {
			continue
		}
		for _, m := range targetLine.FindAllSubmatch(b, -1) {
			if string(m[1]) == address {
				return name
			}
		}
	}
	return ""
}

// shellQuote quotes s for POSIX shells. Recipes are shown for copying, never executed.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func recipe(engineName string, a Address, id, provider, file, generated string) *Recipe {
	if engineName != "tofu" {
		engineName = "terraform"
	}
	plan := engineName + " plan -out=import.tfplan"
	if generated != "" {
		plan += " -generate-config-out=" + generated
	}
	legacy := engineName + " import " + shellQuote(a.String()) + " " + shellQuote(id)
	return &Recipe{
		Block: Block(a, id, provider),
		Commands: []string{
			"# Save the import block in " + file + " in the root module, then:",
			plan,
			engineName + " show import.tfplan",
			engineName + " apply import.tfplan",
		},
		Legacy: legacy,
	}
}
