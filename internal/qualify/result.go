package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Scenario statuses (tasks/M27/README.md). A scenario starts NOT_TESTED.
const (
	Pass      = "PASS"
	Fail      = "FAIL"
	NotTested = "NOT_TESTED"
	Skipped   = "SKIPPED"
)

// RunResult is the run JSON, schemaVersion 1. M27.08 consumes it; change it only with a new
// schemaVersion.
type RunResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	StartedAt     string `json:"startedAt"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	// Partial is true until the final write after the last scenario.
	Partial bool `json:"partial"`

	HclaroCommit       string `json:"hclaroCommit"`
	HclaroDirty        bool   `json:"hclaroDirty"`
	HclaroVersion      string `json:"hclaroVersion"`
	HclaroBinarySha256 string `json:"hclaroBinarySha256"`

	OS                      string `json:"os"`
	Arch                    string `json:"arch"`
	OSVersion               string `json:"osVersion"`
	RunnerImage             string `json:"runnerImage"`
	RunnerImageVersion      string `json:"runnerImageVersion,omitempty"`
	FilesystemCaseSensitive bool   `json:"filesystemCaseSensitive"`

	Engines              []EngineRecord   `json:"engines"`
	Providers            []ProviderRecord `json:"providers"`
	MirrorManifestSha256 string           `json:"mirrorManifestSha256,omitempty"`

	Isolation                Isolation `json:"isolation"`
	CredentialFilesReachable *bool     `json:"credentialFilesReachable"`
	EnvironmentAllowlist     []string  `json:"environmentAllowlist"`

	// GoTestExit is 1 when any scenario failed or the test binary failed, else 0. It is
	// written by TestMain-equivalent code after m.Run; a partial file has -1.
	GoTestExit int              `json:"goTestExit"`
	Scenarios  []ScenarioResult `json:"scenarios"`
	// Differences are recorded deviations from a reference run (e.g. RE-01), never hidden.
	Differences []string `json:"differences,omitempty"`
}

type EngineRecord struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	BinarySha256  string `json:"binarySha256"`
	ArchiveSha256 string `json:"archiveSha256,omitempty"`
	// Provenance is "verified" only for a binary on the OD-1 list.
	Provenance string `json:"provenance"`
}

type ProviderRecord struct {
	Family        string   `json:"family"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Platforms     []string `json:"platforms"`
	ArchiveSha256 []string `json:"archiveSha256"`
}

type Isolation struct {
	Mechanism string `json:"mechanism"`
	// Level is derived from the probes, never from the mechanism name alone.
	Level  string                     `json:"level"`
	Reason string                     `json:"reason,omitempty"`
	Probes map[string]json.RawMessage `json:"probes,omitempty"`
}

type ScenarioResult struct {
	ID               string   `json:"id"`
	Engine           string   `json:"engine"`
	Mode             string   `json:"mode"`
	HubExecutionFlag bool     `json:"hubExecutionFlag"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason,omitempty"`
	Steps            []Step   `json:"steps"`
	ReviewedPlanPath bool     `json:"reviewedPlanPath,omitempty"`
	DirectEngineCall []string `json:"directEngineCalls"`
	SetupInit        string   `json:"setupInit,omitempty"`
	Canary           string   `json:"canary"`
	Cleanup          Cleanup  `json:"cleanup"`
	Artifacts        []string `json:"artifacts"`
	// Engines records the engine each project used: name, version, sanitised path, SHA-256.
	ProjectEngines []ProjectEngine `json:"projectEngines,omitempty"`
	// Environment additions declared by the scenario (names only).
	ExtraEnvironment []string `json:"extraEnvironment,omitempty"`
	Observations     []string `json:"observations,omitempty"`
}

type ProjectEngine struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Sha256  string `json:"sha256"`
}

