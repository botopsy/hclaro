package assist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"hclaro/internal/imports"
)

// Proposed changes (AI-01.12, OD-AI-12). A reply may carry whole-file proposals in fenced
// blocks: ```hclaro-change path=<relative path> base=<sha256 of the shared file text, or new>.
// Hclaro validates each one against the project on disk, computes the diff itself and writes a
// file only when the user applies that one proposal. Anything not in this exact form stays
// inert text. No engine command ever runs because of a proposal.

const (
	// MaxChangeBytes caps one proposed file.
	MaxChangeBytes = 128 << 10
	// maxProposals is the most proposals read from one reply; more are ignored.
	maxProposals = 20
	baseNew      = "new"
)

// Change states.
const (
	ChangeReady    = "ready"
	ChangeInvalid  = "invalid"
	ChangeApplied  = "applied"
	ChangeRejected = "rejected"
	ChangeUndone   = "undone"
)

var (
	changeOpen = regexp.MustCompile("^(`{3,})hclaro-change[ \\t]+path=(\\S+)[ \\t]+base=(\\S+)[ \\t]*$")
	// changePath is a relative slash path of visible names: no "..", hidden folders, drive
	// letters, backslashes or absolute paths.
	changePath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)
	sha256Hex  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	heredocAt  = regexp.MustCompile(`^<<-?([A-Za-z_][A-Za-z0-9_-]*)[ \t]*$`)
	// placeholders are markers Hclaro writes into shared text in place of hidden values.
	placeholders = []string{"[redacted", "<default omitted>", "[backend configuration omitted]", notShown, "more bytes not shown]"}
)

// proposal is one well-formed block of a reply.
type proposal struct {
	index            int
	path, base, text string
}

// proposals reads the well-formed change blocks of a reply, in order. A block without its
// closing fence, or with any other header, is not a proposal.
func proposals(reply string) []proposal {
	out := []proposal{}
	lines := strings.Split(reply, "\n")
	for i := 0; i < len(lines) && len(out) < maxProposals; i++ {
		m := changeOpen.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if m == nil {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if l := strings.TrimRight(lines[j], " \t\r"); len(l) >= len(m[1]) && strings.Trim(l, "`") == "" {
				body := strings.Join(lines[i+1:j], "\n")
				if j > i+1 {
					body += "\n"
				}
				out = append(out, proposal{index: len(out), path: m[2], base: m[3], text: strings.ReplaceAll(body, "\r\n", "\n")})
				i = j
				break
			}
		}
	}
	return out
}

// ChangeView is one proposal as the UI shows it. The diff is computed by Hclaro from the file
// on disk and the proposed text; the model's own description is never trusted.
type ChangeView struct {
	Turn       int    `json:"turn"`
	Index      int    `json:"index"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Created    bool   `json:"created"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
	NewSHA256  string `json:"newSha256,omitempty"`
	Diff       string `json:"diff,omitempty"`
	Added      int    `json:"added"`
	Removed    int    `json:"removed"`
	CanUndo    bool   `json:"canUndo,omitempty"`
	// Aside names the replaced file's recovery artifact next to it (AI-01.12).
	Aside string `json:"aside,omitempty"`
}

