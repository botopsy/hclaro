package assist

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
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"hclaro/internal/imports"
	"hclaro/internal/state"
)

// Project context (AI-01.09, OD-AI-10). The package is read from the project root on disk:
// configuration files of the root module and of local modules inside the root, never anything
// else. Inside a Git repository only files Git lists as not ignored are read (an allowlist, so
// files in nested repositories and submodules are never read). A file with a scan hit is omitted
// whole and listed with its reason. The package is blocked when Hclaro cannot tell which files
// Git ignores. The structural summary comes first, then file text, smallest first, until
// MaxBytes; files that do not fit are listed as not included.
//
// The scan is heuristic: it finds credential-shaped and random-looking values, literals under
// secret-like names and local paths, but a secret written as an ordinary-looking value under an
// ordinary name, or in prose in a comment, is not recognized. The preview exists for that.

const (
	KindProject = "project"
	// KindChat is a conversation started without a context package.
	KindChat = "conversation"
)

const (
	maxProjectFiles = 2000     // configuration files in scope; more answers ErrTooLarge
	maxProjectFile  = 1 << 20  // larger files are listed as omitted, never read
	maxProjectBytes = 32 << 20 // configuration read in total; more answers ErrTooLarge
)

// neverIncluded is stated in every project package, so the assistant knows what it cannot see.
const neverIncluded = "never included: state, .tfvars values, .terraform/, saved plans, environment, backend configuration, " +
	"files outside the project root, symbolic links, untracked files Git ignores, nested repositories and submodules " +
	"(files already tracked by Git are included even if they match .gitignore)"

var (
	// topBlock opens a top-level HCL block: its type and up to two quoted labels.
	topBlock = regexp.MustCompile(`^\s*(resource|data|ephemeral|module|variable|output|provider|terraform|locals|moved|import|removed|check)\b\s*(?:"([^"]*)")?\s*(?:"([^"]*)")?`)
	// attr is a name = value line (or "name": value in an object).
	attr = regexp.MustCompile(`^(\s*)"?([A-Za-z0-9_.-]+)"?\s*[=:]\s*(.*)$`)
	// scalar is a one-line literal without templates: number, bool, null or plain string.
	scalar     = regexp.MustCompile(`^(-?[0-9][0-9._eE+-]*|true|false|null|"[^"$%\\]*")\s*$`)
	heredocVal = regexp.MustCompile(`<<-?([A-Za-z_][A-Za-z0-9_-]*)\s*$`)
)

// GitFiles is what Git reports about the project's configuration files (gitctx.ConfigFiles).
type GitFiles struct {
	// Repo is true when the project is inside a Git repository: then only Listed files are read.
	Repo bool
	// Listed holds root-relative slash paths of configuration files Git does not ignore.
	Listed map[string]bool
	// Known is false when the project is in a repository but Git could not list its files
	// (Git missing, failing or truncated output): the package is blocked.
	Known bool
}

var (
	errGitUnknown        = errors.New("git could not list the project's files")
	errGitignoreNoRepo   = errors.New("a .gitignore outside a Git repository")
	errProjectUnreadable = errors.New("the project folder could not be read")
)

type projectFile struct {
	rel, lang, text string
}

type moduleCall struct{ dir, name, source string }

type projectVar struct {
	dir, name, typ, desc                  string
	sensitive, defaultOmitted, hasDefault bool
}

type projectOut struct {
	dir, name, desc string
	sensitive       bool
}

type projectScan struct {
	b         *builder
	files     []projectFile
	omitted   []string // "path: reason"
	ignored   int      // configuration files Git ignores, counted only
	lock      []byte
	dirs      map[string]int
	addresses map[string][]string // module dir → resource and data addresses
	calls     []moduleCall
	providers []string // provider blocks and required_providers sources
	vars      []projectVar
	outs      []projectOut
}

