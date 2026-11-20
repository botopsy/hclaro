package history

// Internals the external tests (package history_test) read.
const (
	FileName     = fileName
	MaxChanges   = maxChanges
	WithheldAddr = withheldAddr
)

var (
	RootID      = rootID
	KindTable   = kindTable
	ScrubAssist = scrubAssist
)

type Document = document

func (s *Store) Path() string { return s.path }

// UseProject replaces the project resolver given to Open.
func (s *Store) UseProject(f func(root string) (id, name string, registered bool)) {
	s.sources.Project = f
}
