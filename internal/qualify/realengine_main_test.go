//go:build realengine

package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// Real-engine entry. `make qualify` (scripts/qualify/qualify.sh) compiles this test binary
// outside the isolation, copies it with hclaro, netprobe, the engines and the mirror into a
// run directory outside the home directory, and starts it with `env -i` inside the isolation.
// Never set the realengine tag in PR or push CI.

func init() { realEngineBuild = true }

// od1 is the OD-1 approved engine binary list (darwin_amd64, fetch-engines.sh output of
// 2026-11-14, evidence p2-2026-11-14/engines-run1.txt). Other platforms need their own approval.
var od1 = map[string]Approval{
	"a4ac9d8302cf1b929d468ad44f40fad9e61eb4ea7a5e6c8ad234d7f02d71115f": {"terraform", "1.5.5", "6d61639e2141b7c23a9219c63994f729aa41f91110a1aa08b8a37969fb45e229"},
	"1ed224ce22941b1a7bc4f291cdafff44c039aaa9aaaac836d1f215fac6d21dc5": {"terraform", "1.5.7", "b310ec0e626e9799000cfc8e30247cd827cf7f8030c8e0400257c7f111e93537"},
	"907c58c510e981f70d625d505a2a13d8c3ff6a23a7fefbb3927be5abf47af92f": {"terraform", "1.17.2", "66b472dcc36cf770d83757ed4d002c476174842c05c68637b40a7e99519cf3e1"},
	"aacd317bc57d5abeb9d606ff5fcab2e15bd7a8f8c6a198004476df008c8eda31": {"tofu", "1.6.3", "25b559b6b0cfafaa96ad5d315efaff42186f1fcad09afc6ba4958bc73b4d7ba9"},
	"93c5a8e1d523bae0f33e8d51fcedec364b72eb622eb1b0cec8990a854b0dbad4": {"tofu", "1.13.9", "ac5505dd19d896f327d0ab727d4b853413c694931a7602b31f93c3a3e9d46d00"},
}

// run is the real-engine run, nil when no engine is configured.
var run *Run

// runSkip is the reason no run exists (recorded as NOT_TESTED).
var runSkip string

func TestMain(m *testing.M) {
	out := os.Getenv("HCLARO_QUAL_OUT")
	if out == "" {
		fmt.Fprintln(os.Stderr, "HCLARO_QUAL_OUT is required")
		os.Exit(2)
	}
	if err := Preflight(os.Environ()); err != nil {
		abort(out, "environment", err)
	}
	var err error
	run, err = newRealRun(out)
	if err != nil {
		abort(out, "setup", err)
	}
	if run == nil {
		// No engine: a valid run, NOT_TESTED with the reason; scenarios skip.
		if err := WriteNotTested(out, runSkip); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(m.Run())
	}
	code := m.Run()
	run.isolationAfter()
	if err := run.Finish(code); err != nil {
		fmt.Fprintln(os.Stderr, "finish:", err)
		code = 1
	}
	if run.Result.GoTestExit != 0 {
		code = 1
	}
	os.Exit(code)
}

func abort(out, reason string, err error) {
	fmt.Fprintf(os.Stderr, "qualify: %s: %v\n", reason, err)
	_ = WriteAborted(out, reason, err.Error())
	os.Exit(1)
}

