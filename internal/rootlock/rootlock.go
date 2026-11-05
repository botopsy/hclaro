// Package rootlock gives one operation at a time exclusive use of a project root, across the
// launch project's plan/apply manager and Hub initialization. Read-only project tools take
// shared claims: they run alongside each other but never alongside an exclusive operation.
package rootlock

import "sync"

// Set holds exclusive claims keyed by canonical root path. Callers canonicalize roots
// (absolute, symlinks resolved) before claiming, so one directory has one key.
type Set struct {
	mu      sync.Mutex
	held    map[string]string
	readers map[string]int
}

func New() *Set { return &Set{held: map[string]string{}, readers: map[string]int{}} }

// Acquire claims root for holder, a short description such as "init". ok is false when the
// root is already claimed, exclusively or shared. release is idempotent.
func (s *Set) Acquire(root, holder string) (release func(), ok bool) {
	if s == nil {
		return func() {}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.held[root]; busy || s.readers[root] > 0 {
		return nil, false
	}
	s.held[root] = holder
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.held, root)
			s.mu.Unlock()
		})
	}, true
}

// AcquireShared claims root for a read-only operation. ok is false while an exclusive claim
// is held. release is idempotent.
func (s *Set) AcquireShared(root string) (release func(), ok bool) {
	if s == nil {
		return func() {}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.held[root]; busy {
		return nil, false
	}
	s.readers[root]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.readers[root]--; s.readers[root] <= 0 {
				delete(s.readers, root)
			}
			s.mu.Unlock()
		})
	}, true
}

// Holder reports who claims root exclusively, if anyone. Shared claims are not reported.
func (s *Set) Holder(root string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[root]
	return h, ok
}

// Readers reports how many shared claims root has.
func (s *Set) Readers(root string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readers[root]
}
