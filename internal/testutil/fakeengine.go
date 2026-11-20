// Package testutil holds helpers shared by the tests of several packages. Only tests import it.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	buildMu  sync.Mutex
	builtDir string
)

// Exe is the executable suffix of the current platform.
func Exe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// FakeEngine returns a new directory holding the synthetic engine (scripts/fake-engine) as
// terraform and tofu. The engine is compiled once per test binary and copied for each caller,
// so a test may modify its copies without affecting another test.
func FakeEngine(t testing.TB) string {
	t.Helper()
	dir, err := build()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "engine"+Exe()))
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	for _, name := range []string{"terraform", "tofu"} {
		if err := os.WriteFile(filepath.Join(dir, name+Exe()), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// build compiles the engine on first success. A failure is not kept, so a test that changed
// PATH or the environment does not fail every later caller.
func build() (string, error) {
	buildMu.Lock()
	defer buildMu.Unlock()
	if builtDir != "" {
		return builtDir, nil
	}
	dir, err := os.MkdirTemp("", "hclaro-fake-engine-")
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "engine"+Exe()), "hclaro/scripts/fake-engine")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("build synthetic engine: %v %s", err, out)
	}
	builtDir = dir
	return dir, nil
}

// Cleanup removes the compiled engine. Call it from TestMain after m.Run.
func Cleanup() {
	if builtDir != "" {
		_ = os.RemoveAll(builtDir)
	}
}
