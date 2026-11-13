package projects

import (
	"path/filepath"

	"hclaro/internal/history"
)

// UseHistory records the Hub's initializations, workspace commands and tools in store. Entries
// name projects by their Hub ID and alias; an unregistered root gets an ID derived from its
// path and its folder name. The commit checked out in the root is read from the repository
// files when an operation starts.
func (h *Hub) UseHistory(store *history.Store) {
	h.History = store
	store.Git = history.ReadGit
	store.Project = func(root string) (string, string, bool) {
		records, _ := h.Registry.List()
		for _, p := range records {
			if p.RootPath == root || sameDir(p.RootPath, root) {
				name := p.Alias
				if name == "" {
					name = filepath.Base(p.RootPath)
				}
				return p.ID, name, true
			}
		}
		return "", filepath.Base(root), false
	}
	if h.Inits != nil {
		h.Inits.Recorder = store
	}
}
