// Package qualify is the M27 real-engine harness. It starts the real bin/hclaro against
// engines in temp directories, with an allowlisted environment, and drives it over HTTP as the
// UI does. Only real-engine scenario files carry the `realengine` build tag; the core and the
// fake-engine smoke test are untagged so normal CI compiles and runs them.
package qualify

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/fixtures"
)

// Config is the harness input. Nothing here is passed to Hclaro.
type Config struct {
	// Hclaro is the binary under test.
	Hclaro string
	// Engines maps "terraform" and "tofu" to source binaries (at most one each).
	Engines map[string]string
	// CLIConfig maps an engine name to its TF_CLI_CONFIG_FILE (the mirror config).
	CLIConfig map[string]string
	// Out is the result directory; Root the run's temp root (realpath, outside the repo).
	Out, Root string
	// Approved maps a binary SHA-256 to its OD-1 record; nil skips the check (smoke only).
	Approved map[string]Approval
	// Repo and Home are sanitised in results.
	Repo, Home string
	Keep       bool
}

// Approval is one OD-1 engine binary.
type Approval struct {
	Name, Version, ArchiveSha256 string
}

// Run is one harness run: one result file, one canary.
type Run struct {
	Cfg    Config
	Canary string
	San    *Sanitizer
	W      *Writer
	Result *RunResult
	// engines: name → record for the configured binaries.
	engines map[string]EngineRecord
	user    string
	// negativeDone: the token/Origin/CSRF refusals are checked once per run.
	negativeDone bool
	// hubConfigSha is the SHA-256 of the combined Hub CLI config template (M27.05).
	hubConfigSha string
	// alt holds a second approved binary per engine, used only by the H6 binary swap.
	alt map[string]Alternate
}

// Alternate is a second OD-1 binary of a configured engine (another version).
type Alternate struct {
	Path   string
	Record EngineRecord
}

// SetAlternate checks a second binary of engine name against the OD-1 list; it must report a
// version other than the configured binary's.
func (r *Run) SetAlternate(name, bin string) error {
	main, ok := r.engines[name]
	if !ok {
		return fmt.Errorf("alternate %s: engine not configured", name)
	}
	rec, err := r.checkEngine(name, bin)
	if err != nil {
		return fmt.Errorf("alternate %v", err)
	}
	if rec.Version == main.Version || rec.BinarySha256 == main.BinarySha256 {
		return fmt.Errorf("alternate %s: same version %s as the configured binary", name, rec.Version)
	}
	if r.alt == nil {
		r.alt = map[string]Alternate{}
	}
	r.alt[name] = Alternate{Path: bin, Record: rec}
	return nil
}

// NewRun checks the engines, prepares the result and writes the first partial file.
func NewRun(cfg Config) (*Run, error) {
	if len(cfg.Engines) == 0 {
		return nil, fmt.Errorf("no engine configured")
	}
	for _, d := range []*string{&cfg.Out, &cfg.Root} {
		if err := os.MkdirAll(*d, 0o700); err != nil {
			return nil, err
		}
		if r, err := filepath.EvalSymlinks(*d); err == nil {
			*d = r
		}
	}
	r := &Run{Cfg: cfg, Canary: "HCLARO-QUAL-CANARY-" + randomHex(8), engines: map[string]EngineRecord{}}
	host, _ := os.Hostname()
	r.user = currentUser()
	r.San = NewSanitizer(r.Canary, cfg.Root, cfg.Home, cfg.Repo, r.user, host)
	r.Result = &RunResult{SchemaVersion: 1, RunID: uuid(), StartedAt: now(), Partial: true, GoTestExit: -1,
		OS: runtime.GOOS, Arch: runtime.GOARCH, OSVersion: osVersion(), RunnerImage: "maintainer-machine",
		HclaroCommit: buildCommit, HclaroDirty: buildDirty == "true",
		EnvironmentAllowlist: AllowedNames(), Isolation: Isolation{Mechanism: "none", Level: "none"}}
	if buildRunnerImage != "" {
		r.Result.RunnerImage, r.Result.RunnerImageVersion = buildRunnerImage, buildRunnerImageVersion
	}
	r.Result.FilesystemCaseSensitive = caseSensitive(cfg.Root)
	sum, err := fileSHA256(cfg.Hclaro)
	if err != nil {
		return nil, fmt.Errorf("hclaro binary: %v", err)
	}
	r.Result.HclaroBinarySha256 = sum
	if out, err := exec.Command(cfg.Hclaro, "--version").Output(); err == nil {
		r.Result.HclaroVersion = strings.TrimSpace(string(out))
	}
	names := make([]string, 0, len(cfg.Engines))
	for n := range cfg.Engines {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		rec, err := r.checkEngine(name, cfg.Engines[name])
		if err != nil {
			return nil, err
		}
		r.engines[name] = rec
		r.Result.Engines = append(r.Result.Engines, rec)
	}
	r.W = NewWriter(cfg.Out, r.Result, r.San)
	return r, r.W.write("run.partial.json")
}

