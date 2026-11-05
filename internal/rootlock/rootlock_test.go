package rootlock

import (
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
	var nilSet *Set
	if r, ok := nilSet.Acquire("/x", "plan"); !ok {
		t.Fatal("nil set must not block")
	} else {
		r()
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
