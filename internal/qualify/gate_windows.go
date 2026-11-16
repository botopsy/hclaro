package qualify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	createNamedPipe  = kernel32.NewProc("CreateNamedPipeW")
	connectNamedPipe = kernel32.NewProc("ConnectNamedPipe")
	flushFileBuffers = kernel32.NewProc("FlushFileBuffers")
)

const (
	pipeAccessOutbound      = 0x00000002
	fileFlagFirstInstance   = 0x00080000
	pipeRejectRemoteClients = 0x00000008 // with PIPE_TYPE_BYTE, PIPE_READMODE_BYTE, PIPE_WAIT (all 0)
	pipeUnlimitedInstances  = 255
	errorPipeConnected      = syscall.Errno(535)
	invalidHandle           = ^uintptr(0)
)

// gateName is the pipe for project: derived from its path, so the hold.tfvars written when
// the project is copied names the same pipe; no path or username is in it.
func gateName(project string) string {
	abs, err := filepath.Abs(project)
	if err != nil {
		abs = project
	}
	// One name per folder however it is spelled: links and 8.3 forms resolved, case folded.
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	return `\\.\pipe\hclaro-qual-` + hex.EncodeToString(sum[:8])
}

// pipeGate serves the named pipe: a listening instance always exists (an open with none
// fails instead of holding); every connection waits for the release, then gets "gate\n".
type pipeGate struct {
	name     string
	released chan struct{}
	release  sync.Once
	closing  chan struct{}
	close    sync.Once
	serving  chan struct{}
	handlers sync.WaitGroup
}

func newGate(project string) (gate, error) {
	g := &pipeGate{name: gateName(project), released: make(chan struct{}), closing: make(chan struct{}), serving: make(chan struct{})}
	// The first instance must be new: an earlier gate on the same project still serving
	// would otherwise share connections with this one.
	h, err := g.instance(fileFlagFirstInstance)
	if err != nil {
		return nil, err
	}
	go g.serve(h)
	return g, nil
}

func (g *pipeGate) Path() string { return g.name }

func (g *pipeGate) instance(flags uint32) (syscall.Handle, error) {
	name, err := syscall.UTF16PtrFromString(g.name)
	if err != nil {
		return 0, err
	}
	h, _, callErr := createNamedPipe.Call(uintptr(unsafe.Pointer(name)), uintptr(pipeAccessOutbound|flags), pipeRejectRemoteClients, pipeUnlimitedInstances, 4096, 4096, 0, 0)
	if h == invalidHandle {
		return 0, fmt.Errorf("create named pipe: %v", callErr)
	}
	return syscall.Handle(h), nil
}

// serve accepts connections until Close. The next instance is created before a connection is
// handed off, so a reader never finds the pipe without a listener.
func (g *pipeGate) serve(h syscall.Handle) {
	defer close(g.serving)
	for {
		ok, _, callErr := connectNamedPipe.Call(uintptr(h), 0)
		if ok == 0 && !errors.Is(callErr, errorPipeConnected) {
			syscall.CloseHandle(h)
			return
		}
		select {
		case <-g.closing:
			// The connection is Close's own wake-up.
			syscall.CloseHandle(h)
			return
		default:
		}
		next, err := g.instance(0)
		g.handlers.Add(1)
		go g.answer(h)
		if err != nil {
			return
		}
		h = next
	}
}

// answer waits for the release, writes the gate line and lets the reader finish.
func (g *pipeGate) answer(h syscall.Handle) {
	defer g.handlers.Done()
	defer syscall.CloseHandle(h)
	<-g.released
	var n uint32
	_ = syscall.WriteFile(h, []byte("gate\n"), &n, nil)
	// Flush waits until the reader has the line; closing (not DisconnectNamedPipe, which the
	// reader sees as ERROR_PIPE_NOT_CONNECTED) then ends its read with ERROR_BROKEN_PIPE,
	// which Go's os.ReadFile, like hashicorp/local's data source, treats as end of file.
	flushFileBuffers.Call(uintptr(h))
}

func (g *pipeGate) Release() { g.release.Do(func() { close(g.released) }) }

// Close releases every reader, wakes the pending accept with a connection of its own and
// waits (bounded) for the server and every handler.
func (g *pipeGate) Close() {
	g.close.Do(func() {
		g.Release()
		close(g.closing)
		// The listening instance may be taken for a moment by a reader the server is handing
		// off; try again until the accept wakes.
		for i := 0; i < 100; i++ {
			if f, err := os.Open(g.name); err == nil {
				f.Close()
				break
			}
			select {
			case <-g.serving:
				i = 100
			case <-time.After(20 * time.Millisecond):
			}
		}
		done := make(chan struct{})
		go func() { <-g.serving; g.handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
	})
}
