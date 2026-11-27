// Package scannerstest writes fake tflint, Trivy and Checkov executables for tests. They are
// POSIX shell scripts, so tests that use them skip on Windows. They never touch a network.
package scannerstest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Versions the fakes print for --version.
var Versions = map[string]string{
	"tflint":  "TFLint version 0.50.3\n+ ruleset.terraform (0.5.0-bundled)\n",
	"trivy":   "Version: 0.58.1\n",
	"checkov": "3.2.100\n",
}

// Fake is one fake scanner. Dir/Args/Env/Cwd files record the last scan call.
type Fake struct {
	Path                 string
	ArgsFile, EnvFile    string
	CwdFile, StartedFile string
}

// Install writes a fake `name` in a new directory. A run prints stdout and stderr and exits
// with code; --version prints version (Versions[name] when empty). hang makes a run sleep
// until it is interrupted.
func Install(t testing.TB, name, version, stdout, stderr string, code int, hang bool) *Fake {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake scanners are shell scripts")
	}
	if version == "" {
		version = Versions[name]
	}
	dir := t.TempDir()
	f := &Fake{Path: filepath.Join(dir, name), ArgsFile: filepath.Join(dir, "args"), EnvFile: filepath.Join(dir, "env"), CwdFile: filepath.Join(dir, "cwd"), StartedFile: filepath.Join(dir, "started")}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then printf %%s %s; exit 0; fi
printf '%%s\n' "$@" > %s
env > %s
pwd > %s
: > %s
`, q(version), q(f.ArgsFile), q(f.EnvFile), q(f.CwdFile), q(f.StartedFile))
	if hang {
		script += "trap 'exit 130' INT TERM\nwhile :; do sleep 0.1; done\n"
	}
	script += fmt.Sprintf("printf %%s %s\nprintf %%s %s >&2\nexit %d\n", q(stdout), q(stderr), code)
	if err := os.WriteFile(f.Path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// Args returns the argv of the last scan call, one per element.
func (f *Fake) Args() []string {
	b, err := os.ReadFile(f.ArgsFile)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// Env returns the environment of the last scan call.
func (f *Fake) Env() string {
	b, _ := os.ReadFile(f.EnvFile)
	return string(b)
}

// Ran reports whether a scan call (not --version) happened.
func (f *Fake) Ran() bool {
	_, err := os.Stat(f.StartedFile)
	return err == nil
}

// Cwd returns the directory of the last scan call.
func (f *Fake) Cwd() string {
	b, _ := os.ReadFile(f.CwdFile)
	return strings.TrimSpace(string(b))
}
