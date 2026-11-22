package proc

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrBatchFile refuses a Windows .bat or .cmd executable. Windows runs batch files through
// cmd.exe, which re-parses the command line Go quotes for CommandLineToArgvW, so argv text
// such as a state-derived resource address could start another command.
var ErrBatchFile = errors.New("a Windows batch file (.bat or .cmd) cannot be run safely; install the .exe")

// LookPath is exec.LookPath for runners whose argv can carry lower-trust text: it refuses a
// Windows batch file instead of returning it.
func LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	if batchFile(runtime.GOOS, path) {
		return "", ErrBatchFile
	}
	return path, nil
}

// Windows ignores trailing dots and spaces in a file name, so they cannot hide the extension.
func batchFile(goos, path string) bool {
	if goos != "windows" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimRight(path, ". ")))
	return ext == ".bat" || ext == ".cmd"
}
