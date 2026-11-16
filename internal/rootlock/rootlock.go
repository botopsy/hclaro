// Package rootlock gives one operation at a time exclusive use of a project root, across the
// launch project's plan/apply manager and Hub initialization. Read-only project tools take
// shared claims: they run alongside each other but never alongside an exclusive operation.
// A tree claim (a Git branch switch) holds a whole directory tree: it excludes every claim
// on a root inside it or containing it.
package rootlock

import (
	"os"
	"path/filepath"
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
	// tree is set for a tree claim; ancestors are the identities of the root and every
	// parent folder, used to match claims that contain one another.
	tree      bool
	ancestors []os.FileInfo
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

// ancestors returns the identities of root and each of its parents.
func ancestors(root string) []os.FileInfo {
	var out []os.FileInfo
	for dir := filepath.Clean(root); ; dir = filepath.Dir(dir) {
		if info := identity(dir); info != nil {
			out = append(out, info)
		}
		if filepath.Dir(dir) == dir {
			return out
		}
	}
}

func within(info os.FileInfo, chain []os.FileInfo) bool {
	for _, a := range chain {
		if info != nil && os.SameFile(info, a) {
			return true
		}
	}
	return false
}

// treeHeld reports a held tree claim containing a root with ancestors chain. Caller holds s.mu.
func (s *Set) treeHeld(chain []os.FileInfo) bool {
	for _, c := range s.roots {
		if c.tree && c.holder != "" && within(c.info, chain) {
			return true
		}
	}
	return false
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
	info, chain := identity(root), ancestors(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, c := s.find(root, info)
	if c != nil && (c.holder != "" || c.readers > 0) || s.treeHeld(chain) {
		return nil, false
	}
	if c == nil {
		key, c = root, &claim{info: info}
		s.roots[key] = c
	}
	c.holder = holder
	c.ancestors = chain
	return s.release(key, func(c *claim) { c.holder = "" }), true
}

// AcquireShared claims root for a read-only operation. ok is false while an exclusive claim
// is held. release is idempotent.
func (s *Set) AcquireShared(root string) (release func(), ok bool) {
	info, chain := identity(root), ancestors(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, c := s.find(root, info)
	if c != nil && c.holder != "" || s.treeHeld(chain) {
		return nil, false
	}
	if c == nil {
		key, c = root, &claim{info: info}
		s.roots[key] = c
	}
	c.readers++
	c.ancestors = chain
	return s.release(key, func(c *claim) { c.readers-- }), true
}

// AcquireTree claims the folder tree at top for holder. ok is false while any root inside
// top, top itself or a folder containing top is claimed, exclusively or shared; while it is
// held, every claim on such a root is refused. A top that cannot be stat'ed is refused.
// release is idempotent.
func (s *Set) AcquireTree(top, holder string) (release func(), ok bool) {
	info, chain := identity(top), ancestors(top)
	if info == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.roots {
		if (c.holder != "" || c.readers > 0) && (within(info, c.ancestors) || within(c.info, chain)) {
			return nil, false
		}
	}
	key := "\x00tree:" + top
	if _, taken := s.roots[key]; taken {
		return nil, false
	}
	s.roots[key] = &claim{info: info, holder: holder, tree: true, ancestors: chain}
	return s.release(key, func(c *claim) { c.holder = "" }), true
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
