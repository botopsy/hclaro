package rootlock

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOneHolderPerRoot(t *testing.T) {
	s := New()
	release, ok := s.Acquire("/infra/dev", "init")
	if !ok {
		t.Fatal("first claim refused")
	}
	if _, ok := s.Acquire("/infra/dev", "plan"); ok {
		t.Fatal("second claim on the same root accepted")
	}
	if other, ok := s.Acquire("/infra/prod", "init"); !ok {
		t.Fatal("another root is independent")
	} else {
		other()
	}
	if h, held := s.Holder("/infra/dev"); !held || h != "init" {
		t.Fatalf("%q %v", h, held)
	}
	release()
	release()
	if _, held := s.Holder("/infra/dev"); held {
		t.Fatal("not released")
	}
}

// spellings returns other paths of dir: a symbolic link and, on a case-insensitive file
// system, the same path in other case.
func spellings(t *testing.T, dir string) []string {
	var out []string
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		out = append(out, link)
	}
	upper := filepath.Join(filepath.Dir(dir), strings.ToUpper(filepath.Base(dir)))
	if _, err := os.Stat(upper); err == nil {
		out = append(out, upper)
	}
	if len(out) == 0 {
		t.Skip("no symbolic links and a case-sensitive file system")
	}
	return out
}

func TestSpellingsOfOneFolderShareAClaim(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "infra")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, other := range spellings(t, dir) {
		s := New()
		release, ok := s.Acquire(dir, "plan")
		if !ok {
			t.Fatal("first claim refused")
		}
		if _, ok := s.Acquire(other, "init"); ok {
			t.Fatalf("%s: exclusive claim accepted under another spelling", other)
		}
		if _, ok := s.AcquireShared(other); ok {
			t.Fatalf("%s: shared claim accepted under another spelling", other)
		}
		if h, held := s.Holder(other); !held || h != "plan" {
			t.Fatalf("%s: holder %q %v", other, h, held)
		}
		release()
		read, ok := s.AcquireShared(other)
		if !ok {
			t.Fatalf("%s: shared claim refused after release", other)
		}
		if _, ok := s.Acquire(dir, "init"); ok {
			t.Fatalf("%s: exclusive claim accepted during a read under another spelling", other)
		}
		if n := s.Readers(dir); n != 1 {
			t.Fatalf("%s: %d readers", other, n)
		}
		read()
		if again, ok := s.Acquire(dir, "init"); !ok {
			t.Fatalf("%s: claim leaked after release through another spelling", other)
		} else {
			again()
		}
		if len(s.roots) != 0 {
			t.Fatalf("%s: %d claims left", other, len(s.roots))
		}
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := s.Acquire("/infra/dev", "init"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners", wins)
	}
}

func TestSharedClaims(t *testing.T) {
	s := New()
	a, ok := s.AcquireShared("/infra/dev")
	if !ok {
		t.Fatal("shared claim refused")
	}
	b, ok := s.AcquireShared("/infra/dev")
	if !ok {
		t.Fatal("shared claims must coexist")
	}
	if _, ok := s.Acquire("/infra/dev", "init"); ok {
		t.Fatal("exclusive claim accepted while shared claims are held")
	}
	if _, held := s.Holder("/infra/dev"); held {
		t.Fatal("shared claims are not exclusive holders")
	}
	if n := s.Readers("/infra/dev"); n != 2 {
		t.Fatalf("%d readers", n)
	}
	a()
	a()
	if n := s.Readers("/infra/dev"); n != 1 {
		t.Fatalf("release must be idempotent: %d readers", n)
	}
	b()
	release, ok := s.Acquire("/infra/dev", "init")
	if !ok {
		t.Fatal("exclusive claim refused after shared claims ended")
	}
	if _, ok := s.AcquireShared("/infra/dev"); ok {
		t.Fatal("shared claim accepted while an exclusive claim is held")
	}
	release()
}
