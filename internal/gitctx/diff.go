package gitctx

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"hclaro/internal/imports"
)

// DiffLine is one displayed diff line. Old and New are 1-based line numbers.
type DiffLine struct {
	// Kind is context, add, delete or note ("\ No newline at end of file").
	Kind     string `json:"kind"`
	Old      int    `json:"old,omitempty"`
	New      int    `json:"new,omitempty"`
	Text     string `json:"text"`
	Redacted bool   `json:"redacted,omitempty"`
}

// GitDiffHunk is one hunk of a redacted line diff.
type GitDiffHunk struct {
	OldStart int        `json:"oldStart"`
	OldLines int        `json:"oldLines"`
	NewStart int        `json:"newStart"`
	NewLines int        `json:"newLines"`
	Section  string     `json:"section,omitempty"`
	Lines    []DiffLine `json:"lines"`
}

// GitDiff is the redacted diff of one file against HEAD (or the whole file when it is new
// to Git).
type GitDiff struct {
	File      GitFileStatus `json:"file"`
	Base      string        `json:"base"`
	Hunks     []GitDiffHunk `json:"hunks"`
	Added     int           `json:"added"`
	Removed   int           `json:"removed"`
	Redacted  int           `json:"redacted"`
	Truncated bool          `json:"truncated"`
	Binary    bool          `json:"binary"`
	Notice    string        `json:"notice,omitempty"`
}

// Diff errors. Messages are safe to show.
var (
	ErrUnknownFile = errors.New("this file is no longer in the list of changed files; refresh the Git status")
	ErrNotDiffable = errors.New("only .tf and .tf.json configuration files can be diffed")
	ErrDiffFailed  = errors.New("git could not produce a diff for this file")
	ErrMalformed   = errors.New("git returned diff output Hclaro could not read safely; nothing is shown")
)

const (
	maxDiffOutput = 1 << 20
	maxDiffLines  = 4000
	maxNewFile    = 512 << 10
	// fullContext asks Git for the whole file as context: more lines than fit in
	// maxDiffOutput, so the redaction sees every block a changed line sits in. The display
	// is cut back to displayContext lines around each change.
	fullContext    = "--unified=1048576"
	displayContext = 3
)

