package operations

import (
	"os"

	"hclaro/internal/workspace"
)

// Fingerprint identifies the plan context: engine, version, workspace, variable file
// selection, configuration files, engine data directory and planning environment.
// It is "" when the configuration could not be read.
func (m *Manager) Fingerprint(snap workspace.Snapshot, varFiles []string) string {
	fp, _ := m.fingerprintExcept(snap, varFiles, "", os.Environ())
	return fp
}
