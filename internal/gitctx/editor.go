package gitctx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Editors maps the only accepted editor names to their URL schemes. The browser chooses a
// name from this list; it can never supply an executable, scheme or URL.
var Editors = map[string]string{"vscode": "vscode", "cursor": "cursor"}

// EditorLabels are display names.
var EditorLabels = map[string]string{"vscode": "VS Code", "cursor": "Cursor"}

// Editor errors.
var (
	ErrEditor     = errors.New("choose VS Code or Cursor")
	ErrLine       = errors.New("the line is outside the file")
	ErrOpenFailed = errors.New("the operating system could not hand the link to the editor")
)

// EditorResult reports what was opened, without the absolute path.
type EditorResult struct {
	Editor string `json:"editor"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
}

// EditorURL builds the editor's file URL for an absolute path and line, for example
// vscode://file/Users/me/infra/main.tf:12. Path segments are percent-encoded, so spaces,
// quotes, ampersands or other shell-relevant characters stay data.
func EditorURL(editor, abs string, line int) (string, error) {
	scheme, ok := Editors[editor]
	if !ok {
		return "", ErrEditor
	}
	if !filepath.IsAbs(abs) || line < 1 {
		return "", ErrLine
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		// Windows: C:/x → /C:/x
		p = "/" + p
	}
	return scheme + "://file" + escapePath(p) + ":" + strconv.Itoa(line), nil
}

// escapePath percent-encodes every byte except unreserved characters, '/' and ':' (drive
// letters), stricter than url.PathEscape, so no shell- or handler-relevant character such
// as '&', '$', '(', ';', quotes or spaces survives.
func escapePath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~/:", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// OpenURL hands a URL to the operating system's URL handler with a fixed argv. It is only
// called with URLs built by EditorURL.
func OpenURL(ctx context.Context, link string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", link)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", link)
	}
	return cmd.Run()
}

// maxEditorFile bounds the read used to validate a line number.
const maxEditorFile = 16 << 20

// OpenEditor opens an IaC file of root's scope, named by its opaque ID, in a supported
// editor at line. The ID is resolved against the files in scope now; symlinks must stay
// inside the scope and the line must exist.
func (s *Service) OpenEditor(ctx context.Context, root, id, editor string, line int) (EditorResult, error) {
	if _, ok := Editors[editor]; !ok {
		return EditorResult{}, ErrEditor
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return EditorResult{}, ErrNotFound
	}
	top := FindGit(canonical)
	if top != "" {
		top, _ = filepath.EvalSymlinks(top)
	}
	scope, err := NewScope(canonical, top, s.environ())
	if err != nil {
		return EditorResult{}, ErrNotFound
	}
	files, err := scope.Files()
	if err != nil {
		return EditorResult{}, ErrNotFound
	}
	var target *iacFile
	for i := range files {
		if s.ids.of(scope.Base, files[i].Rel) == id {
			target = &files[i]
		}
	}
	if target == nil {
		return EditorResult{}, ErrNotFound
	}
	b, truncated, err := scope.open(target.Abs, maxEditorFile)
	if err != nil {
		return EditorResult{}, err
	}
	lines := bytes.Count(b, []byte{'\n'}) + 1
	if line < 1 || line > lines && !truncated {
		return EditorResult{}, ErrLine
	}
	real, err := filepath.EvalSymlinks(target.Abs)
	if err != nil {
		return EditorResult{}, ErrNotFound
	}
	link, err := EditorURL(editor, real, line)
	if err != nil {
		return EditorResult{}, err
	}
	if s.OpenURL == nil || s.OpenURL(ctx, link) != nil {
		return EditorResult{}, ErrOpenFailed
	}
	return EditorResult{Editor: editor, Path: target.Rel, Line: line}, nil
}