// label returns v for display, or a marker when it fails the scan. Names and descriptions are
// labels: one that looks secret is hidden, it never blocks the package.
func (b *builder) label(v string) string {
	if !b.safe("", v, 0) || len(v) > 512 {
		return "[not shown]"
	}
	return display(v, false)
}

func (p *projectScan) omit(rel, reason string) {
	p.omitted = append(p.omitted, p.b.label(rel)+": "+reason)
}

func configKind(name string) string {
	switch {
	case strings.HasSuffix(name, ".tf.json"), strings.HasSuffix(name, ".tofu.json"):
		return "json"
	case strings.HasSuffix(name, ".tf"), strings.HasSuffix(name, ".tofu"):
		return "hcl"
	}
	return ""
}

// candidate is one configuration file (or the lock file) in scope: its bytes, or why it is not read.
type candidate struct {
	rel, kind, reason string
	data              []byte
}

// readBounded reads a regular file of at most maxProjectFile bytes. The opened file must be the
// one Lstat saw, so a file swapped for a symbolic link while the package is built is not read.
func readBounded(abs string) ([]byte, string) {
	before, err := os.Lstat(abs)
	switch {
	case err != nil || !before.Mode().IsRegular():
		return nil, "could not be read"
	case before.Size() > maxProjectFile:
		return nil, fmt.Sprintf("larger than %d KiB", maxProjectFile>>10)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, "could not be read"
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, "changed while it was read"
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProjectFile+1))
	switch {
	case err != nil:
		return nil, "could not be read"
	case len(data) > maxProjectFile:
		return nil, fmt.Sprintf("larger than %d KiB", maxProjectFile>>10)
	}
	return data, ""
}

// walkProject collects the configuration files in scope and the project fingerprint: a SHA-256
// over every in-scope path with its outcome and content hash, and every path Git ignores. Any
// change to the configuration the package is built from changes it; other files do not.
func walkProject(root string, git GitFiles, keep bool) (cands []candidate, ignored int, fingerprint string, err error) {
	if !git.Known {
		return nil, 0, "", errGitUnknown
	}
	// WalkDir does not enter a root that is itself a symbolic link (a project registered through
	// one); links inside the root are never followed.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, 0, "", errProjectUnreadable
	}
	h := sha256.New()
	total := 0
	err = filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".gitignore" && !git.Repo {
			// Git applies no ignore rules outside a repository, but the user meant some files
			// to stay private: Hclaro cannot honor that, so it refuses (fail closed).
			return errGitignoreNoRepo
		}
		kind := configKind(d.Name())
		if rel == ".terraform.lock.hcl" {
			kind = "lock"
		}
		if kind == "" {
			return nil // .tfvars, state, plans and every other file
		}
		if git.Repo && !git.Listed[rel] {
			ignored++
			fmt.Fprintf(h, "ignored\x00%s\n", rel)
			return nil
		}
		if len(cands) >= maxProjectFiles {
			return ErrTooLarge
		}
		c := candidate{rel: rel, kind: kind}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			c.reason = "symbolic link, never followed"
		case !d.Type().IsRegular():
			c.reason = "not a regular file"
		default:
			c.data, c.reason = readBounded(abs)
		}
		if total += len(c.data); total > maxProjectBytes {
			return ErrTooLarge
		}
		sum := sha256.Sum256(c.data)
		fmt.Fprintf(h, "file\x00%s\x00%s\x00%x\n", rel, c.reason, sum)
		if !keep {
			c.data = nil
		}
		cands = append(cands, c)
		return nil
	})
	if err != nil && !errors.Is(err, ErrTooLarge) && !errors.Is(err, errGitignoreNoRepo) {
		err = errProjectUnreadable
	}
	return cands, ignored, hex.EncodeToString(h.Sum(nil)), err
}

// ProjectFingerprint is the fingerprint Project records for the same root and Git listing. A
// conversation re-checks it before every turn: a different value means the attached project
// package no longer matches the configuration on disk.
func ProjectFingerprint(root string, git GitFiles) (string, error) {
	_, _, fp, err := walkProject(root, git, false)
	return fp, err
}

