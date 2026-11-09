package gitctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"hclaro/internal/imports"
)

// PlanSourceFingerprint records the source a plan was made from: the Git commit and
// branch (when in a repository) and a SHA-256 over every IaC file in the project root and
// its local module directories, taken before the engine runs. Local modules outside the
// project root are not covered by the context fingerprint; this one covers them.
type PlanSourceFingerprint struct {
	Repository bool   `json:"repository"`
	Commit     string `json:"commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Detached   bool   `json:"detached"`
	// Dirty is set when IaC files in scope were uncommitted at plan time; DirtyFiles counts
	// them. Unknown (DirtyKnown false) when Git could not be run.
	Dirty      bool `json:"dirty"`
	DirtyKnown bool `json:"dirtyKnown"`
	DirtyFiles int  `json:"dirtyFiles"`
	// Content is the SHA-256 of the IaC files in scope; empty when it could not be computed.
	Content      string `json:"contentSha256"`
	Files        int    `json:"files"`
	LocalModules int    `json:"localModules"`
	RecordedAt   string `json:"recordedAt"`
}

// SourceStatus compares a plan's recorded source with the working tree now.
type SourceStatus struct {
	// State is unchanged, changed, unknown or not_recorded.
	State        string                 `json:"state"`
	HeadChanged  bool                   `json:"headChanged"`
	FilesChanged bool                   `json:"filesChanged"`
	Message      string                 `json:"message"`
	Recorded     *PlanSourceFingerprint `json:"recorded,omitempty"`
	Current      *PlanSourceFingerprint `json:"current,omitempty"`
}

const maxSourceBytes = 256 << 20

// Fingerprint computes the source fingerprint of root without running Git: HEAD is read
// from .git files and the content hash from the files in scope.
func (s *Service) Fingerprint(root string) *PlanSourceFingerprint {
	fp := &PlanSourceFingerprint{RecordedAt: s.now().UTC().Format(time.RFC3339)}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fp
	}
	top := FindGit(canonical)
	if head := ReadHead(canonical); head != nil {
		fp.Repository, fp.Commit, fp.Branch, fp.Detached = true, head.Commit, head.Branch, head.Detached
	}
	if top != "" {
		top, _ = filepath.EvalSymlinks(top)
	}
	scope, err := NewScope(canonical, top, s.environ())
	if err != nil {
		return fp
	}
	fp.LocalModules = len(scope.Modules)
	fp.Content, fp.Files = contentHash(scope)
	return fp
}

// Record is Fingerprint plus whether IaC files in scope are uncommitted, from one
// read-only `git status`. It is taken when a plan starts.
func (s *Service) Record(ctx context.Context, root string) *PlanSourceFingerprint {
	fp := s.Fingerprint(root)
	if !fp.Repository {
		return fp
	}
	status := s.Status(ctx, root)
	if status.State == StateRepository && !status.Incomplete {
		fp.DirtyKnown = true
		fp.DirtyFiles = status.Counts.Changed
		fp.Dirty = status.Counts.Changed > 0
	}
	return fp
}

func contentHash(scope *Scope) (string, int) {
	files, err := scope.Files()
	if err != nil {
		return "", 0
	}
	h := sha256.New()
	var total int64
	for _, f := range files {
		h.Write([]byte(f.Rel))
		h.Write([]byte{0})
		real, err := filepath.EvalSymlinks(f.Abs)
		if err != nil {
			return "", 0
		}
		file, err := os.Open(real)
		if err != nil {
			return "", 0
		}
		n, err := io.Copy(h, io.LimitReader(file, maxSourceBytes-total+1))
		file.Close()
		total += n
		if err != nil || total > maxSourceBytes {
			return "", 0
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), len(files)
}

// Compare reports whether the source changed since recorded. A different commit with
// identical files is a HEAD change only; different file contents are a source change.
func (s *Service) Compare(recorded *PlanSourceFingerprint, root string) SourceStatus {
	if recorded == nil {
		return SourceStatus{State: "not_recorded", Message: "No source fingerprint was recorded for this plan."}
	}
	current := s.Fingerprint(root)
	return CompareFingerprints(recorded, current)
}

// CompareFingerprints compares two fingerprints without I/O.
func CompareFingerprints(recorded, current *PlanSourceFingerprint) SourceStatus {
	st := SourceStatus{Recorded: recorded, Current: current}
	if recorded.Content == "" || current.Content == "" {
		st.State = "unknown"
		st.Message = "The IaC files in the project and its local modules could not be fingerprinted (too many or unreadable files)."
		return st
	}
	st.FilesChanged = recorded.Content != current.Content
	st.HeadChanged = recorded.Repository != current.Repository || recorded.Commit != current.Commit
	switch {
	case st.FilesChanged && st.HeadChanged:
		st.State = "changed"
		st.Message = "Plan generated from a different source revision: HEAD moved from " + short(recorded.Commit) + " to " + short(current.Commit) + " and IaC files changed."
	case st.FilesChanged:
		st.State = "changed"
		st.Message = "Plan generated from different source files: IaC files in the project or its local modules changed since the plan."
	case st.HeadChanged:
		st.State = "changed"
		st.Message = "HEAD moved from " + short(recorded.Commit) + " to " + short(current.Commit) + " since the plan; the IaC files in scope are identical."
	default:
		st.State = "unchanged"
		st.Message = "The source matches the plan."
	}
	return st
}

func short(commit string) string {
	switch {
	case commit == "":
		return "no commit"
	case len(commit) > 12:
		return commit[:12]
	}
	return commit
}

// SourceReference is a best-effort location of a resource's configuration block.
type SourceReference struct {
	// State is found, not_found or unsupported.
	State string `json:"state"`
	// Confidence is block (an HCL block header matched) or file (a JSON file declares it).
	Confidence string `json:"confidence,omitempty"`
	FileID     string `json:"fileId,omitempty"`
	// Path is relative to the scope base (repository top level or project root).
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Module    string `json:"module,omitempty"`
	BlockType string `json:"blockType,omitempty"`
	Reason    string `json:"reason"`
}

// Locate finds the configuration block of a resource from its recorded mode, module, type
// and name, using the module manifest for module directories. It reads only IaC files in
// the scope. The mapping is textual and labeled best effort: state and plans are the
// authority on what exists and what changes.
func (s *Service) Locate(root, mode, module, typ, name string) SourceReference {
	ref := SourceReference{State: "not_found"}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		ref.Reason = "The project folder could not be resolved."
		return ref
	}
	top := FindGit(canonical)
	if top != "" {
		top, _ = filepath.EvalSymlinks(top)
	}
	scope, err := NewScope(canonical, top, s.environ())
	if err != nil {
		ref.Reason = "The project folder could not be resolved."
		return ref
	}
	keyword := "resource"
	if mode == "data" {
		keyword = "data"
	}
	ref.BlockType = keyword
	dir := scope.Root
	if module != "" {
		// Parse the module path with a placeholder resource to reuse the address grammar.
		a, err := imports.ParseAddress(module + ".x.y")
		if err != nil || a.Type != "x" {
			ref.State, ref.Reason = "unsupported", "The module address could not be parsed."
			return ref
		}
		ref.Module = a.ModuleKey()
		dir = ""
		for _, m := range scope.Modules {
			if m.Key == ref.Module {
				dir = m.Abs
			}
		}
		if dir == "" {
			ref.State, ref.Reason = "unsupported", "Module "+ref.Module+" is not a local module in this repository (registry, Git or other remote sources are downloaded copies), or the project is not initialized."
			return ref
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		ref.Reason = "The module directory could not be read."
		return ref
	}
	header := regexp.MustCompile(`^[ \t]*` + keyword + `[ \t]+"?` + regexp.QuoteMeta(typ) + `"?[ \t]+"?` + regexp.QuoteMeta(name) + `"?[ \t]*\{`)
	for _, e := range entries {
		kind := Kind(e.Name())
		if e.IsDir() || kind != KindHCL && kind != KindJSON {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		b, _, err := scope.open(abs, 16<<20)
		if err != nil {
			continue
		}
		rel := scope.relOf(abs)
		if kind == KindHCL {
			for i, line := range strings.Split(string(b), "\n") {
				if header.MatchString(line) {
					ref.State, ref.Confidence, ref.FileID, ref.Path, ref.Line = "found", "block", s.ids.of(scope.Base, rel), rel, i+1
					ref.Reason = "Best effort: matched the block header textually."
					return ref
				}
			}
			continue
		}
		if line := jsonDeclares(b, keyword, typ, name); line > 0 {
			ref.State, ref.Confidence, ref.FileID, ref.Path, ref.Line = "found", "file", s.ids.of(scope.Base, rel), rel, line
			ref.Reason = "Best effort: the JSON configuration declares this " + keyword + "; the line is approximate."
			return ref
		}
	}
	ref.Reason = "No matching " + keyword + " block was found in the module's configuration files. It may be generated, renamed, or declared in a way a text search cannot see."
	return ref
}

// jsonDeclares reports the approximate line of a resource in a .tf.json document.
func jsonDeclares(b []byte, keyword, typ, name string) int {
	text := string(b)
	i := strings.Index(text, `"`+keyword+`"`)
	if i < 0 {
		return 0
	}
	j := strings.Index(text[i:], `"`+typ+`"`)
	if j < 0 {
		return 0
	}
	k := strings.Index(text[i+j:], `"`+name+`"`)
	if k < 0 {
		return 0
	}
	return strings.Count(text[:i+j+k], "\n") + 1
}
