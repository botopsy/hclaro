package qualify

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// readGate reads the gate the way hashicorp/local 2.9.1's data.local_file does (os.ReadFile).
func readGate(path string) chan string {
	out := make(chan string, 1)
	go func() {
		b, err := os.ReadFile(path)
		if err != nil {
			out <- "error: " + err.Error()
			return
		}
		out <- string(b)
	}()
	return out
}

// TestGateHoldsARead: the gate holds a read until Release, then the reader gets "gate\n";
// on Windows (FU-WIN-HOLD) later readers are answered at once, a Release before any reader
// works, and Close returns without a reader. This test never skips: CI is not verbose.
func TestGateHoldsARead(t *testing.T) {
	g, err := newGate(t.TempDir())
	if err != nil {
		t.Fatalf("no gate on %s: %v", runtime.GOOS, err)
	}
	held := readGate(g.Path())
	select {
	case got := <-held:
		t.Fatalf("the read was not held: %q", got)
	case <-time.After(500 * time.Millisecond):
	}
	g.Release()
	select {
	case got := <-held:
		if got != "gate\n" {
			t.Fatalf("read %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read did not finish after the release")
	}
	if runtime.GOOS == "windows" {
		// After the release, every later reader is answered at once.
		for i := 0; i < 3; i++ {
			select {
			case got := <-readGate(g.Path()):
				if got != "gate\n" {
					t.Fatalf("later read %d: %q", i, got)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("later read %d was held after the release", i)
			}
		}
	}
	start := time.Now()
	g.Close()
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Close took %v", time.Since(start))
	}

	if runtime.GOOS == "windows" {
		// Released before any reader; Close with no reader at all.
		g, err := newGate(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		g.Release()
		select {
		case got := <-readGate(g.Path()):
			if got != "gate\n" {
				t.Fatalf("read after an early release: %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a read after an early release was held")
		}
		g.Close()
		idle, err := newGate(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		idle.Close()
		if time.Since(start) > 5*time.Second {
			t.Fatalf("Close without a reader took %v", time.Since(start))
		}
		// The name is free again once closed.
		project := t.TempDir()
		a, err := newGate(project)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newGate(project); err == nil {
			t.Fatal("a second gate on a served project's pipe was created")
		}
		a.Close()
		b, err := newGate(project)
		if err != nil {
			t.Fatalf("the name was not freed by Close: %v", err)
		}
		b.Close()
	}
}

// The hub-hold project's hold.tfvars names the project's gate, HCL-escaped (empty on Unix).
func TestGateInHoldVarFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "p")
	if err := copyTemplate("hub-hold", dst, "CANARY"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "hold.tfvars"))
	if err != nil {
		t.Fatal(err)
	}
	want := `gate  = "` + strings.ReplaceAll(gateName(dst), `\`, `\\`) + `"`
	if !strings.Contains(string(b), want) || strings.Contains(string(b), "@GATE@") {
		t.Fatalf("hold.tfvars:\n%s\nwant %s", b, want)
	}
	if runtime.GOOS == "windows" && (!strings.HasPrefix(gateName(dst), `\\.\pipe\hclaro-qual-`) || strings.ContainsAny(strings.TrimPrefix(gateName(dst), `\\.\pipe\`), `\/:`)) {
		t.Fatalf("pipe name %q", gateName(dst))
	}
}