// checkEngine refuses a binary that is not on the OD-1 list (when the list is set) and
// records its version from `version -json` run in a clean directory with the allowlist.
func (r *Run) checkEngine(name, bin string) (EngineRecord, error) {
	if name != "terraform" && name != "tofu" {
		return EngineRecord{}, fmt.Errorf("engine %q: must be terraform or tofu", name)
	}
	sum, err := fileSHA256(bin)
	if err != nil {
		return EngineRecord{}, fmt.Errorf("engine %s: %v", name, err)
	}
	rec := EngineRecord{Name: name, BinarySha256: sum, Provenance: "unverified"}
	if r.Cfg.Approved != nil {
		a, ok := r.Cfg.Approved[sum]
		if !ok || a.Name != name {
			return rec, fmt.Errorf("engine %s: binary SHA-256 %s is not on the OD-1 list; refused", name, sum)
		}
		rec.ArchiveSha256, rec.Provenance = a.ArchiveSha256, "verified"
	}
	dir, err := os.MkdirTemp(r.Cfg.Root, "version-")
	if err != nil {
		return rec, err
	}
	defer os.RemoveAll(dir)
	dirs, err := NewProcessDirs(dir)
	if err != nil {
		return rec, err
	}
	if err := copyExecutable(bin, filepath.Join(dirs.Bin, exeName(name))); err != nil {
		return rec, err
	}
	env := Environment(dirs, r.Cfg.CLIConfig[name], nil)
	out, err := direct(filepath.Join(dirs.Bin, exeName(name)), dirs.Home, env, "version -json")
	if err != nil {
		return rec, fmt.Errorf("engine %s: %v", name, err)
	}
	if rec.Version, err = parseVersion(out); err != nil {
		return rec, fmt.Errorf("engine %s: %v", name, err)
	}
	if a, ok := r.Cfg.Approved[sum]; ok && a.Version != rec.Version {
		return rec, fmt.Errorf("engine %s: reports %s, OD-1 lists %s", name, rec.Version, a.Version)
	}
	return rec, nil
}

// Engine returns the record for a configured engine.
func (r *Run) Engine(name string) (EngineRecord, bool) {
	e, ok := r.engines[name]
	return e, ok
}

// Finish writes run.json and scans the result directory. exit is the test binary's result.
// A file holding a forbidden value is deleted and run.json is rewritten with a FAIL record
// that names the file (never the value).
func (r *Run) Finish(exit int) error {
	if err := r.W.Finish(exit, now()); err != nil {
		return err
	}
	hits, err := ScanOut(r.Cfg.Out, r.user, r.Canary, r.Cfg.Home)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return nil
	}
	var names []string
	for _, h := range hits {
		rel, _ := filepath.Rel(r.Cfg.Out, h)
		names = append(names, filepath.ToSlash(rel))
		_ = os.Remove(h)
	}
	reason := "result directory scan: forbidden value (canary, home path or username) in " + strings.Join(names, ", ") + "; file removed"
	if err := r.W.Add(stubScenario("result-scan", Fail, reason)); err != nil {
		return err
	}
	if err := r.W.Finish(1, now()); err != nil {
		return err
	}
	if again, _ := ScanOut(r.Cfg.Out, r.user, r.Canary, r.Cfg.Home); len(again) > 0 {
		// run.json itself leaks: keep only a stub.
		for _, h := range again {
			_ = os.Remove(h)
		}
		_ = WriteAborted(r.Cfg.Out, reason, "")
	}
	return errors.New(reason)
}