// Diff returns the redacted line diff of one changed configuration file, named by the ID
// from the latest status. The ID is checked against a fresh status, so a stale or forged
// ID is refused, and only HCL and JSON configuration in the project scope is read.
func (s *Service) Diff(ctx context.Context, root, id string) (GitDiff, error) {
	status, scope := s.status(ctx, root)
	if status.State != StateRepository || scope == nil {
		return GitDiff{}, ErrDiffFailed
	}
	var file *GitFileStatus
	for i := range status.Files {
		if status.Files[i].ID == id {
			file = &status.Files[i]
		}
	}
	if file == nil {
		return GitDiff{}, ErrUnknownFile
	}
	if !file.Diffable {
		return GitDiff{File: *file}, ErrNotDiffable
	}
	d := GitDiff{File: *file, Base: "HEAD", Hunks: []GitDiffHunk{}}
	abs := filepath.Join(scope.Base, filepath.FromSlash(file.Path))
	// Files Git does not have at HEAD are shown whole, read directly and bounded.
	if file.Status == "untracked" || status.Head.Unborn {
		d.Base = "none"
		b, truncated, err := scope.open(abs, maxNewFile)
		if err != nil {
			return GitDiff{File: *file}, err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			d.Binary, d.Notice = true, "This file looks binary; its content is not shown."
			return d, nil
		}
		d.Truncated = truncated
		d.Hunks = []GitDiffHunk{newFileHunk(string(b), &d)}
		return d, nil
	}
	// The working copy must still resolve inside the scope (a symlink swapped to point
	// elsewhere is refused) unless the file was deleted.
	if file.Status != "deleted" {
		if _, _, err := scope.open(abs, 1); err != nil {
			return GitDiff{File: *file}, err
		}
	}
	raw, overflow, err := s.Runner.Run(ctx, scope.Base, maxDiffOutput, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", fullContext, "HEAD", "--", ":(literal)"+file.Path)
	if err != nil {
		return GitDiff{File: *file}, ErrDiffFailed
	}
	if err := ParseDiff(raw, overflow, &d); err != nil {
		return GitDiff{File: *file}, err
	}
	return d, nil
}

func newFileHunk(text string, d *GitDiff) GitDiffHunk {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	h := GitDiffHunk{OldStart: 0, NewStart: 1, Lines: []DiffLine{}}
	r := &LineRedactor{}
	forced := imports.SecretLines(lines)
	for i, l := range lines {
		if len(h.Lines) == maxDiffLines {
			d.Truncated = true
			break
		}
		t, red := r.line(l, forced[i])
		if red {
			d.Redacted++
		}
		h.Lines = append(h.Lines, DiffLine{Kind: "add", New: i + 1, Text: t, Redacted: red})
		d.Added++
	}
	h.NewLines = len(h.Lines)
	return h
}

// headerPrefixes are the extended header lines git diff prints before the first hunk.
var headerPrefixes = []string{"diff --git ", "index ", "--- ", "+++ ", "new file mode ", "deleted file mode ", "old mode ", "new mode ", "similarity index ", "dissimilarity index ", "rename from ", "rename to ", "copy from ", "copy to "}

// ParseDiff parses unified diff output for one file into redacted hunks. Unknown lines,
// inconsistent hunk counts and more than one file are refused. Binary diffs are reported
// without content. overflow marks output that was cut at the size limit. Output is expected
// with full context (fullContext); see display for what happens otherwise.
func ParseDiff(raw []byte, overflow bool, d *GitDiff) error {
	text := string(raw)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if overflow && len(lines) > 0 {
		// The last line may be cut in the middle.
		lines = lines[:len(lines)-1]
		d.Truncated = true
	}
	d.Hunks = []GitDiffHunk{}
	var h *GitDiffHunk
	oldLeft, newLeft, oldNo, newNo := 0, 0, 0, 0
	files := 0
	finish := func() error {
		if h == nil {
			return nil
		}
		if oldLeft != 0 || newLeft != 0 {
			return ErrMalformed
		}
		d.Hunks = append(d.Hunks, *h)
		h = nil
		return nil
	}
	for _, line := range lines {
		if h != nil && (oldLeft > 0 || newLeft > 0) {
			if line == "" {
				return ErrMalformed
			}
			kind := ""
			switch line[0] {
			case ' ':
				kind = "context"
				oldNo++
				newNo++
				oldLeft--
				newLeft--
			case '+':
				kind = "add"
				newNo++
				newLeft--
			case '-':
				kind = "delete"
				oldNo++
				oldLeft--
			case '\\':
				h.Lines = append(h.Lines, DiffLine{Kind: "note", Text: "No newline at end of file"})
				continue
			default:
				return ErrMalformed
			}
			if oldLeft < 0 || newLeft < 0 {
				return ErrMalformed
			}
			// Raw text, replaced by display before ParseDiff returns.
			dl := DiffLine{Kind: kind, Text: line[1:]}
			if kind != "add" {
				dl.Old = oldNo
			}
			if kind != "delete" {
				dl.New = newNo
			}
			h.Lines = append(h.Lines, dl)
			continue
		}
		switch {
		case strings.HasPrefix(line, "\\") && h != nil:
			h.Lines = append(h.Lines, DiffLine{Kind: "note", Text: "No newline at end of file"})
		case strings.HasPrefix(line, "@@ "):
			if err := finish(); err != nil {
				return err
			}
			hunk, err := hunkHeader(line)
			if err != nil {
				return err
			}
			// Git's section text is a line from outside the hunk; it is never shown.
			hunk.Section = ""
			h = &hunk
			oldLeft, newLeft = hunk.OldLines, hunk.NewLines
			oldNo, newNo = hunk.OldStart-1, hunk.NewStart-1
			if hunk.OldLines == 0 {
				oldNo = hunk.OldStart
			}
			if hunk.NewLines == 0 {
				newNo = hunk.NewStart
			}
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			d.Binary, d.Notice = true, "Git reports this file as binary; its content is not shown."
			d.Hunks = []GitDiffHunk{}
			return nil
		default:
			known := false
			for _, prefix := range headerPrefixes {
				if strings.HasPrefix(line, prefix) {
					known = true
				}
			}
			if !known || h != nil {
				return ErrMalformed
			}
			if strings.HasPrefix(line, "diff --git ") {
				files++
				if files > 1 {
					return ErrMalformed
				}
			}
		}
	}
	if d.Truncated {
		if h != nil {
			d.Hunks = append(d.Hunks, *h)
		}
		display(d)
		return nil
	}
	if err := finish(); err != nil {
		return err
	}
	display(d)
	return nil
}

// display redacts the parsed full-context hunks and cuts them to displayContext lines
// around each change. Each side (context and deleted lines; context and added lines) is
// analysed whole with imports.SecretLines, so a line inside a secret block is hidden even
// when the block opens far above it. A side that does not start at line 1 or skips lines
// (Git did not give full context) has no known enclosing block: all of it is withheld.
func display(d *GitDiff) {
	type pos struct{ h, l int }
	forced := map[pos]bool{}
	for _, side := range []string{"delete", "add"} {
		var seg []pos
		var texts []string
		flush := func(known bool) {
			marks := imports.SecretLines(texts)
			for i, p := range seg {
				forced[p] = forced[p] || marks[i] || !known
			}
			seg, texts = nil, nil
		}
		known, prev := true, 0
		for hi, h := range d.Hunks {
			for li, l := range h.Lines {
				if l.Kind != "context" && l.Kind != side {
					continue
				}
				n := l.Old
				if side == "add" {
					n = l.New
				}
				if n != prev+1 {
					flush(known)
					known = false
				}
				seg, texts, prev = append(seg, pos{hi, li}), append(texts, l.Text), n
			}
		}
		flush(known)
	}
	parsed := d.Hunks
	d.Hunks, d.Added, d.Removed, d.Redacted = []GitDiffHunk{}, 0, 0, 0
	shown := 0
	for hi, h := range parsed {
		oldR, newR := &LineRedactor{}, &LineRedactor{}
		near := make([]bool, len(h.Lines))
		for li, l := range h.Lines {
			if l.Kind == "add" || l.Kind == "delete" {
				for k := max(0, li-displayContext); k <= min(len(h.Lines)-1, li+displayContext); k++ {
					near[k] = true
				}
			}
		}
		var cur *GitDiffHunk
		lastOld, lastNew := 0, 0
		for li, l := range h.Lines {
			// Both redactors see every line of their side, so heredocs are followed.
			f := forced[pos{hi, li}]
			text, red := l.Text, false
			switch l.Kind {
			case "context":
				o, ored := oldR.line(l.Text, f)
				n, nred := newR.line(l.Text, f)
				text, red = n, nred
				if ored && !nred {
					text, red = o, true
				}
			case "delete":
				text, red = oldR.line(l.Text, f)
			case "add":
				text, red = newR.line(l.Text, f)
			}
			keep := near[li] || l.Kind == "note" && cur != nil
			if keep && shown == maxDiffLines {
				d.Truncated = true
				return
			}
			if keep && cur == nil {
				// Each side starts at its first line in the hunk; a side without lines starts
				// after the line before it, as in Git.
				d.Hunks = append(d.Hunks, GitDiffHunk{OldStart: lastOld, NewStart: lastNew, Lines: []DiffLine{}})
				cur = &d.Hunks[len(d.Hunks)-1]
			}
			if l.Old > 0 {
				lastOld = l.Old
			}
			if l.New > 0 {
				lastNew = l.New
			}
			if !keep {
				cur = nil
				continue
			}
			if l.Old > 0 && cur.OldLines == 0 {
				cur.OldStart = l.Old
			}
			if l.New > 0 && cur.NewLines == 0 {
				cur.NewStart = l.New
			}
			switch l.Kind {
			case "context":
				cur.OldLines++
				cur.NewLines++
			case "delete":
				cur.OldLines++
				d.Removed++
			case "add":
				cur.NewLines++
				d.Added++
			}
			if red {
				d.Redacted++
			}
			l.Text, l.Redacted = text, red
			cur.Lines = append(cur.Lines, l)
			shown++
		}
	}
}

// hunkHeader parses "@@ -a,b +c,d @@ section".
func hunkHeader(line string) (GitDiffHunk, error) {
	rest := strings.TrimPrefix(line, "@@ ")
	ranges, section, ok := strings.Cut(rest, " @@")
	if !ok {
		return GitDiffHunk{}, ErrMalformed
	}
	parts := strings.Split(ranges, " ")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "-") || !strings.HasPrefix(parts[1], "+") {
		return GitDiffHunk{}, ErrMalformed
	}
	os, ol, err1 := rangeOf(parts[0][1:])
	ns, nl, err2 := rangeOf(parts[1][1:])
	if err1 != nil || err2 != nil {
		return GitDiffHunk{}, ErrMalformed
	}
	return GitDiffHunk{OldStart: os, OldLines: ol, NewStart: ns, NewLines: nl, Section: strings.TrimSpace(section), Lines: []DiffLine{}}, nil
}

func rangeOf(s string) (int, int, error) {
	start, count, has := strings.Cut(s, ",")
	a, err := strconv.Atoi(start)
	if err != nil || a < 0 || a > 10_000_000 {
		return 0, 0, ErrMalformed
	}
	if !has {
		return a, 1, nil
	}
	b, err := strconv.Atoi(count)
	if err != nil || b < 0 || b > 10_000_000 {
		return 0, 0, ErrMalformed
	}
	return a, b, nil
}
