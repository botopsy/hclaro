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
// 2026-11-14, evidence p2-2026-11-14/engines-run1.txt), plus the M27.07 platforms below.
var od1 = map[string]Approval{
	"a4ac9d8302cf1b929d468ad44f40fad9e61eb4ea7a5e6c8ad234d7f02d71115f": {"terraform", "1.5.5", "6d61639e2141b7c23a9219c63994f729aa41f91110a1aa08b8a37969fb45e229"},
	"1ed224ce22941b1a7bc4f291cdafff44c039aaa9aaaac836d1f215fac6d21dc5": {"terraform", "1.5.7", "b310ec0e626e9799000cfc8e30247cd827cf7f8030c8e0400257c7f111e93537"},
	"907c58c510e981f70d625d505a2a13d8c3ff6a23a7fefbb3927be5abf47af92f": {"terraform", "1.17.2", "66b472dcc36cf770d83757ed4d002c476174842c05c68637b40a7e99519cf3e1"},
	"aacd317bc57d5abeb9d606ff5fcab2e15bd7a8f8c6a198004476df008c8eda31": {"tofu", "1.6.3", "25b559b6b0cfafaa96ad5d315efaff42186f1fcad09afc6ba4958bc73b4d7ba9"},
	"93c5a8e1d523bae0f33e8d51fcedec364b72eb622eb1b0cec8990a854b0dbad4": {"tofu", "1.13.9", "ac5505dd19d896f327d0ab727d4b853413c694931a7602b31f93c3a3e9d46d00"},
	// M27.07 PLATFORM set: binary hashes printed by fetch-engines.sh in real-engine workflow run
	// 43753465542 (2026-11-15), each extracted from its pinned, signature-checked archive.
	"eeaa5652e0ccb6426360655725d3bafe8bbc2081fb42b2cd531733588039b347": {"terraform", "1.17.2", "da0c7ef278a911347862758e7573a5907365e66485c2637cf5c217c5e227992e"}, // darwin_arm64
	"dbfef0be43ba2117481be510d3b83a2d8cdfa6ae7ec69c5aeb04b3947956ff7e": {"terraform", "1.17.2", "e6f16b2f0b137ee5729caa8c4e79a3bae4ee261acd198a1fcfa8584a6296605b"}, // linux_amd64
	"d54e1202dac657f53fa5982c3481e6f001fafce7ee505dfaf3466d5e934f44e1": {"terraform", "1.17.2", "858e08c181a76b6b223e8c767a8e983922eebe2d062db949a2de66ce040c368c"}, // linux_arm64
	"8d2fb22f4c9008375535daf61fb5fd906355539d8e41d637c68651a0fdd9cf2f": {"terraform", "1.17.2", "2244b7a379ea8c4b224a2eab411b7288f41ef36316fca27ae4b51b1cf2a53024"}, // windows_amd64
	"650a88f830c7e46975eb6aa8e36c0df7ff66e040a3a1914ba195ee722b0b913e": {"tofu", "1.13.9", "29ba2f4c78556a8d25476e5070fe4745495ef649c4a3b827170eb7436d23feb3"},      // darwin_arm64
	"9ce9173bf161f6466ef57dab7edebe8f55089fdf81caa967415323c25d004990": {"tofu", "1.13.9", "bc44eb6b06446ba83bd4072114891987f590f66cca712737a7f1147c50c539ce"},      // linux_amd64
	"aeb19f94f41268f14bbb4771ece083e91ee871a77221b126048166864426d9d3": {"tofu", "1.13.9", "04f7c93828b859d420269ed30f923ffebf09157b5d7fb95bf8d88a4d208b7fa3"},      // linux_arm64
	"7a71d4627b169710267f2b89c9be98e3d03fdf77b55b21714bffd7db1d321117": {"tofu", "1.13.9", "b23a811741911baa829aa7d3d3718626c549e8934546822b0e9b99b9fba75e4e"},      // windows_amd64
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
	downgradeOffline()
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
	host, _ := os.Hostname()
	exe, _ := os.Executable()
	san := NewSanitizer("", filepath.Dir(exe), buildHome, buildRepo, currentUser(), host)
	_ = WriteAborted(out, reason, san.String(err.Error()))
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
	// The combined config for Hub processes with both engines (M27.05).
	if b, err := os.ReadFile(filepath.Join(mirror, "hub.tfrc")); err == nil {
		p := filepath.Join(root, "hub.tfrc")
		if err := os.WriteFile(p, bytes.ReplaceAll(b, []byte("@MIRROR@"), []byte(mirror)), 0o600); err != nil {
			return nil, err
		}
		cli["hub"] = p
	}
	r, err := NewRun(Config{Hclaro: hclaro, Engines: engines, CLIConfig: cli, Out: out, Root: root,
		Approved: od1, Home: buildHome, Repo: buildRepo, Keep: os.Getenv("HCLARO_QUAL_KEEP") == "1"})
	if err != nil {
		return nil, err
	}
	if err := r.recordProviders(mirror); err != nil {
		return nil, err
	}
	if p := filepath.Join(mirror, "hub.tfrc"); cli["hub"] != "" {
		r.hubConfigSha, _ = fileSHA256(p)
	}
	// A second approved binary of an engine, for the M27.05 H6 binary swap only.
	for name, v := range map[string]string{"terraform": "HCLARO_QUAL_TERRAFORM_ALT", "tofu": "HCLARO_QUAL_TOFU_ALT"} {
		if p := os.Getenv(v); p != "" {
			if err := r.SetAlternate(name, p); err != nil {
				return nil, err
			}
		}
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

// needsEnforced lists scenarios whose assertion is offline behaviour (N1). They run when the
// isolated probe before the run passed; if the run as a whole is not enforced (the probe after
// the run failed), a PASS becomes NOT_TESTED.
var needsEnforced = map[string]bool{}

func beforeEnforced() bool {
	i := run.Result.Isolation
	return i.Level == "enforced" || i.Mechanism != "" && i.Mechanism != "proxy" && i.Mechanism != "none" && i.Reason == "no isolated probe after the run"
}

func downgradeOffline() {
	if run.Result.Isolation.Level == "enforced" {
		return
	}
	for i, sc := range run.Result.Scenarios {
		if needsEnforced[sc.ID] && sc.Status == Pass {
			run.Result.Scenarios[i].Status = NotTested
			run.Result.Scenarios[i].Reason = "offline not verified (isolation " + run.Result.Isolation.Level + ": " + run.Result.Isolation.Reason + ")"
		}
	}
}