// Scenario is one scenario's state; its result starts NOT_TESTED.
type Scenario struct {
	T      *testing.T
	ID     string
	Dir    string
	run    *Run
	mu     sync.Mutex
	result ScenarioResult
	procs  []*Hclaro
	// projects are the directories built for this scenario; files is their content before the
	// run, to detect writes Hclaro should not make.
	projects map[string]map[string]string
	allowed  map[string]bool
	// startCodes are workspace diagnostics a scenario accepts after a read (N1: uninitialized).
	startCodes map[string]bool
	// keepLock and keepPlanDir are set by A7b, which asserts these leftovers itself (the
	// lock-info file is never force-unlocked; the temp dir deletion removes it).
	keepLock, keepPlanDir bool
}

// Scenario runs fn as subtest id and records its result. PASS needs fn to return, then the
// canary and cleanup checks. A panic, timeout or t.Fatal gives FAIL; t.Skip must go through
// NotTested or Skip with a reason.
func (r *Run) Scenario(t *testing.T, id, engine, mode string, hubFlag bool, fn func(s *Scenario)) {
	t.Run(id, func(t *testing.T) {
		dir, err := os.MkdirTemp(r.Cfg.Root, "s-")
		s := &Scenario{T: t, ID: id, Dir: dir, run: r, projects: map[string]map[string]string{}, allowed: map[string]bool{},
			result: ScenarioResult{ID: id, Engine: engine, Mode: mode, HubExecutionFlag: hubFlag, Status: NotTested,
				Steps: []Step{}, DirectEngineCall: []string{}, Artifacts: []string{}, Canary: "not_checked"}}
		completed := false
		defer func() {
			p := recover()
			s.finish(p, completed)
			if err := r.W.Add(s.result); err != nil {
				t.Errorf("result: %v", err)
			}
			if !r.Cfg.Keep || s.result.Status != Fail {
				_ = os.RemoveAll(dir)
			}
		}()
		if err != nil {
			s.Fatalf("scenario directory: %v", err)
		}
		fn(s)
		completed = true
	})
}

func (s *Scenario) finish(p any, completed bool) {
	t := s.T
	stopErrs := s.stopAll()
	switch {
	case p != nil:
		s.setFail(fmt.Sprintf("panic: %v", p))
		t.Errorf("scenario panicked: %v", p)
	case t.Skipped():
		if s.result.Status != NotTested && s.result.Status != Skipped || s.result.Reason == "" {
			s.setFail("skipped without a recorded reason")
			t.Errorf("t.Skip without NotTested/Skip reason")
		}
		// A NOT_TESTED scenario may have run processes: canary and cleanup are still checked.
	case t.Failed() || !completed:
		s.setFail("assertion failed")
	}
	for _, e := range stopErrs {
		s.setFail(e)
		t.Error(e)
	}
	// Canary and cleanup run for every executed scenario, failed or not.
	if where := s.canaryHits(); len(where) > 0 {
		s.result.Canary = "found:" + strings.Join(where, ",")
		s.setFail("canary found in " + strings.Join(where, ", "))
		t.Errorf("canary found in %v", where)
	} else {
		s.result.Canary = "absent"
	}
	if problems := s.cleanupCheck(); len(problems) > 0 {
		s.setFail(strings.Join(problems, "; "))
		t.Errorf("cleanup: %v", problems)
	}
	if s.result.Status == NotTested && completed && !t.Failed() {
		s.result.Status = Pass
	}
}

func (s *Scenario) setFail(reason string) {
	if s.result.Status != Fail {
		s.result.Status, s.result.Reason = Fail, s.run.San.String(reason)
	} else if !strings.Contains(s.result.Reason, reason) {
		s.result.Reason += "; " + s.run.San.String(reason)
	}
}

// Fatalf records the reason and stops the scenario as FAIL.
func (s *Scenario) Fatalf(format string, args ...any) {
	s.T.Helper()
	msg := fmt.Sprintf(format, args...)
	s.setFail(msg)
	s.T.Fatal(s.run.San.String(msg))
}

// Errorf records a failed assertion and lets the scenario continue (it ends as FAIL).
func (s *Scenario) Errorf(format string, args ...any) {
	s.T.Helper()
	msg := fmt.Sprintf(format, args...)
	s.setFail(msg)
	s.T.Error(s.run.San.String(msg))
}

// NotTested stops the scenario as NOT_TESTED with a mandatory reason.
func (s *Scenario) NotTested(reason string) {
	if s.result.Status == Fail {
		// A failed assertion stays FAIL; the later precondition is only noted.
		s.setFail("then not tested: " + reason)
		s.T.FailNow()
	}
	s.result.Status, s.result.Reason = NotTested, reason
	s.T.Skip(reason)
}

