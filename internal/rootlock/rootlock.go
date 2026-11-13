// Package rootlock gives one operation at a time exclusive use of a project root, across the
// launch project's plan/apply manager and Hub initialization. Read-only project tools take
// shared claims: they run alongside each other but never alongside an exclusive operation.
package rootlock

import (
	"os"
	"sync"
)

// Set holds claims per project directory. Roots are matched by file identity, so two
// spellings of one folder (case on a case-insensitive file system, a symbolic link) share
// one claim. A root that cannot be stat'ed is matched by its exact path.
type Set struct {
	mu    sync.Mutex
	roots map[string]*claim
}

type claim struct {
	info    os.FileInfo
	holder  string
	readers int
}

func New() *Set { return &Set{roots: map[string]*claim{}} }

// identity stats root outside the lock. os.SameFile on the FileInfo with itself loads the
// identity now; on Windows it is otherwise read from the path at comparison time.
func identity(root string) os.FileInfo {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	os.SameFile(info, info)
	return info
}

// find returns the stored key and claim for root, if any. The caller holds s.mu.
func (s *Set) find(root string, info os.FileInfo) (string, *claim) {
	if c, ok := s.roots[root]; ok {
		return root, c
	}
	if info == nil {
		return "", nil
	}
	for key, c := range s.roots {
		if c.info != nil && os.SameFile(c.info, info) {
			return key, c
		}
	}
	return "", nil
}

// release returns an idempotent func that applies drop to the claim stored under key.
func (s *Set) release(key string, drop func(*claim)) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			c := s.roots[key]
			drop(c)
			if c.holder == "" && c.readers <= 0 {
				delete(s.roots, key)
			}
		})
	}
}

// Acquire claims root for holder, a short description such as "init". ok is false when the
// root is already claimed, exclusively or shared. release is idempotent.
func (s *Set) Acquire(root, holder string) (release func(), ok bool) {
	info := identity(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, c := s.find(root, info)
	if c != nil && (c.holder != "" || c.readers > 0) {
		return nil, false
	}
	if c == nil {
		key, c = root, &claim{info: info}
		s.roots[key] = c
	}
	c.holder = holder
	return s.release(key, func(c *claim) { c.holder = "" }), true
}

// AcquireShared claims root for a read-only operation. ok is false while an exclusive claim
// is held. release is idempotent.
func (s *Set) AcquireShared(root string) (release func(), ok bool) {
	info := identity(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, c := s.find(root, info)
	if c != nil && c.holder != "" {
		return nil, false
	}
	if c == nil {
		key, c = root, &claim{info: info}
		s.roots[key] = c
	}
	c.readers++
	return s.release(key, func(c *claim) { c.readers-- }), true
}

// Holder reports who claims root exclusively, if anyone. Shared claims are not reported.
func (s *Set) Holder(root string) (string, bool) {
	info := identity(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, c := s.find(root, info); c != nil && c.holder != "" {
		return c.holder, true
	}
	return "", false
}

// Readers reports how many shared claims root has.
func (s *Set) Readers(root string) int {
	info := identity(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, c := s.find(root, info); c != nil {
		return c.readers
	}
	return 0
}