type Step struct {
	Route      string `json:"route"`
	HTTPStatus int    `json:"httpStatus"`
	Code       string `json:"code,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

type Cleanup struct {
	TempDirs  string `json:"tempDirs"`
	Processes string `json:"processes"`
	LockFiles string `json:"lockFiles"`
}

// Writer owns the run JSON: a partial file after every scenario and one final atomic write.
type Writer struct {
	mu     sync.Mutex
	dir    string
	result *RunResult
	san    *Sanitizer
}

func NewWriter(dir string, r *RunResult, san *Sanitizer) *Writer {
	return &Writer{dir: dir, result: r, san: san}
}

func (w *Writer) Add(s ScenarioResult) error {
	w.mu.Lock()
	w.result.Scenarios = append(w.result.Scenarios, s)
	w.mu.Unlock()
	return w.write("run.partial.json")
}

// Finish records the exit and writes run.json atomically, then removes the partial file.
func (w *Writer) Finish(goTestExit int, finishedAt string) error {
	w.mu.Lock()
	for _, s := range w.result.Scenarios {
		if s.Status == Fail {
			goTestExit = 1
		}
	}
	w.result.GoTestExit, w.result.FinishedAt, w.result.Partial = goTestExit, finishedAt, false
	w.mu.Unlock()
	if err := w.write("run.json"); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(w.dir, "run.partial.json"))
	return nil
}

func (w *Writer) write(name string) error {
	w.mu.Lock()
	b, err := marshal(w.result)
	w.mu.Unlock()
	if err != nil {
		return err
	}
	b = w.san.Bytes(b)
	tmp, err := os.CreateTemp(w.dir, "."+name+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(w.dir, name))
}

// marshal writes indented JSON without HTML escaping, so <TMP> stays readable.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(v)
	return buf.Bytes(), err
}

// Preflight is the first check of a qualification run: the harness's own environment must
// hold no credential-like variable (`make qualify` starts from env -i).
func Preflight(environ []string) error {
	if hits := CredentialVariables(environ); len(hits) > 0 {
		return fmt.Errorf("environment: credential-like variables %v in the harness environment", hits)
	}
	return nil
}

// WriteAborted writes a run.json for a run stopped before any scenario: one FAIL record with
// the reason, and goTestExit 1, so M27.08 sees a valid failed run rather than nothing.
func WriteAborted(out, reason, detail string) error { return writeStub(out, Fail, reason, detail, 1) }

// WriteNotTested writes a run.json for a run with nothing to test (no engine configured).
func WriteNotTested(out, reason string) error { return writeStub(out, NotTested, reason, "", 0) }

func writeStub(out, status, reason, detail string, exit int) error {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	r := RunResult{SchemaVersion: 1, RunID: uuid(), StartedAt: now(), FinishedAt: now(), GoTestExit: exit,
		OS: runtime.GOOS, Arch: runtime.GOARCH, HclaroCommit: buildCommit, HclaroDirty: buildDirty == "true",
		EnvironmentAllowlist: AllowedNames(), Isolation: Isolation{Mechanism: "none", Level: "none"},
		Engines: []EngineRecord{}, Providers: []ProviderRecord{},
		Scenarios: []ScenarioResult{{ID: "preflight", Status: status, Reason: reason, Steps: []Step{}, DirectEngineCall: []string{}, Artifacts: []string{}, Canary: "not_checked"}}}
	if detail != "" {
		r.Scenarios[0].Observations = []string{detail}
	}
	b, err := marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "run.json"), b, 0o600)
}

// Sanitizer replaces absolute paths, the username, the hostname and the canary before
// anything is written to the result directory.
type Sanitizer struct {
	pairs [][2]string
	// words are replaced only as whole words, so a username like "runner" leaves
	// "runnerImage" alone.
	words []*regexp.Regexp
	names []string
}

// NewSanitizer: longer values are replaced first, so <TMP> inside <HOME> wins.
func NewSanitizer(canary, tmp, home, repo, user, host string) *Sanitizer {
	s := &Sanitizer{}
	add := func(from, to string) {
		if len(from) < 3 {
			return
		}
		s.pairs = append(s.pairs, [2]string{from, to})
		// JSON escapes backslashes in Windows paths.
		if esc := strings.ReplaceAll(from, `\`, `\\`); esc != from {
			s.pairs = append(s.pairs, [2]string{esc, to})
		}
	}
	add(canary, "<CANARY>")
	for _, p := range []string{tmp, repo, home} {
		for _, v := range pathForms(p) {
			add(v, map[string]string{tmp: "<TMP>", repo: "<REPO>", home: "<HOME>"}[p])
		}
	}
	for _, w := range [][2]string{{user, "<USER>"}, {host, "<HOST>"}} {
		if len(w[0]) >= 2 {
			s.words = append(s.words, wordPattern(w[0]))
			s.names = append(s.names, w[1])
		}
	}
	// Longest first so a path is replaced before a username inside it.
	for i := range s.pairs {
		for j := i + 1; j < len(s.pairs); j++ {
			if len(s.pairs[j][0]) > len(s.pairs[i][0]) {
				s.pairs[i], s.pairs[j] = s.pairs[j], s.pairs[i]
			}
		}
	}
	return s
}

// pathForms returns a path as given and with symlinks resolved (macOS /tmp → /private/tmp).
func pathForms(p string) []string {
	if p == "" {
		return nil
	}
	out := []string{p}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
		out = append(out, r)
	}
	return out
}

func (s *Sanitizer) Bytes(b []byte) []byte {
	for _, p := range s.pairs {
		b = bytes.ReplaceAll(b, []byte(p[0]), []byte(p[1]))
	}
	for i, w := range s.words {
		b = w.ReplaceAll(b, []byte("${1}"+s.names[i]+"${2}"))
	}
	return b
}

func wordPattern(v string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(v) + `($|[^A-Za-z0-9_-])`)
}

func (s *Sanitizer) String(v string) string { return string(s.Bytes([]byte(v))) }

// ScanOut fails on any file in dir that holds one of the forbidden substrings (the canary,
// the real home path) or the username as a whole word. It is the last step of a run.
func ScanOut(dir, user string, forbidden ...string) error {
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range forbidden {
			if len(f) >= 3 && bytes.Contains(b, []byte(f)) {
				hits = append(hits, fmt.Sprintf("%s holds a forbidden value", filepath.Base(path)))
			}
		}
		if len(user) >= 2 && wordPattern(user).Match(b) {
			hits = append(hits, fmt.Sprintf("%s holds the username", filepath.Base(path)))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(hits) > 0 {
		return fmt.Errorf("result directory scan: %s", strings.Join(hits, "; "))
	}
	return nil
}
