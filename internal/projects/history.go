package projects

import "path/filepath"

// Identify names the project at root for the operation history: its Hub ID and alias (or
// folder name) when it is registered, otherwise only its folder name.
func (r *Registry) Identify(root string) (id, name string, registered bool) {
	records, _ := r.List() // on a read error List returns the last good records
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