// Validation codes of a proposal that cannot be applied.
var changeReasons = map[string]string{
	"change_path":       "Hclaro writes only .tf and .tofu files inside the project folder: never hidden folders such as .terraform, variable files, the lock file or a path outside the project.",
	"change_folder":     "The folder does not exist or is a symbolic link.",
	"change_ignored":    "Git ignores this new file's name (a .gitignore rule in its folder or a parent folder, .git/info/exclude or core.excludesFile), or Git could not tell. A file Git ignores is not part of the project.",
	"change_scope":      "The file is not one Hclaro shares as project context: a new file can only go into a folder whose configuration is in the package, and only a file in the package can be replaced (never a file Git ignores, a nested repository or submodule).",
	"change_project":    "The project folder was moved or replaced by a symbolic link since Hclaro started.",
	"change_link":       "This file is a symbolic link or not a regular file. Hclaro never writes through links.",
	"change_base":       "The proposal does not name the file's version: the sha256 from the context package, or new for a new file.",
	"change_exists":     "The proposal creates a new file, but a file with that name already exists.",
	"change_missing":    "The file this proposal replaces no longer exists.",
	"change_stale":      "The file changed since the assistant saw it. Ask for a new proposal.",
	"change_edited":     "Hclaro shared this file with values hidden or removed, so replacing it whole would lose them. Edit it yourself.",
	"change_backend":    "Proposals cannot add, change or remove backend or cloud configuration.",
	"change_size":       fmt.Sprintf("The proposed file is empty or larger than %d KiB.", MaxChangeBytes>>10),
	"change_text":       "The proposed text has characters that are not plain text.",
	"change_unsafe":     "The proposed text looks like it contains a secret, a credential, a local path or a value Hclaro hid, so it cannot be written.",
	"change_syntax":     "The proposed text is not well-formed HCL: unbalanced brackets, or an unterminated string, comment or heredoc.",
	"change_same":       "The proposed text is identical to the file.",
	"change_applied":    "The file already holds the proposed text.",
	"change_unreadable": "Hclaro could not read the current file safely.",
}

// ChangeReason is the message of a validation code, for history.
func ChangeReason(code string) string {
	if m, ok := changeReasons[code]; ok {
		return m
	}
	return Message(code)
}

func invalid(v ChangeView, code string) ChangeView {
	v.Status, v.Code, v.Reason, v.Diff = ChangeInvalid, code, changeReasons[code], ""
	return v
}

// normalized is file text as the context package shares it: no UTF-8 byte order mark, LF lines.
func normalized(data []byte) string {
	return strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// checkChange validates p against the project at root now. A ready view carries the diff; old
// is the current file's bytes when p replaces one.
func checkChange(root string, p proposal, git GitFiles) (v ChangeView, old []byte) {
	b := newBuilder(KindProject, Scope{Root: root})
	v = ChangeView{Index: p.index, Path: b.label(p.path), Created: p.base == baseNew, NewSHA256: sum(p.text)}
	if p.base != baseNew {
		v.BaseSHA256 = p.base
	}
	ext := path.Ext(p.path)
	switch {
	case !validChangePath(p.path) || ext != ".tf" && ext != ".tofu":
		return invalid(v, "change_path"), nil
	case p.base != baseNew && !sha256Hex.MatchString(p.base):
		return invalid(v, "change_base"), nil
	case strings.TrimSpace(p.text) == "" || len(p.text) > MaxChangeBytes:
		return invalid(v, "change_size"), nil
	case !plainText(p.text):
		return invalid(v, "change_text"), nil
	case hasPlaceholder(p.text) || b.hit(strings.Split(p.text, "\n")):
		return invalid(v, "change_unsafe"), nil
	case checkHCL(p.text) != nil:
		return invalid(v, "change_syntax"), nil
	case hasBackend(p.text):
		return invalid(v, "change_backend"), nil
	}
	if real, err := filepath.EvalSymlinks(root); err != nil || real != root {
		return invalid(v, "change_project"), nil
	}
	dir, err := imports.SafeDir(root, p.path)
	if err != nil {
		return invalid(v, "change_folder"), nil
	}
	// The package's own scope (walkProject): only files it can share, and new files only in a
	// folder whose configuration it shares.
	listed, folder := inScope(root, p.path, git)
	if !folder || p.base != baseNew && !listed {
		return invalid(v, "change_scope"), nil
	}
	// A new name is in no listing: Git itself says whether its rules would ignore it.
	if p.base == baseNew && git.Repo {
		if git.Ignored == nil {
			return invalid(v, "change_ignored"), nil
		}
		if ignored, err := git.Ignored(p.path); err != nil || ignored {
			return invalid(v, "change_ignored"), nil
		}
	}
	target := filepath.Join(dir, path.Base(p.path))
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist) && p.base == baseNew:
		v.Diff, v.Added, v.Removed = unifiedDiff(p.path, nil, splitLines(p.text))
		v.Status = ChangeReady
		return v, nil
	case errors.Is(err, fs.ErrNotExist):
		return invalid(v, "change_missing"), nil
	case err != nil:
		return invalid(v, "change_unreadable"), nil
	case !info.Mode().IsRegular():
		return invalid(v, "change_link"), nil
	}
	data, reason := readBounded(target)
	if reason != "" {
		return invalid(v, "change_unreadable"), nil
	}
	text := normalized(data)
	switch {
	case text == p.text:
		return invalid(v, "change_applied"), nil
	case p.base == baseNew:
		return invalid(v, "change_exists"), nil
	case sum(text) != p.base:
		return invalid(v, "change_stale"), nil
	case hasBackend(text):
		return invalid(v, "change_backend"), nil
	case !sharedAsIs(root, p.path, text):
		return invalid(v, "change_edited"), nil
	}
	v.Diff, v.Added, v.Removed = unifiedDiff(p.path, splitLines(text), splitLines(p.text))
	v.Status = ChangeReady
	return v, data
}

