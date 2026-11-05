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