// Skip stops the scenario as SKIPPED (does not apply on this platform) with a mandatory reason.
func (s *Scenario) Skip(reason string) {
	s.result.Status, s.result.Reason = Skipped, reason
	s.T.Skip(reason)
}

// Observe adds a sanitised note to the scenario result.
func (s *Scenario) Observe(format string, args ...any) {
	s.result.Observations = append(s.result.Observations, s.run.San.String(fmt.Sprintf(format, args...)))
}

// Result exposes the record for scenario-specific fields.
func (s *Scenario) Result() *ScenarioResult { return &s.result }

func (s *Scenario) step(st Step) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result.Steps = append(s.result.Steps, st)
}

// Direct runs an allowlisted engine subcommand and records its name in the result.
func (s *Scenario) Direct(engine, dir string, env []string, sub string, extra ...string) ([]byte, error) {
	s.mu.Lock()
	s.result.DirectEngineCall = append(s.result.DirectEngineCall, sub)
	s.mu.Unlock()
	out, err := direct(engine, dir, env, sub, extra...)
	if err != nil {
		return out, fmt.Errorf("%s", s.run.San.String(err.Error()))
	}
	return out, nil
}

// Process prepares the directories and environment of one Hclaro process: a bin directory
// with the named engines only (copies of the configured binaries) and the allowlist.
func (s *Scenario) Process(engines ...string) (ProcessDirs, []string, error) {
	dir, err := os.MkdirTemp(s.Dir, "p-")
	if err != nil {
		return ProcessDirs{}, nil, err
	}
	dirs, err := NewProcessDirs(dir)
	if err != nil {
		return dirs, nil, err
	}
	cli := ""
	for _, e := range engines {
		src, ok := s.run.Cfg.Engines[e]
		if !ok {
			return dirs, nil, fmt.Errorf("engine %s is not configured", e)
		}
		if err := copyExecutable(src, filepath.Join(dirs.Bin, exeName(e))); err != nil {
			return dirs, nil, err
		}
		if cli != "" && s.run.Cfg.CLIConfig[e] != cli {
			// Both families in one process: the combined mirror config (scripts/qualify/hub.tfrc,
			// checked by verify-mirror.sh COMBINED=1).
			if cli = s.run.Cfg.CLIConfig["hub"]; cli == "" {
				return dirs, nil, fmt.Errorf("engines %v need the combined CLI config (hub.tfrc)", engines)
			}
			break
		}
		cli = s.run.Cfg.CLIConfig[e]
	}
	return dirs, Environment(dirs, cli, nil), nil
}

// m2616Project is the M26.16 recording project (fixtures/engines/project), also a template.
const m2616Project = "engines/project"

// Project copies fixtures/qualify/<template> (or m2616Project) into the scenario, replacing @CANARY@ with the
// run canary, and applies the fixture guard. allowed lists project-relative paths the
// scenario expects Hclaro or the engine to write (the lock-info file is always checked).
func (s *Scenario) Project(template string, allowed ...string) string {
	return s.ProjectAs(template, template, allowed...)
}

// ProjectAs is Project into projects/<name>, for scenarios with several copies of a template.
func (s *Scenario) ProjectAs(template, name string, allowed ...string) string {
	dst := filepath.Join(s.Dir, "projects", name)
	if err := copyTemplate(template, dst, s.run.Canary); err != nil {
		s.Fatalf("project %s: %v", template, err)
	}
	guard := GuardFixture
	if template == "hub-remote" {
		guard = GuardRemote
		s.Observe("fixture guard exception (H12 only): hub-remote main.tf declares backend \"s3\" with no settings and is never initialized")
	}
	if err := guard(dst); err != nil {
		s.Fatalf("%v", err)
	}
	s.projects[dst] = snapshotFiles(dst)
	for _, a := range allowed {
		s.allowed[filepath.Join(dst, filepath.FromSlash(a))] = true
	}
	return dst
}

// Guard re-applies the fixture guard (before each scenario step that runs an engine).
func (s *Scenario) Guard(project string) {
	if err := GuardFixture(project); err != nil {
		s.Fatalf("%v", err)
	}
}