func validChangePath(p string) bool {
	if len(p) > 512 || strings.Count(p, "/") >= 16 || !changePath.MatchString(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if reservedName.MatchString(part) {
			return false
		}
	}
	return true
}

func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && !unicode.IsGraphic(r) {
			return false
		}
	}
	return true
}

func hasPlaceholder(s string) bool {
	for _, p := range placeholders {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// inScope reports whether rel is a readable HCL file of the project package (listed) and
// whether its folder holds one (folder). Git-ignored files, hidden folders, nested repositories
// and submodules are outside, as for the package; an unreadable scope is outside.
func inScope(root, rel string, git GitFiles) (listed, folder bool) {
	cands, _, _, err := walkProject(root, git, false)
	if err != nil {
		return false, false
	}
	for _, c := range cands {
		if c.kind != "hcl" || c.reason != "" {
			continue
		}
		listed = listed || c.rel == rel
		folder = folder || path.Dir(c.rel) == path.Dir(rel)
	}
	return listed, folder
}

// reservedName matches Windows device names, which open a device instead of a file.
var reservedName = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)`)

// backendWord is a backend block with a quoted or identifier label (HCL accepts both), or a
// cloud block, with any whitespace before the label or brace.
var backendWord = regexp.MustCompile(`\bbackend\b\s*["A-Za-z_]|\bcloud\s*\{`)

// hasBackend reports a backend or cloud block. It looks at the text with comments removed (so
// a comment between the keyword and its label or brace hides nothing) and at the raw text (so
// the word in a comment or string is refused too: false positives fail closed).
func hasBackend(text string) bool {
	code, err := stripComments(text)
	return err != nil || backendWord.MatchString(code) || backendBlock.MatchString(text) || backendOther.MatchString(text)
}

// checkHCL is a structural check. It guarantees only that, outside strings, comments and
// heredoc bodies, every (, [ and { closes in order, and that every quoted string ends on its
// line (templates included), every /* comment and every heredoc ends. It does not check
// attribute or block syntax, expressions, references, types or Terraform semantics: the
// engine's validate or plan, which the user runs next, does.
// shortcut: not a full HCL parse (Hclaro has no HCL parser dependency).
func checkHCL(text string) error {
	_, err := stripComments(text)
	return err
}

// stripComments returns text with every comment replaced by spaces (line breaks kept), after
// the structural check above; an error means the text is not well-formed.
func stripComments(text string) (string, error) {
	bad := errors.New("not well-formed HCL")
	out := []byte(text)
	blank := func(from, to int) {
		for k := from; k < to; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	var stack []byte
	inComment := false
	lines := strings.Split(text, "\n")
	at := 0 // offset of the current line in text
	for i := 0; i < len(lines); i++ {
		l := lines[i]
	line:
		for j := 0; j < len(l); j++ {
			c := l[j]
			switch {
			case inComment:
				blank(at+j, at+j+1)
				if strings.HasPrefix(l[j:], "*/") {
					inComment = false
					blank(at+j, at+j+2)
					j++
				}
			case c == '#' || strings.HasPrefix(l[j:], "//"):
				blank(at+j, at+len(l))
				break line
			case strings.HasPrefix(l[j:], "/*"):
				inComment = true
				blank(at+j, at+j+2)
				j++
			case c == '"':
				_, end, ok := imports.ReadString(l, j)
				if !ok {
					return "", bad
				}
				j = end - 1
			case strings.HasPrefix(l[j:], "<<"):
				m := heredocAt.FindStringSubmatch(l[j:])
				if m == nil {
					return "", bad
				}
				for at += len(l) + 1; i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != m[1]; i++ {
					at += len(lines[i+1]) + 1
				}
				if i+1 == len(lines) {
					return "", bad
				}
				i++
				l = lines[i]
				break line
			case c == '{' || c == '[' || c == '(':
				stack = append(stack, c)
			case c == '}' || c == ']' || c == ')':
				open := map[byte]byte{'}': '{', ']': '[', ')': '('}[c]
				if len(stack) == 0 || stack[len(stack)-1] != open {
					return "", bad
				}
				stack = stack[:len(stack)-1]
			}
		}
		at += len(l) + 1
	}
	if inComment || len(stack) > 0 {
		return "", bad
	}
	return string(out), nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// maxDiffCells bounds the line comparison table; a larger changed middle shows as replaced.
const maxDiffCells = 4 << 20

// unifiedDiff returns a unified diff with 3 lines of context and the added and removed counts.
// Lines are passed through display, so control characters never reach the UI raw.
func unifiedDiff(name string, a, b []string) (string, int, int) {
	type op struct {
		kind byte
		text string
	}
	ops := []op{}
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	for _, l := range a[:p] {
		ops = append(ops, op{' ', l})
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]
	n, m := len(ma), len(mb)
	if n*m > maxDiffCells {
		for _, l := range ma {
			ops = append(ops, op{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, op{'+', l})
		}
	} else {
		// t[i][j] is the longest common subsequence of ma[i:] and mb[j:].
		w := m + 1
		t := make([]int32, (n+1)*w)
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					t[i*w+j] = t[(i+1)*w+j+1] + 1
				} else {
					t[i*w+j] = max(t[(i+1)*w+j], t[i*w+j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && ma[i] == mb[j]:
				ops = append(ops, op{' ', ma[i]})
				i, j = i+1, j+1
			case j == m || i < n && t[(i+1)*w+j] >= t[i*w+j+1]:
				ops = append(ops, op{'-', ma[i]})
				i++
			default:
				ops = append(ops, op{'+', mb[j]})
				j++
			}
		}
	}
	for _, l := range a[len(a)-s:] {
		ops = append(ops, op{' ', l})
	}
	var out strings.Builder
	from := "a/" + name
	if a == nil {
		from = "/dev/null"
	}
	out.WriteString("--- " + from + "\n+++ b/" + name + "\n")
	added, removed := 0, 0
	// aAt and bAt are the line numbers before ops[k].
	aAt, bAt := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for k, o := range ops {
		aAt[k+1], bAt[k+1] = aAt[k], bAt[k]
		if o.kind != '+' {
			aAt[k+1]++
		}
		if o.kind != '-' {
			bAt[k+1]++
		}
		if o.kind == '+' {
			added++
		} else if o.kind == '-' {
			removed++
		}
	}
	const context = 3
	for k := 0; k < len(ops); {
		for k < len(ops) && ops[k].kind == ' ' {
			k++
		}
		if k == len(ops) {
			break
		}
		start, last := max(0, k-context), k
		for q := k; q < len(ops) && q-last <= 2*context; q++ {
			if ops[q].kind != ' ' {
				last = q
			}
		}
		stop := min(len(ops), last+context+1)
		header := func(at []int) string {
			count := at[stop] - at[start]
			if count == 0 {
				return fmt.Sprintf("%d,0", at[start])
			}
			return fmt.Sprintf("%d,%d", at[start]+1, count)
		}
		out.WriteString("@@ -" + header(aAt) + " +" + header(bAt) + " @@\n")
		for _, o := range ops[start:stop] {
			out.WriteString(string(o.kind) + display(o.text, true) + "\n")
		}
		k = stop
	}
	return out.String(), added, removed
}

// changeState is what this session decided about one proposal.
type changeState struct {
	view   ChangeView
	backup string // the backup file name of a replaced file
}

// ChangeRequest names one proposal and the versions the user reviewed: the apply is refused
// unless Hclaro would write exactly that diff.
type ChangeRequest struct {
	Turn       int    `json:"turn"`
	Index      int    `json:"index"`
	BaseSHA256 string `json:"baseSha256"`
	NewSHA256  string `json:"newSha256"`
}

// ChangeRecord is the metadata kept about a decision: never file content.
type ChangeRecord struct {
	RunScope
	Conversation, Provider, Path, BaseSHA256, NewSHA256, Outcome, Code string
	At                                                                 time.Time
}

// Change refusals.
const (
	RefuseChangeNotFound = "change_not_found"
	RefuseChangeDecided  = "change_decided"
	RefuseChangeReviewed = "change_review_changed"
	RefuseChangeBusy     = "change_busy"
	RefuseChangeNoBackup = "change_no_backup"
	RefuseChangeFailed   = "change_write_failed"
	RefuseChangeNoUndo   = "change_undo_unavailable"
	RefuseChangeInvalid  = "change_invalid"
	RefuseChangeConflict = "change_conflict"
	RefuseChangeNoLinks  = "change_unsupported_fs"
)

// keptAside explains a file another program wrote while Hclaro wrote: it was not lost.
const keptAside = "Another program wrote into this file while Hclaro replaced it. That version is kept next to it as %s and is never deleted by Hclaro; compare it with the file and delete it when done."

func init() {
	for code, msg := range map[string]string{
		RefuseChangeNotFound: "This proposal is not in the conversation. Reload it.",
		RefuseChangeDecided:  "This proposal was already applied or rejected.",
		RefuseChangeReviewed: "The file or the proposal changed since you reviewed the diff. Nothing was written. Review it again.",
		RefuseChangeBusy:     "An operation is running in this workspace. Nothing was written. Wait for it to finish, then apply again.",
		RefuseChangeNoBackup: "Hclaro cannot keep a backup (the per-user configuration folder is unavailable), so it writes no proposed change.",
		RefuseChangeFailed:   "The file could not be written safely. Nothing was changed.",
		RefuseChangeNoUndo:   "Undo is not possible: the file changed after the proposal was applied, or its backup is gone. Nothing was written.",
		RefuseChangeInvalid:  "This proposal cannot be applied. Nothing was written.",
		RefuseChangeNoLinks:  "This folder's file system does not support hard links, which Hclaro needs to write without ever overwriting another program's save. Nothing was changed; edit the file yourself.",
		RefuseChangeConflict: "Another program changed this file at the same moment, so Hclaro wrote nothing. Its version was kept next to the file under a hidden name; compare it with the file and delete it when done.",
	} {
		refusalMessages[code] = msg
	}
}

// changeTarget is the conversation and reply a change request names, read under mu.
type changeTarget struct {
	c     *conversation
	reply string
	scope RunScope
}

// target finds the assistant reply turn of conversation id in scope key. The conversation must
// still be in the project and workspace of scope.
func (s *Session) target(key, id string, turn int, scope RunScope) (changeTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[id]
	switch {
	case !s.enabled:
		return changeTarget{}, refuse(RefuseDisabled)
	case !ok || c.key != key:
		return changeTarget{}, refuse(RefuseNoChat)
	case c.scope.Root == "" || c.scope.Root != scope.Root || c.scope.Workspace != scope.Workspace:
		return changeTarget{}, refuse(RefuseStale)
	case turn < 0 || turn >= len(c.view.Turns) || c.view.Turns[turn].Role != "assistant" || c.view.Turns[turn].Hidden:
		return changeTarget{}, refuse(RefuseChangeNotFound)
	}
	return changeTarget{c: c, reply: c.view.Turns[turn].Text, scope: c.scope}, nil
}

func changeKey(turn, index int) string { return fmt.Sprintf("%d/%d", turn, index) }

// Changes lists the proposals of every reply in conversation id, validated against the project
// files now. Decided ones show the decision.
func (s *Session) Changes(key, id string, scope RunScope, git func() GitFiles) ([]ChangeView, error) {
	s.mu.Lock()
	c, ok := s.conversations[id]
	if !ok || c.key != key || !s.enabled {
		s.mu.Unlock()
		return nil, refuse(RefuseNoChat)
	}
	turns := append([]Turn{}, c.view.Turns...)
	decided := map[string]ChangeView{}
	for k, st := range c.changes {
		decided[k] = st.view
	}
	here := c.scope.Root != "" && c.scope.Root == scope.Root && c.scope.Workspace == scope.Workspace
	s.mu.Unlock()
	out := []ChangeView{}
	var files *GitFiles
	for t, turn := range turns {
		if turn.Role != "assistant" || turn.Hidden {
			continue
		}
		for _, p := range proposals(turn.Text) {
			v, ok := decided[changeKey(t, p.index)]
			if !ok {
				if files == nil {
					f := git()
					files = &f
				}
				v, _ = checkChange(scope.Root, p, *files)
				if !here {
					v = invalid(v, "change_project")
					v.Reason = "This conversation belongs to another project or workspace."
				}
			}
			v.Turn = t
			out = append(out, v)
		}
	}
	return out, nil
}

// pick finds proposal index of the target reply and refuses one already decided.
func (s *Session) pick(t changeTarget, turn, index int, undo bool) (proposal, *changeState, error) {
	all := proposals(t.reply)
	if index < 0 || index >= len(all) {
		return proposal{}, nil, refuse(RefuseChangeNotFound)
	}
	s.mu.Lock()
	st := t.c.changes[changeKey(turn, index)]
	s.mu.Unlock()
	if undo && (st == nil || st.view.Status != ChangeApplied) || !undo && st != nil {
		if undo {
			return proposal{}, nil, refuse(RefuseChangeNoUndo)
		}
		return proposal{}, nil, refuse(RefuseChangeDecided)
	}
	return all[index], st, nil
}

// decide records the decision and its history entry.
func (s *Session) decide(t changeTarget, turn int, st *changeState, code string) {
	s.mu.Lock()
	if t.c.changes == nil {
		t.c.changes = map[string]*changeState{}
	}
	st.view.Turn = turn
	t.c.changes[changeKey(turn, st.view.Index)] = st
	record := s.RecordChange
	s.mu.Unlock()
	s.recordChange(record, t, st.view, st.view.Status, code)
}

func (s *Session) recordChange(record func(ChangeRecord), t changeTarget, v ChangeView, outcome, code string) {
	if record == nil {
		return
	}
	p := v.Path
	if !validChangePath(p) {
		p = ""
	}
	record(ChangeRecord{RunScope: t.scope, Conversation: t.c.view.ID, Provider: t.c.view.Provider, Path: p, BaseSHA256: v.BaseSHA256, NewSHA256: v.NewSHA256, Outcome: outcome, Code: code, At: s.Now()})
}

// ApplyChange writes one proposal after validating it again. exclusive runs the write with the
// workspace's operation lock held and fails without running it when an operation is running.
// The file must still match what the user reviewed (req). A new file is hard-linked into place
// (imports.CreateAt). A replaced file is first copied to the backup folder, then replaced with
// imports.ReplaceAt, which keeps the moved file as a recovery artifact (see moved).
func (s *Session) ApplyChange(key, id string, req ChangeRequest, scope RunScope, git func() GitFiles, exclusive func(func() error) error) (ChangeView, error) {
	t, err := s.target(key, id, req.Turn, scope)
	if err != nil {
		return ChangeView{}, err
	}
	if s.Store == nil {
		return ChangeView{}, refuse(RefuseChangeNoBackup)
	}
	var out ChangeView
	ran := false
	err = exclusive(func() error {
		ran = true
		// Under the lock, so two applies of one proposal cannot both pass.
		p, _, err := s.pick(t, req.Turn, req.Index, false)
		if err != nil {
			return err
		}
		v, old := checkChange(scope.Root, p, git())
		switch {
		case v.Status != ChangeReady:
			s.recordChange(s.RecordChange, t, v, "refused", v.Code)
			return refuse(RefuseChangeInvalid)
		case v.BaseSHA256 != req.BaseSHA256 || v.NewSHA256 != req.NewSHA256:
			return refuse(RefuseChangeReviewed)
		}
		st := &changeState{view: v}
		unsettled := false
		if old == nil {
			err = imports.CreateAt(scope.Root, p.path, []byte(p.text))
		} else {
			// Exactly the bytes in the backup: a file that changed in any way is not replaced.
			current := func(b []byte) bool { return bytes.Equal(b, old) }
			st.backup, unsettled, err = s.moved(scope, p.path, old, func(keep string) error {
				return imports.ReplaceAt(scope.Root, p.path, []byte(p.text), current, keep)
			})
			if st.backup != "" {
				st.view.Aside = imports.AsideName(st.backup)
			}
		}
		var aside *imports.AsideError
		switch {
		case errors.As(err, &aside) && aside.Written:
			// Written, but another program wrote into the replaced file meanwhile: that version
			// is the kept artifact. Undo stays available from the backup.
			st.view.Reason = fmt.Sprintf(keptAside, aside.Name)
		case err != nil:
			return changeError(err)
		}
		if unsettled {
			st.view.Reason = strings.TrimSpace(st.view.Reason + " " + unsettledReason)
		}
		st.view.Status, st.view.CanUndo = ChangeApplied, true
		s.decide(t, req.Turn, st, "")
		out = st.view
		return nil
	})
	if !ran {
		return ChangeView{}, refuse(RefuseChangeBusy)
	}
	return out, err
}

// RejectChange records that the user rejected one proposal. Nothing is written.
func (s *Session) RejectChange(key, id string, turn, index int, scope RunScope, git func() GitFiles) (ChangeView, error) {
	t, err := s.target(key, id, turn, scope)
	if err != nil {
		return ChangeView{}, err
	}
	p, _, err := s.pick(t, turn, index, false)
	if err != nil {
		return ChangeView{}, err
	}
	v, _ := checkChange(scope.Root, p, git())
	v.Status, v.Code, v.Reason = ChangeRejected, "", ""
	st := &changeState{view: v}
	s.decide(t, turn, st, "")
	return st.view, nil
}

// UndoChange puts back what an applied proposal replaced (or removes the file it created), only
// while the file still holds exactly the applied text. It runs under the same lock as apply.
func (s *Session) UndoChange(key, id string, turn, index int, scope RunScope, exclusive func(func() error) error) (ChangeView, error) {
	t, err := s.target(key, id, turn, scope)
	if err != nil {
		return ChangeView{}, err
	}
	var out ChangeView
	ran := false
	err = exclusive(func() error {
		ran = true
		p, st, err := s.pick(t, turn, index, true)
		if err != nil {
			return err
		}
		applied := func(b []byte) bool { return sum(string(b)) == st.view.NewSHA256 }
		var kept string
		unsettled := false
		// The applied text is backed up too: the file Undo moves aside is its recovery artifact.
		if st.view.Created {
			kept, unsettled, err = s.moved(scope, p.path, []byte(p.text), func(keep string) error {
				return imports.RemoveAt(scope.Root, p.path, applied, keep)
			})
		} else {
			old, rerr := s.Store.ReadBackup(scope.ProjectKey, st.backup)
			if rerr != nil {
				return refuse(RefuseChangeNoUndo) // the backup is gone (retention) or not plain
			}
			kept, unsettled, err = s.moved(scope, p.path, []byte(p.text), func(keep string) error {
				return imports.ReplaceAt(scope.Root, p.path, old, applied, keep)
			})
		}
		var aside *imports.AsideError
		switch {
		case errors.As(err, &aside) && aside.Written:
		case errors.Is(err, imports.ErrChanged), errors.Is(err, imports.ErrUnsafePath):
			return refuse(RefuseChangeNoUndo) // edited after apply: never restored over
		case err != nil:
			return changeError(err)
		}
		next := &changeState{view: st.view, backup: st.backup}
		if unsettled && st.view.Created && kept != "" && putBackRemoved(scope.Root, p.path, kept) {
			// The next start would restore the removed file anyway; restore it now and say so.
			return refuse(RefuseChangeFailed)
		}
		next.view.Status, next.view.CanUndo = ChangeUndone, false
		if unsettled {
			next.view.Reason = unsettledReason
		}
		s.decide(t, turn, next, "")
		out = next.view
		return nil
	})
	if !ran {
		return ChangeView{}, refuse(RefuseChangeBusy)
	}
	return out, err
}

// moved runs op, a replacement or removal of rel that moves the file aside, with data (the
// bytes the moved file must hold) backed up under a new ID first and a record of the recovery
// artifact written before anything moves (Store.Retain), so an interrupted change is rolled back
// at the next start. Afterwards the record is kept while the artifact exists, and dropped with
// the backup when nothing was moved.
func (s *Session) moved(scope RunScope, rel string, data []byte, op func(keep string) error) (id string, unsettled bool, err error) {
	id, err = s.Store.Backup(scope.ProjectKey, data)
	if err != nil {
		return "", false, refuse(RefuseChangeNoBackup)
	}
	if err := s.Store.Retain(scope.ProjectKey, id, scope.Root, rel); err != nil {
		s.Store.RemoveBackup(scope.ProjectKey, id)
		return "", false, refuse(RefuseChangeNoBackup)
	}
	err = op(id)
	kept := false
	if dir, derr := imports.SafeDir(scope.Root, rel); derr == nil {
		_, lerr := os.Lstat(filepath.Join(dir, imports.AsideName(id)))
		kept = lerr == nil
	}
	unsettled = s.Store.Settle(scope.ProjectKey, id, scope.Root, rel, kept) != nil
	if !kept {
		id = ""
	}
	return id, unsettled, err
}

// putBackRemoved links the moved-aside file keep back to rel, never over an existing name, as
// the start-up sweep would. It reports whether the file is back.
func putBackRemoved(root, rel, keep string) bool {
	dir, err := imports.SafeDir(root, rel)
	if err != nil {
		return false
	}
	aside := filepath.Join(dir, imports.AsideName(keep))
	if os.Link(aside, filepath.Join(dir, path.Base(rel))) != nil {
		return false
	}
	_ = os.Remove(aside) // only a second name of the restored file
	return true
}

const unsettledReason = "The file was written, but Hclaro could not record in its settings folder that the change finished. If Hclaro restarts before that folder is writable, it may treat the change as interrupted and put the moved-aside file back."

// changeError maps a write failure to its refusal.
func changeError(err error) error {
	var r *Refusal
	switch {
	case errors.As(err, &r):
		return err
	case errors.Is(err, imports.ErrKeptAside):
		return refuse(RefuseChangeConflict)
	case errors.Is(err, imports.ErrNoLinks):
		return refuse(RefuseChangeNoLinks)
	case errors.Is(err, imports.ErrChanged), errors.Is(err, imports.ErrFileExists):
		return refuse(RefuseChangeReviewed)
	}
	return refuse(RefuseChangeFailed)
}
