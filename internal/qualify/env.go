package qualify

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// credentialName matches variable names that must never reach a qualification run
// (M27.02 §Environment, fail fast).
var credentialName = regexp.MustCompile(`^(AWS_|ARM_|AZURE_|GOOGLE_|GCLOUD_|CLOUDSDK_|TF_TOKEN_|TFE_|VAULT_|DIGITALOCEAN_|HCLOUD_|OCI_|ALICLOUD_)|(?i)(TOKEN|SECRET|PASSWORD|CREDENTIAL)`)

// CredentialVariables returns the names in environ that look like credentials. `make qualify`
// starts from `env -i`, so any hit means the harness was started outside the wrapper.
func CredentialVariables(environ []string) []string {
	var hits []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if credentialName.MatchString(name) {
			hits = append(hits, name)
		}
	}
	sort.Strings(hits)
	return hits
}

// ProcessDirs are the per-Hclaro-process directories under one scenario directory.
type ProcessDirs struct {
	Bin, Home, Tmp, Config string
}

// NewProcessDirs creates bin, home, tmp and config under dir.
func NewProcessDirs(dir string) (ProcessDirs, error) {
	d := ProcessDirs{Bin: filepath.Join(dir, "bin"), Home: filepath.Join(dir, "home"), Tmp: filepath.Join(dir, "tmp"), Config: filepath.Join(dir, "config")}
	for _, p := range []string{d.Bin, d.Home, d.Tmp, d.Config} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return d, err
		}
	}
	return d, nil
}

const proxyBlackhole = "http://127.0.0.1:9"

// Environment builds the child environment from the allowlist only. cliConfig is the
// TF_CLI_CONFIG_FILE for the project's engine family; extra holds the scenario's declared
// additions (recorded in its result).
func Environment(d ProcessDirs, cliConfig string, extra map[string]string) []string {
	env := map[string]string{
		"HCLARO_CONFIG_DIR":  d.Config,
		"TF_CLI_CONFIG_FILE": cliConfig,
		"CHECKPOINT_DISABLE": "1",
		"LANG":               "C.UTF-8",
		"LC_ALL":             "C.UTF-8",
		"TZ":                 "UTC",
		"HTTPS_PROXY":        proxyBlackhole,
		"HTTP_PROXY":         proxyBlackhole,
	}
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		env["PATH"] = strings.Join([]string{d.Bin, filepath.Join(root, "System32"), root, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0")}, ";")
		env["USERPROFILE"] = d.Home
		env["APPDATA"] = filepath.Join(d.Home, "AppData", "Roaming")
		env["LOCALAPPDATA"] = filepath.Join(d.Home, "AppData", "Local")
		env["TMP"], env["TEMP"] = d.Tmp, d.Tmp
		env["SystemRoot"] = root
		env["ComSpec"] = os.Getenv("ComSpec")
		env["PATHEXT"] = os.Getenv("PATHEXT")
		env["HOME"] = d.Home
	} else {
		env["PATH"] = d.Bin + ":/usr/bin:/bin"
		env["HOME"] = d.Home
		env["XDG_CONFIG_HOME"] = filepath.Join(d.Home, ".config")
		env["XDG_CACHE_HOME"] = filepath.Join(d.Home, ".cache")
		env["TMPDIR"] = d.Tmp
	}
	for k, v := range extra {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// AllowedNames is the allowlist for this platform (names only, as recorded in the run JSON).
func AllowedNames() []string {
	names := []string{"PATH", "HOME", "HCLARO_CONFIG_DIR", "TF_CLI_CONFIG_FILE", "CHECKPOINT_DISABLE", "LANG", "LC_ALL", "TZ", "HTTPS_PROXY", "HTTP_PROXY"}
	if runtime.GOOS == "windows" {
		names = append(names, "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TMP", "TEMP", "SystemRoot", "ComSpec", "PATHEXT")
	} else {
		names = append(names, "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMPDIR")
	}
	sort.Strings(names)
	return names
}

// CheckEnvironment fails unless env holds exactly the allowlist plus the declared extras,
// with no credential-like name among them.
func CheckEnvironment(env []string, extra map[string]string) error {
	want := map[string]bool{}
	for _, n := range AllowedNames() {
		want[n] = true
	}
	for n := range extra {
		want[n] = true
	}
	got := map[string]bool{}
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || got[name] {
			return fmt.Errorf("environment: malformed or duplicate entry %q", name)
		}
		if !want[name] {
			return fmt.Errorf("environment: %s is not on the allowlist", name)
		}
		got[name] = true
	}
	for n := range want {
		if !got[n] {
			return fmt.Errorf("environment: %s is missing", n)
		}
	}
	if hits := CredentialVariables(env); len(hits) > 0 {
		return fmt.Errorf("environment: credential-like variables %v", hits)
	}
	return nil
}