// AssertEngine fails unless Hclaro reported the expected engine and version for a project,
// and records the binary that served it.
func (s *Scenario) AssertEngine(project, name, gotEngine, gotVersion string) {
	want, ok := s.run.engines[name]
	if !ok {
		s.Fatalf("engine %s is not configured", name)
	}
	rec := ProjectEngine{Project: s.run.San.String(project), Name: name, Version: want.Version, Path: "<TMP>/…/bin/" + exeName(name), Sha256: want.BinarySha256}
	if !slices.Contains(s.result.ProjectEngines, rec) {
		s.result.ProjectEngines = append(s.result.ProjectEngines, rec)
	}
	if gotEngine != name || gotVersion != want.Version {
		s.Fatalf("engine mismatch for %s: Hclaro reported %s %s, expected %s %s", filepath.Base(project), gotEngine, gotVersion, name, want.Version)
	}
}

// Artifact copies a sanitised file into the result directory and records its relative path.
func (s *Scenario) Artifact(name string, data []byte) {
	rel := filepath.ToSlash(filepath.Join("artifacts", s.ID, name))
	p := filepath.Join(s.run.Cfg.Out, filepath.FromSlash(rel))
	if os.MkdirAll(filepath.Dir(p), 0o700) == nil && os.WriteFile(p, s.run.San.Bytes(data), 0o600) == nil {
		s.result.Artifacts = append(s.result.Artifacts, rel)
	}
}

// CheckPlanArtifacts asserts saved-plan directories are private while they exist: 0700
// directories and 0600 files on Unix. On Windows Hclaro relies on the per-user TEMP ACL (it
// sets no ACL of its own), so only presence is recorded there.
func (s *Scenario) CheckPlanArtifacts(h *Hclaro) int {
	entries, _ := os.ReadDir(h.Dirs.Tmp)
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "hclaro-plan-") {
			continue
		}
		n++
		if runtime.GOOS == "windows" {
			continue
		}
		dir := filepath.Join(h.Dirs.Tmp, e.Name())
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			s.Fatalf("saved-plan directory %s is not 0700", e.Name())
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if fi, err := d.Info(); err != nil || fi.Mode().Perm() != 0o600 {
					s.Fatalf("saved-plan file %s is not 0600", d.Name())
				}
			}
			return nil
		})
	}
	return n
}

func (s *Scenario) stopAll() []string {
	var errs []string
	for i, h := range s.procs {
		if h.client != nil && h.client.dialRetries > 0 {
			s.Observe("hclaro %d: %d loopback connect(s) refused with EPERM and retried (F0-LB)", i, h.client.dialRetries)
		}
		code, err := h.Stop(60 * time.Second)
		if err != nil {
			errs = append(errs, fmt.Sprintf("hclaro %d: %v", i, err))
		} else if h.shutdown {
			s.Observe("hclaro %d: exit code %d after the scenario's shutdown", i, code)
		} else if code != 0 {
			errs = append(errs, fmt.Sprintf("hclaro %d exited %d after the interrupt", i, code))
		}
	}
	return errs
}

// canaryHits scans everything Hclaro produced: response bodies and SSE events, stdout,
// stderr, and its config directory (history.json, operation logs, registry). Files the
// engine owns (state, plan files, var files) are not scanned.
func (s *Scenario) canaryHits() []string {
	var where []string
	check := func(label, file string) {
		if b, err := os.ReadFile(file); err == nil && bytes.Contains(b, []byte(s.run.Canary)) {
			where = append(where, label)
		}
	}
	check("responses", filepath.Join(s.Dir, "responses.raw"))
	for i, h := range s.procs {
		check(fmt.Sprintf("hclaro-%d.stdout", i), h.Stdout)
		check(fmt.Sprintf("hclaro-%d.stderr", i), h.Stderr)
		_ = filepath.WalkDir(h.Dirs.Config, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(h.Dirs.Config, p)
				check("config/"+filepath.ToSlash(rel), p)
			}
			return nil
		})
	}
	return where
}