// Project builds the context for the project at s.Root, reading only what git allows. The
// returned Package carries the fingerprint of exactly the bytes it was built from.
func Project(s Scope, git GitFiles) (Package, error) {
	b := newBuilder(KindProject, s)
	cands, ignored, fp, err := walkProject(s.Root, git, true)
	switch {
	case errors.Is(err, ErrTooLarge):
		return Package{}, ErrTooLarge
	case errors.Is(err, errGitUnknown):
		b.blocked["project.gitFiles"] = true
		return b.finish()
	case errors.Is(err, errGitignoreNoRepo):
		b.blocked["project.gitignoreOutsideRepository"] = true
		return b.finish()
	case err != nil:
		b.blocked["project.files"] = true
		return b.finish()
	}
	p := &projectScan{b: b, ignored: ignored, dirs: map[string]int{}, addresses: map[string][]string{}}
	for _, c := range cands {
		switch {
		case c.reason != "":
			p.omit(c.rel, c.reason)
		case c.kind == "lock":
			p.lock = c.data
		default:
			p.read(c)
		}
	}
	pkg, err := p.render()
	pkg.Fingerprint = fp
	return pkg, err
}

func (p *projectScan) read(c candidate) {
	rel, kind := c.rel, c.kind
	// CRLF files and a UTF-8 byte order mark give the same package as their Unix form.
	text := strings.ReplaceAll(strings.TrimPrefix(string(c.data), "\ufeff"), "\r\n", "\n")
	if !utf8.ValidString(text) {
		p.omit(rel, "not UTF-8 text")
		return
	}
	dir := path.Dir(rel)
	var out, reason string
	if kind == "json" {
		out, reason = p.jsonFile(dir, text)
	} else {
		out, reason = p.hclFile(dir, text)
	}
	if reason != "" {
		p.omit(rel, reason)
		return
	}
	p.dirs[dir]++
	p.files = append(p.files, projectFile{rel: rel, lang: map[string]string{"json": "json", "hcl": "hcl"}[kind], text: out})
}

// hit is the per-line scan of a configuration file: a credential-shaped or local-path line, or
// a string literal assigned to a secret-like name. One hit omits the whole file. Lines inside a
// block under a secret-like name are not hits; the redaction below still hides their values.
func (b *builder) hit(lines []string) bool {
	for _, l := range lines {
		if state.LooksSecret(l) || b.isLocal(l) {
			return true
		}
		if m := attr.FindStringSubmatch(l); m != nil && state.RiskyKey(m[2]) && strings.Contains(m[3], `"`) {
			return true
		}
	}
	return false
}

// hclFile scans one HCL file and collects its structure. It returns the text to share (backend
// blocks and unsafe variable defaults removed, then the existing redaction), or a reason to omit
// it. Structure is kept only for a file that is shared.
func (p *projectScan) hclFile(dir, text string) (string, string) {
	text, ok := omitBackend(text)
	if !ok {
		return "", "backend or cloud configuration written in a form Hclaro cannot remove"
	}
	before := *p
	nAddr := len(p.addresses[dir])
	undo := func(reason string) (string, string) {
		p.calls, p.providers, p.vars, p.outs = before.calls, before.providers, before.vars, before.outs
		p.addresses[dir] = p.addresses[dir][:nAddr]
		return "", reason
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		m := topBlock.FindStringSubmatch(lines[i])
		if m == nil || !strings.Contains(lines[i], "{") {
			out = append(out, lines[i])
			continue
		}
		start, depth := i, braces(lines[i])
		for depth > 0 && depth < hideRest && i+1 < len(lines) {
			i++
			depth += braces(lines[i])
		}
		if depth != 0 {
			return undo("its block structure could not be read")
		}
		block := lines[start : i+1]
		switch m[1] {
		case "variable":
			block = p.variable(dir, m[2], block)
		case "resource":
			p.addresses[dir] = append(p.addresses[dir], m[2]+"."+m[3])
		case "data", "ephemeral":
			p.addresses[dir] = append(p.addresses[dir], m[1]+"."+m[2]+"."+m[3])
		case "module":
			p.calls = append(p.calls, moduleCall{dir: dir, name: m[2], source: attrValue(block, "source")})
		case "provider":
			p.providers = append(p.providers, "configured in "+dir+": provider "+m[2])
		case "output":
			p.outs = append(p.outs, projectOut{dir: dir, name: m[2], desc: unquote(attrValue(block, "description")), sensitive: attrValue(block, "sensitive") == "true"})
		case "terraform":
			for _, l := range block {
				if a := attr.FindStringSubmatch(l); a != nil && a[2] == "source" {
					p.providers = append(p.providers, "required in "+dir+": "+unquote(strings.TrimRight(a[3], " ,}")))
				}
			}
		}
		out = append(out, block...)
	}
	if p.b.hit(out) {
		return undo("may contain a secret or a local path")
	}
	return imports.RedactGenerated(strings.Join(out, "\n"), false), ""
}

