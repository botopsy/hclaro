package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Go test binary is the fake executable: no shell or real IaC engine is involved.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("HCLARO_PLAN_HELPER") == "1" {
		planHelper()
	}
	if os.Getenv("HCLARO_TEST_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args[1:], " ")
	if os.Getenv("HCLARO_TEST_DELAY") == "1" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if os.Getenv("HCLARO_TEST_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "DO_NOT_LEAK_HCLARO_TEST_SECRET")
		os.Exit(1)
	}
	if os.Getenv("TF_LOG_PATH") != "" || os.Getenv("TF_LOG") != "OFF" {
		os.Exit(3)
	}
	switch args {
	case "version -json":
		fmt.Println(`{"terraform_version":"1.9.8"}`)
	case "workspace show":
		fmt.Println("staging")
	case "show -json":
		fmt.Println(`{"format_version":"1.0"}`)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
func TestFakeExecutable(t *testing.T) {
	t.Setenv("HCLARO_TEST_HELPER", "1")
	t.Setenv("TF_LOG_PATH", filepath.Join(t.TempDir(), "never.log"))
	t.Setenv("TF_LOG", "TRACE")
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_version", "TF_CLI_ARGS_workspace", "TF_CLI_ARGS_show"} {
		t.Setenv(key, "")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &CLI{Name: "terraform", Executable: exe, Root: t.TempDir()}
	if v, err := c.Version(context.Background()); err != nil || v != "1.9.8" {
		t.Fatalf("version %s %v", v, err)
	}
	if w, err := c.Workspace(context.Background()); err != nil || w != "staging" {
		t.Fatalf("workspace %s %v", w, err)
	}
	if _, err := c.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_CLI_ARGS_show", "-json-into=never-write.json")
	if _, err := c.State(context.Background()); !errors.Is(err, ErrUnsafeArgs) {
		t.Fatal("output override was accepted")
	}
	t.Setenv("TF_CLI_ARGS_show", "")
	t.Setenv("HCLARO_TEST_FAIL", "1")
	if _, err := c.State(context.Background()); err == nil || strings.Contains(err.Error(), "DO_NOT_LEAK") {
		t.Fatal("unsafe diagnostic")
	}
	t.Setenv("HCLARO_TEST_FAIL", "")
	t.Setenv("HCLARO_TEST_DELAY", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.State(ctx); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("cancellation failed")
	}
}
func TestDetection(t *testing.T) {
	dir := t.TempDir()
	suffix := ""
	if os.PathSeparator == '\\' {
		suffix = ".exe"
	}
	for _, name := range []string{"terraform", "tofu"} {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), []byte("synthetic"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	if c, err := Detect(dir, ""); err != nil || c.Name != "terraform" {
		t.Fatal(c, err)
	}
	if c, err := Detect(dir, "tofu"); err != nil || c.Name != "tofu" {
		t.Fatal(c, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Detect(dir, ""); err == nil {
		t.Fatal("missing CLI accepted")
	}
}
func TestVersionCompatibility(t *testing.T) {
	for _, v := range []string{"0.12.0", "2.0.0", "1.18.0", "1.9.0-beta", "bogus"} {
		if Supported("terraform", v) {
			t.Fatal(v)
		}
	}
	if !Supported("tofu", "1.6.0") || !Supported("terraform", "1.5.0") || Supported("tofu", "1.5.0") {
		t.Fatal("incorrect compatibility")
	}
}

func TestBoundedOutput(t *testing.T) {
	b := &boundedBuffer{max: 8}
	if _, err := b.Write([]byte("safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("excess-output")); err == nil || string(b.b) != "safe" {
		t.Fatal("output limit failed")
	}
}

func TestImportSupport(t *testing.T) {
	for _, c := range []struct {
		name, version string
		want          bool
	}{
		{"terraform", "1.5.0", true}, {"terraform", "1.9.8", true}, {"tofu", "1.6.0", true},
		{"terraform", "1.4.6", false}, {"tofu", "1.5.0", false}, {"terraform", "1.10.0-rc1", false}, {"terraform", "2.0.0", false},
	} {
		if blocks, generate := ImportSupport(c.name, c.version); blocks != c.want || generate != c.want {
			t.Errorf("%s %s: %v %v", c.name, c.version, blocks, generate)
		}
	}
}
