// Package rootlock gives one operation at a time exclusive use of a project root, across the
// launch project's plan/apply manager and Hub initialization.
package rootlock

import "sync"

// Set holds exclusive claims keyed by canonical root path. Callers canonicalize roots
// (absolute, symlinks resolved) before claiming, so one directory has one key.
type Set struct {
	mu   sync.Mutex
	held map[string]string
}

func New() *Set { return &Set{held: map[string]string{}} }

// Acquire claims root for holder, a short description such as "init". ok is false when the
// root is already claimed. release is idempotent.
func (s *Set) Acquire(root, holder string) (release func(), ok bool) {
	if s == nil {
		return func() {}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.held[root]; busy {
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

// Holder reports who claims root, if anyone.
func (s *Set) Holder(root string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[root]
	return h, ok
}