func newRealRun(out string) (*Run, error) {
	engines := map[string]string{}
	for name, v := range map[string]string{"terraform": "HCLARO_QUAL_TERRAFORM", "tofu": "HCLARO_QUAL_TOFU"} {
		if p := os.Getenv(v); p != "" {
			if !filepath.IsAbs(p) {
				return nil, fmt.Errorf("%s must be absolute", v)
			}
			engines[name] = p
		}
	}
	if len(engines) == 0 {
		runSkip = "no engine configured"
		return nil, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	runDir := filepath.Dir(exe)
	if runDir, err = filepath.EvalSymlinks(runDir); err != nil {
		return nil, err
	}
	hclaro := os.Getenv("HCLARO_BIN")
	if hclaro == "" {
		hclaro = filepath.Join(runDir, exeName("hclaro"))
	}
	mirror := os.Getenv("HCLARO_QUAL_MIRROR")
	if mirror == "" {
		return nil, fmt.Errorf("HCLARO_QUAL_MIRROR is required")
	}
	// The harness root lives in the run dir: the isolation allows provider Unix sockets only
	// under it (sandbox.sb RUN_TMP).
	root, err := os.MkdirTemp(runDir, "h-")
	if err != nil {
		return nil, err
	}
	cli := map[string]string{}
	for name := range engines {
		family := name
		tfrc, err := os.ReadFile(filepath.Join(mirror, family+".tfrc"))
		if err != nil {
			return nil, err
		}
		p := filepath.Join(root, family+".tfrc")
		if err := os.WriteFile(p, bytes.ReplaceAll(tfrc, []byte("@MIRROR@"), []byte(mirror)), 0o600); err != nil {
			return nil, err
		}
		cli[name] = p
	}
	r, err := NewRun(Config{Hclaro: hclaro, Engines: engines, CLIConfig: cli, Out: out, Root: root,
		Approved: od1, Home: buildHome, Repo: buildRepo, Keep: os.Getenv("HCLARO_QUAL_KEEP") == "1"})
	if err != nil {
		return nil, err
	}
	if err := r.recordProviders(mirror); err != nil {
		return nil, err
	}
	r.isolationBefore(runDir)
	return r, nil
}

// recordProviders re-hashes every mirrored archive against mirror-manifest.json and records
// the providers.
func (r *Run) recordProviders(mirror string) error {
	manifest := filepath.Join(mirror, "mirror-manifest.json")
	b, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var m struct {
		Entries []struct {
			Archive, Family, Platform, Provider, Sha256, Version string
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	r.Result.MirrorManifestSha256, _ = fileSHA256(manifest)
	byKey := map[string]*ProviderRecord{}
	for _, e := range m.Entries {
		got, err := fileSHA256(filepath.Join(mirror, e.Family, filepath.FromSlash(e.Archive)))
		if err != nil || got != e.Sha256 {
			return fmt.Errorf("mirror archive %s does not match the manifest", e.Archive)
		}
		k := e.Family + " " + e.Provider + " " + e.Version
		if byKey[k] == nil {
			byKey[k] = &ProviderRecord{Family: e.Family, Name: e.Provider, Version: e.Version}
		}
		byKey[k].Platforms = append(byKey[k].Platforms, e.Platform)
		byKey[k].ArchiveSha256 = append(byKey[k].ArchiveSha256, e.Sha256)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.Result.Providers = append(r.Result.Providers, *byKey[k])
	}
	return nil
}

var probeBin, probeControl, probeBefore string

// isolationBefore runs the isolated probe inside the isolation (this process is inside it)
// and reads the control that qualify.sh ran outside it.
func (r *Run) isolationBefore(runDir string) {
	r.Result.Isolation.Mechanism = os.Getenv("HCLARO_QUAL_ISOLATION")
	r.Result.Isolation.Probes = map[string]json.RawMessage{}
	probeBin = filepath.Join(runDir, exeName("netprobe"))
	if b, err := os.ReadFile(filepath.Join(runDir, "f0-control.json")); err == nil {
		probeControl = string(b)
		r.Result.Isolation.Probes["control"] = b
	}
	if b := r.isolatedProbe(); b != nil {
		probeBefore = string(b)
		r.Result.Isolation.Probes["isolatedBefore"] = b
	}
	r.Result.Isolation.Level, r.Result.Isolation.Reason, r.Result.CredentialFilesReachable =
		IsolationLevel(r.Result.Isolation.Mechanism, []byte(probeControl), []byte(probeBefore), nil)
}

func (r *Run) isolationAfter() {
	after := r.isolatedProbe()
	if after != nil {
		r.Result.Isolation.Probes["isolatedAfter"] = after
	}
	r.Result.Isolation.Level, r.Result.Isolation.Reason, r.Result.CredentialFilesReachable =
		IsolationLevel(r.Result.Isolation.Mechanism, []byte(probeControl), []byte(probeBefore), after)
}

func (r *Run) isolatedProbe() []byte {
	if _, err := os.Stat(probeBin); err != nil {
		return nil
	}
	args := []string{"isolated"}
	if buildHome != "" {
		args = append(args, "-read", filepath.Join(buildHome, "hclaro-qualify", "home-read-canary"))
	}
	out, err := exec.Command(probeBin, args...).Output()
	if err != nil || !json.Valid(out) {
		return nil
	}
	return bytes.TrimSpace(out)
}