// cleanupCheck runs after Hclaro stopped: no process under the scenario directory, no
// saved-plan or drift directory left in Hclaro's TMPDIR, no lock-info file, and no project
// file written that the scenario did not allow.
func (s *Scenario) cleanupCheck() []string {
	var problems []string
	left, err := leftoverProcesses(s.Dir, nil)
	switch {
	case err != nil:
		s.result.Cleanup.Processes = "unchecked: " + err.Error()
		problems = append(problems, "process list unavailable")
	case len(left) > 0:
		s.result.Cleanup.Processes = "left: " + strings.Join(left, ", ")
		problems = append(problems, "engine or provider processes left: "+strings.Join(left, ", "))
	default:
		s.result.Cleanup.Processes = "none left"
	}
	var dirs []string
	for _, h := range s.procs {
		entries, _ := os.ReadDir(h.Dirs.Tmp)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "hclaro-plan-") || strings.HasPrefix(e.Name(), "hclaro-drift-") {
				dirs = append(dirs, e.Name())
			}
		}
	}
	s.result.Cleanup.TempDirs = "removed"
	if len(dirs) > 0 && s.keepPlanDir {
		s.result.Cleanup.TempDirs = "left (asserted by the scenario): " + strings.Join(dirs, ", ")
	} else if len(dirs) > 0 {
		s.result.Cleanup.TempDirs = "left: " + strings.Join(dirs, ", ")
		problems = append(problems, "saved-plan or drift directories left: "+strings.Join(dirs, ", "))
	}
	var locks, writes []string
	for project, before := range s.projects {
		after := snapshotFiles(project)
		for p, sum := range after {
			if strings.HasSuffix(p, ".terraform.tfstate.lock.info") {
				locks = append(locks, filepath.Base(p))
				continue
			}
			if before[p] != sum && !s.allowed[p] {
				rel, _ := filepath.Rel(project, p)
				writes = append(writes, filepath.ToSlash(rel))
			}
		}
	}
	s.result.Cleanup.LockFiles = "none"
	if len(locks) > 0 && s.keepLock {
		s.result.Cleanup.LockFiles = "left (asserted by the scenario; removed with the temp dir): " + strings.Join(locks, ", ")
	} else if len(locks) > 0 {
		s.result.Cleanup.LockFiles = "left: " + strings.Join(locks, ", ")
		problems = append(problems, "lock-info files left")
	}
	if len(writes) > 0 {
		sort.Strings(writes)
		problems = append(problems, "unexpected project writes: "+strings.Join(writes, ", "))
	}
	return problems
}

// AllowWrite permits a project-relative path to change (set before the operation runs).
func (s *Scenario) AllowWrite(project, rel string) {
	s.allowed[filepath.Join(project, filepath.FromSlash(rel))] = true
}

// RefreshBaseline records the project's current files as the expected state, after setup
// steps the harness performed itself (for example a direct init).
func (s *Scenario) RefreshBaseline(project string) { s.projects[project] = snapshotFiles(project) }

// snapshotFiles hashes every file outside .terraform/ (the engine's data directory).
func snapshotFiles(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out[p], _ = fileSHA256(p)
		}
		return nil
	})
	return out
}

func copyTemplate(name, dst, canary string) error {
	root := path.Join("qualify", name)
	if name == m2616Project {
		root = name
	}
	if _, err := fs.Stat(fixtures.Qualify, root); err != nil {
		return err
	}
	return fs.WalkDir(fixtures.Qualify, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := fs.ReadFile(fixtures.Qualify, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, bytes.ReplaceAll(b, []byte("@CANARY@"), []byte(canary)), 0o600)
	})
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// caseSensitive creates two names differing only in case under dir (F-4).
func caseSensitive(dir string) bool {
	d, err := os.MkdirTemp(dir, "case-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(d)
	if os.WriteFile(filepath.Join(d, "CaseProbe"), nil, 0o600) != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(d, "caseprobe"))
	return err != nil
}

func osVersion() string {
	var out []byte
	switch runtime.GOOS {
	case "darwin":
		a, _ := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
		b, _ := exec.Command("/usr/bin/sw_vers", "-buildVersion").Output()
		out = []byte(strings.TrimSpace(string(a)) + " " + strings.TrimSpace(string(b)))
	case "windows":
		out, _ = exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "ver").Output()
	default:
		out, _ = exec.Command("/bin/uname", "-sr").Output()
	}
	return strings.TrimSpace(string(out))
}

func currentUser() string {
	if buildUser != "" {
		return buildUser
	}
	for _, k := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if out, err := exec.Command("/usr/bin/id", "-un").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// Build-time values: `make qualify` sets them with -ldflags -X, since neither git nor the Go
// toolchain runs inside the isolation.
var (
	buildCommit             = "unknown"
	buildDirty              = "true"
	buildRunnerImage        = ""
	buildRunnerImageVersion = ""
	// buildHome is the real home directory, for sanitising and the final scan: inside the
	// isolation HOME is unset.
	buildHome = ""
	// buildRepo is the repository path, sanitised as <REPO>; buildUser the username (the
	// cleared environment has no USER or USERNAME).
	buildRepo = ""
	buildUser = ""
)
