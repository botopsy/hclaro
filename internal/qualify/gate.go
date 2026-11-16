package qualify

import "strings"

// A gate holds an engine operation that reads the fixtures' data.local_file.gate (hub-hold
// with hold.tfvars, and the launch-lock F-2 plan holder). On Unix it is the FIFO
// .qual/gate.fifo, which blocks the engine's open; on Windows it is a named pipe the harness
// serves, which blocks the engine's read (FU-WIN-HOLD). Release lets the read finish; Close
// releases and frees everything.
type gate interface {
	Path() string
	Release()
	Close()
}

// gateHCL is the value of the fixtures' gate variable for project, escaped for an HCL string:
// empty on Unix (the fixture's own .qual/gate.fifo), the pipe name on Windows.
func gateHCL(project string) string { return strings.ReplaceAll(gateName(project), `\`, `\\`) }