// attrValue is the value of a one-line name = value at depth 1 of a block, or "".
func attrValue(block []string, name string) string {
	depth := 0
	for i, l := range block {
		if a := attr.FindStringSubmatch(l); i > 0 && depth == 1 && a != nil && a[2] == name {
			return strings.TrimSpace(a[3])
		}
		depth += braces(l)
	}
	return ""
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// variable records a variable and replaces its default with a marker unless it is a one-line
// literal that passes the scan, under a name that is not secret-like, in a variable not marked
// sensitive.
func (p *projectScan) variable(dir, name string, block []string) []string {
	v := projectVar{dir: dir, name: name, typ: attrValue(block, "type"), desc: unquote(attrValue(block, "description")), sensitive: attrValue(block, "sensitive") == "true"}
	out := make([]string, 0, len(block))
	// A default written on the header line (variable "x" { default = "…" }) is never parsed:
	// everything after the opening brace is replaced.
	if open := strings.Index(block[0], "{"); strings.Contains(block[0][open+1:], "default") {
		v.hasDefault, v.defaultOmitted = true, true
		header := block[0][:open+1] + ` default = "<default omitted>"`
		if len(block) == 1 {
			header += " }"
		}
		block = append([]string{header}, block[1:]...)
	}
	depth := 0
	for i := 0; i < len(block); i++ {
		l := block[i]
		a := attr.FindStringSubmatch(l)
		if i == 0 || depth != 1 || a == nil || a[2] != "default" {
			out = append(out, l)
			depth += braces(l)
			continue
		}
		v.hasDefault = true
		value := strings.TrimSpace(a[3])
		if scalar.MatchString(value) && !v.sensitive && !state.RiskyKey(name) && p.b.safe(name, unquote(value), 0) {
			out = append(out, l)
			continue
		}
		v.defaultOmitted = true
		out = append(out, a[1]+`default = "<default omitted>"`)
		// Skip the rest of a multi-line default: a heredoc or an open bracket.
		if h := heredocVal.FindStringSubmatch(value); h != nil {
			for i+1 < len(block) && strings.TrimSpace(block[i+1]) != h[1] {
				i++
			}
			i++
		} else {
			for d := braces(value) + bracketsOf(value); d > 0 && i+1 < len(block); {
				i++
				d += braces(block[i]) + bracketsOf(block[i])
			}
		}
	}
	p.vars = append(p.vars, v)
	return out
}

// bracketsOf counts [ and ] outside strings, like braces counts { and }.
func bracketsOf(s string) int {
	return braces(strings.NewReplacer("{", "", "}", "", "[", "{", "]", "}").Replace(s))
}

// jsonFile scans one JSON configuration file. Backend and cloud blocks are removed, unsafe
// variable defaults replaced and secret-like values redacted, then it is re-encoded with
// sorted keys.
func (p *projectScan) jsonFile(dir, text string) (string, string) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var doc map[string]any
	if dec.Decode(&doc) != nil || dec.More() {
		return "", "not a valid JSON configuration document"
	}
	before, nAddr := *p, len(p.addresses[dir])
	each := func(v any, f func(map[string]any)) {
		switch t := v.(type) {
		case map[string]any:
			f(t)
		case []any:
			for _, item := range t {
				if m, ok := item.(map[string]any); ok {
					f(m)
				}
			}
		}
	}
	each(doc["terraform"], func(t map[string]any) {
		delete(t, "backend")
		delete(t, "cloud")
		each(t["required_providers"], func(rp map[string]any) {
			for _, req := range sortedKeys(rp) {
				if r, ok := rp[req].(map[string]any); ok {
					if src, ok := r["source"].(string); ok {
						p.providers = append(p.providers, "required in "+dir+": "+src)
					}
				}
			}
		})
	})
	named := func(key string, f func(name string, body map[string]any)) {
		each(doc[key], func(m map[string]any) {
			for _, name := range sortedKeys(m) {
				each(m[name], func(body map[string]any) { f(name, body) })
			}
		})
	}
	named("variable", func(name string, body map[string]any) {
		v := projectVar{dir: dir, name: name, sensitive: body["sensitive"] == true}
		v.typ, _ = body["type"].(string)
		v.desc, _ = body["description"].(string)
		if def, ok := body["default"]; ok {
			v.hasDefault = true
			switch def.(type) {
			case string, json.Number, bool, nil:
				v.defaultOmitted = v.sensitive || state.RiskyKey(name)
			default:
				v.defaultOmitted = true
			}
			if v.defaultOmitted {
				body["default"] = "<default omitted>"
			}
		}
		p.vars = append(p.vars, v)
	})
	named("output", func(name string, body map[string]any) {
		o := projectOut{dir: dir, name: name, sensitive: body["sensitive"] == true}
		o.desc, _ = body["description"].(string)
		p.outs = append(p.outs, o)
	})
	named("module", func(name string, body map[string]any) {
		src, _ := body["source"].(string)
		p.calls = append(p.calls, moduleCall{dir: dir, name: name, source: `"` + src + `"`})
	})
	named("provider", func(name string, _ map[string]any) {
		p.providers = append(p.providers, "configured in "+dir+": provider "+name)
	})
	for _, mode := range []string{"resource", "data", "ephemeral"} {
		named(mode, func(typ string, body map[string]any) {
			prefix := typ + "."
			if mode != "resource" {
				prefix = mode + "." + prefix
			}
			for _, n := range sortedKeys(body) {
				p.addresses[dir] = append(p.addresses[dir], prefix+n)
			}
		})
	}
	// Checked after variable defaults were replaced, and only below block labels: a variable or
	// resource type named like a secret is a name, not a value. Structure of a file with a hit
	// is dropped with it.
	hit := !p.b.safe("", doc, 0)
	for _, body := range blockBodies(doc) {
		hit = hit || redactRisky(body, 0)
	}
	if hit {
		p.calls, p.providers, p.vars, p.outs = before.calls, before.providers, before.vars, before.outs
		p.addresses[dir] = p.addresses[dir][:nAddr]
		return "", "may contain a secret or a local path"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(doc) != nil {
		return "", "could not be re-encoded"
	}
	return strings.TrimSuffix(buf.String(), "\n"), ""
}

// redactRisky reports whether a value sits under a secret-like key (a hit): a non-empty string
// or number, or an object or list with anything in it, at any depth below that key.
func redactRisky(v any, depth int) bool {
	if depth > 64 {
		return true
	}
	switch t := v.(type) {
	case map[string]any:
		for k, item := range t {
			if state.RiskyKey(k) && hasValue(item) {
				return true
			}
			if redactRisky(item, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range t {
			if redactRisky(item, depth+1) {
				return true
			}
		}
	}
	return false
}

// blockBodies returns the bodies of a JSON configuration document's blocks, below their labels
// (resource "type" "name", variable "name", locals with none). Unknown block types have none.
func blockBodies(doc map[string]any) []any {
	labels := map[string]int{"resource": 2, "data": 2, "ephemeral": 2, "variable": 1, "output": 1, "module": 1, "provider": 1, "check": 1}
	out := []any{}
	var at func(v any, d int)
	at = func(v any, d int) {
		switch t := v.(type) {
		case []any:
			for _, item := range t {
				at(item, d)
			}
		case map[string]any:
			if d == 0 {
				out = append(out, t)
				return
			}
			for _, k := range sortedKeys(t) {
				at(t[k], d-1)
			}
		default:
			out = append(out, map[string]any{"": v})
		}
	}
	for _, k := range sortedKeys(doc) {
		at(doc[k], labels[k])
	}
	return out
}

func hasValue(v any) bool {
	switch t := v.(type) {
	case string:
		return t != ""
	case json.Number:
		return true
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	}
	return false // null and booleans carry no secret
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lockLines reads provider versions and constraints from the dependency lock file.
func (p *projectScan) lockLines() []string {
	out, current := []string{}, ""
	for _, l := range strings.Split(strings.ReplaceAll(string(p.lock), "\r\n", "\n"), "\n") {
		if m := topBlock.FindStringSubmatch(l); m != nil && m[1] == "provider" {
			current = m[2]
			continue
		}
		if a := attr.FindStringSubmatch(l); current != "" && a != nil && (a[2] == "version" || a[2] == "constraints") {
			out = append(out, "locked: "+p.b.label(current)+" "+a[2]+" "+p.b.label(unquote(strings.TrimSpace(a[3]))))
		}
	}
	return out
}

func dirLabel(dir string) string {
	if dir == "." {
		return ". (root module)"
	}
	return dir
}

func (p *projectScan) render() (Package, error) {
	b := p.b
	b.context("")
	addrCount := 0
	for _, a := range p.addresses {
		addrCount += len(a)
	}
	b.add("summary", "Project summary",
		fmt.Sprintf("configuration files included: %d", len(p.files)),
		fmt.Sprintf("configuration files omitted: %d", len(p.omitted)),
		fmt.Sprintf("configuration files Git ignores or does not list (not read, not named): %d", p.ignored),
		fmt.Sprintf("module directories: %d", len(p.dirs)),
		fmt.Sprintf("module calls: %d", len(p.calls)),
		fmt.Sprintf("resources, data and ephemeral blocks: %d", addrCount),
		fmt.Sprintf("variables: %d, outputs: %d", len(p.vars), len(p.outs)),
		neverIncluded)

	dirs := make([]string, 0, len(p.dirs))
	for d := range p.dirs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	tree := []string{}
	for _, d := range dirs {
		tree = append(tree, fmt.Sprintf("%s: %d files, %d blocks", b.label(dirLabel(d)), p.dirs[d], len(p.addresses[d])))
	}
	sort.Slice(p.calls, func(i, j int) bool {
		return p.calls[i].dir < p.calls[j].dir || p.calls[i].dir == p.calls[j].dir && p.calls[i].name < p.calls[j].name
	})
	for _, c := range p.calls {
		src := unquote(c.source)
		where := "remote, listed only and never fetched"
		if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
			where = "local"
			if target := path.Join(c.dir, src); target == ".." || strings.HasPrefix(target, "../") {
				where = "local, outside the project root and not included"
			}
		}
		tree = append(tree, fmt.Sprintf("%s calls module.%s source=%s (%s)", b.label(dirLabel(c.dir)), b.label(c.name), b.label(src), where))
	}
	if len(tree) > 0 {
		b.add("modules", "Module tree", tree...)
	}

	providers := append(sorted(dedupe(p.providers)), p.lockLines()...)
	for i := range providers {
		providers[i] = b.label(providers[i])
	}
	if len(providers) > 0 {
		b.add("providers", "Provider requirements and lock versions", providers...)
	}

	resources := []string{}
	for _, d := range dirs {
		for _, a := range sorted(p.addresses[d]) {
			resources = append(resources, b.label(dirLabel(d))+": "+b.label(a))
		}
	}
	if len(resources) > 0 {
		b.add("resources", "Resources and data sources by module", resources...)
	}

	sort.Slice(p.vars, func(i, j int) bool {
		return p.vars[i].dir < p.vars[j].dir || p.vars[i].dir == p.vars[j].dir && p.vars[i].name < p.vars[j].name
	})
	vars := []string{}
	for _, v := range p.vars {
		line := b.label(dirLabel(v.dir)) + ": var." + b.label(v.name)
		if v.typ != "" {
			line += " type=" + b.label(v.typ)
		}
		switch {
		case v.defaultOmitted:
			line += " default=<default omitted>"
		case v.hasDefault:
			line += " default shown in file"
		}
		line += flags("sensitive", v.sensitive)
		if v.desc != "" {
			line += " — " + b.label(v.desc)
		}
		vars = append(vars, line)
	}
	if len(vars) > 0 {
		b.add("variables", "Variables (values from .tfvars are never included)", vars...)
	}
	sort.Slice(p.outs, func(i, j int) bool {
		return p.outs[i].dir < p.outs[j].dir || p.outs[i].dir == p.outs[j].dir && p.outs[i].name < p.outs[j].name
	})
	outs := []string{}
	for _, o := range p.outs {
		line := b.label(dirLabel(o.dir)) + ": output." + b.label(o.name) + flags("sensitive", o.sensitive)
		if o.desc != "" {
			line += " — " + b.label(o.desc)
		}
		outs = append(outs, line)
	}
	if len(outs) > 0 {
		b.add("outputs", "Outputs", outs...)
	}
	if len(p.omitted) > 0 {
		b.add("omitted", "Omitted files", sorted(p.omitted)...)
	}
	if len(b.blocked) > 0 {
		return b.finish()
	}
	p.addFiles()
	return b.finish()
}

func dedupe(in []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// addFiles adds file text, smallest first, while the package stays within MaxBytes, and lists
// the rest under "Not included (budget)". Smallest first fits the most files; the largest come
// last and are the first left out.
func (p *projectScan) addFiles() {
	b := p.b
	files := append([]projectFile{}, p.files...)
	sort.Slice(files, func(i, j int) bool {
		return len(files[i].text) < len(files[j].text) || len(files[i].text) == len(files[j].text) && files[i].rel < files[j].rel
	})
	blocks := make([][]string, len(files))
	for i, f := range files {
		block := []string{"# file: " + b.label(f.rel)}
		for _, l := range strings.Split(f.text, "\n") {
			block = append(block, display(l, true))
		}
		blocks[i] = block
	}
	base := len(b.sections)
	size := func(k int) int {
		b.sections = b.sections[:base]
		p.addSections(files, blocks, k)
		md, _ := b.render(nil)
		return len(md)
	}
	// Adding a file adds its text and removes its shorter "not included" line, so the size
	// grows with k: the largest k that fits is found by binary search.
	lo, hi := 0, len(files)
	for lo < hi {
		if mid := (lo + hi + 1) / 2; size(mid) <= MaxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	size(lo)
}

func (p *projectScan) addSections(files []projectFile, blocks [][]string, k int) {
	if k > 0 {
		sec := section{id: "files", title: "Configuration files (smallest first)", lang: "hcl"}
		sec.blocks = blocks[:k]
		p.b.sections = append(p.b.sections, sec)
	}
	if k < len(files) {
		rest := []string{}
		for _, f := range files[k:] {
			rest = append(rest, fmt.Sprintf("%s (%d bytes)", p.b.label(f.rel), len(f.text)))
		}
		p.b.add("budget", "Not included (budget)", rest...)
	}
}
