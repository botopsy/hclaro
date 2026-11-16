//go:build !windows

package qualify

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// gateName is empty on Unix: the fixtures read their own .qual/gate.fifo.
func gateName(string) string { return "" }

// fifoGate is the Unix gate: the engine blocks opening the FIFO until the harness writes it.
type fifoGate struct {
	path  string
	once  sync.Once
	wrote chan struct{}
}

func newGate(project string) (gate, error) {
	p := filepath.Join(project, ".qual", "gate.fifo")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(p) // an earlier gate's pipe
	if err := mkfifo(p); err != nil {
		return nil, err
	}
	return &fifoGate{path: p, wrote: make(chan struct{})}, nil
}

func (g *fifoGate) Path() string { return g.path }

// Release writes the pipe once, so the engine's read ends.
func (g *fifoGate) Release() {
	g.once.Do(func() {
		go func() {
			if f, err := os.OpenFile(g.path, os.O_WRONLY, 0); err == nil {
				_, _ = f.WriteString("gate\n")
				f.Close()
			}
			close(g.wrote)
		}()
	})
}

// Close releases the pipe and removes it (the project file snapshot would block on it). A
// writer still waiting because no engine opened the pipe is unblocked by a reader.
func (g *fifoGate) Close() {
	g.Release()
	select {
	case <-g.wrote:
	case <-time.After(30 * time.Second):
		if f, err := os.OpenFile(g.path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			<-g.wrote
			f.Close()
		}
	}
	_ = os.Remove(g.path)
}
