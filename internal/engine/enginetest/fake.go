// Package enginetest provides a scripted engine for tests and synthetic fixtures.
package enginetest

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"hclaro/internal/engine"
)

// Fake implements engine.Adapter and engine.Planner for tests and synthetic helpers. It lives
// outside package engine so the hclaro binary never links it.
type Fake struct {
	mu             sync.Mutex
	VersionValue   string
	WorkspaceValue string
	StateJSON      []byte
	Lines          [][]byte
	PlanJSON       []byte
	PlanErr        error
	// WriteOnError writes PlanJSON even when PlanErr is returned, like an engine that saves an
	// errored plan.
	WriteOnError bool
	ShowErr      error
	ApplyLines   [][]byte
	ApplyErr     error
	// Root and GeneratedConfig emulate -generate-config-out: the content is written to the
	// requested file name inside Root.
	Root            string
	GeneratedConfig []byte
	// Block makes Plan wait for cancellation, to exercise cancel paths.
	Block bool
	// BlockApply makes Apply wait for cancellation, then take ApplyStopDelay to stop and
	// return engine.ErrCancelled, or nil with ApplyFinishesAfterCancel.
	BlockApply               bool
	ApplyStopDelay           time.Duration
	ApplyFinishesAfterCancel bool
	Delay                    time.Duration
	// InitLines and InitStderr are emitted by Init on stdout and stderr; InitErr is its
	// result, BlockInit makes it wait for cancellation and InitDataDir is created on success.
	InitLines   [][]byte
	InitStderr  [][]byte
	InitErr     error
	BlockInit   bool
	InitDataDir string
	// Workspaces, CLIDefault and WorkspaceStates back the WorkspaceManager methods.
	// WorkspaceErr fails ChangeWorkspace, BlockWorkspace makes it wait for cancellation,
	// ListErr fails listings and ListErrAfter sets it after a successful change.
	Workspaces      []string
	CLIDefault      string
	WorkspaceStates map[string][]byte
	WorkspaceLines  [][]byte
	WorkspaceErr    error
	BlockWorkspace  bool
	ListErr         error
	ListErrAfter    bool
	StateErr        error
	// LockedReads makes that many State calls fail with engine.ErrReadStateLocked, as a Windows
	// local-backend read does while another process holds the lock (FU-WIN-STATE-READ).
	LockedReads int
	// ValidateOut, ValidateHumanOut, FormatOut, ProvidersOut and OutputsOut are the captured
	// output of the project utilities; ToolErr fails them and BlockTool makes them wait for
	// cancellation.
	ValidateOut      engine.Capture
	ValidateHumanOut engine.Capture
	FormatOut        engine.Capture
	ProvidersOut     engine.Capture
	OutputsOut       engine.Capture
	ToolErr          error
	BlockTool        bool
	// GraphDOT is the `graph` output; GraphErr fails it and BlockGraph makes it wait for
	// cancellation.
	GraphDOT      []byte
	GraphErr      error
	BlockGraph    bool
	calls         []string
	requests      []engine.PlanRequest
	applied       [][]byte
	applyRequests []engine.ApplyRequest
	inits         [][]string
}

func (m *Fake) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

// SetLockedReads sets LockedReads while other goroutines may read state.
func (m *Fake) SetLockedReads(n int) { m.mu.Lock(); m.LockedReads = n; m.mu.Unlock() }

// Calls returns the engine methods invoked so far.
func (m *Fake) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// Requests returns the validated plan requests received so far.
func (m *Fake) Requests() []engine.PlanRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]engine.PlanRequest(nil), m.requests...)
}

// ApplyRequests returns the validated apply requests received so far.
func (m *Fake) ApplyRequests() []engine.ApplyRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]engine.ApplyRequest(nil), m.applyRequests...)
}

// Applied returns the saved plan contents passed to each Apply call.
func (m *Fake) Applied() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.applied...)
}

// SetWorkspace changes the workspace reported from now on.
func (m *Fake) SetWorkspace(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WorkspaceValue = name
}

func (m *Fake) Version(context.Context) (string, error) {
	m.record("version")
	return m.VersionValue, nil
}
func (m *Fake) State(context.Context) ([]byte, error) {
	m.record("show")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.LockedReads > 0 {
		m.LockedReads--
		return nil, engine.ErrReadStateLocked
	}
	return m.StateJSON, nil
}
func (m *Fake) Workspace(context.Context) (string, error) {
	m.record("workspace")
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.WorkspaceValue, nil
}
func (m *Fake) Plan(ctx context.Context, request engine.PlanRequest, line func([]byte)) error {
	m.record("plan")
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if _, err := engine.PlanArgs(request); err != nil {
		return err
	}
	for _, l := range m.Lines {
		line(l)
		if m.Delay > 0 {
			select {
			case <-time.After(m.Delay):
			case <-ctx.Done():
				return engine.ErrCancelled
			}
		}
	}
	if m.Block {
		<-ctx.Done()
		return engine.ErrCancelled
	}
	if request.GenerateConfigOut != "" && m.Root != "" {
		path := filepath.Join(m.Root, request.GenerateConfigOut)
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			_, _ = f.Write(m.GeneratedConfig)
			_ = f.Close()
		}
	}
	if m.PlanErr != nil {
		if m.WriteOnError {
			_ = os.WriteFile(request.Out, m.PlanJSON, 0o600)
		}
		return m.PlanErr
	}
	// A real engine writes a binary artifact; the mock writes the JSON it will show.
	return os.WriteFile(request.Out, m.PlanJSON, 0o600)
}
func (m *Fake) ShowPlan(_ context.Context, path string) ([]byte, error) {
	m.record("show-plan")
	if m.ShowErr != nil {
		return nil, m.ShowErr
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}

func (m *Fake) Apply(ctx context.Context, request engine.ApplyRequest, line func([]byte)) error {
	m.record("apply")
	if _, err := engine.ApplyArgs(request); err != nil {
		return err
	}
	b, err := os.ReadFile(request.Path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.applyRequests = append(m.applyRequests, request)
	m.applied = append(m.applied, b)
	m.mu.Unlock()
	for _, l := range m.ApplyLines {
		line(l)
	}
	if m.BlockApply {
		<-ctx.Done()
		time.Sleep(m.ApplyStopDelay)
		if m.ApplyFinishesAfterCancel {
			return nil
		}
		return engine.ErrCancelled
	}
	return m.ApplyErr
}

// Graph returns GraphDOT, GraphErr, or waits for cancellation with BlockGraph.
func (m *Fake) Graph(ctx context.Context) ([]byte, error) {
	m.record("graph")
	if m.BlockGraph {
		<-ctx.Done()
		return nil, engine.ErrCancelled
	}
	if m.GraphErr != nil {
		return nil, m.GraphErr
	}
	return m.GraphDOT, nil
}

// Init emulates init for tests. It writes nothing unless InitDataDir is set, in which case
// that directory is created on success, like the engine's data directory.
func (m *Fake) Init(ctx context.Context, request engine.InitRequest, line func(stream string, b []byte)) error {
	m.record("init")
	m.mu.Lock()
	m.inits = append(m.inits, engine.InitArgs(request.JSON))
	m.mu.Unlock()
	for _, l := range m.InitLines {
		line("stdout", l)
	}
	for _, l := range m.InitStderr {
		line("stderr", l)
	}
	if m.BlockInit {
		<-ctx.Done()
		return engine.ErrCancelled
	}
	if m.InitErr != nil {
		return m.InitErr
	}
	if m.InitDataDir != "" {
		return os.MkdirAll(m.InitDataDir, 0o755)
	}
	return nil
}

// InitCalls returns the argv of each Init call.
func (m *Fake) InitCalls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]string(nil), m.inits...)
}

// ListWorkspaces returns the fake's workspaces; ListErr fails it.
func (m *Fake) ListWorkspaces(context.Context) (engine.WorkspaceList, error) {
	m.record("workspace list")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return engine.WorkspaceList{}, m.ListErr
	}
	return engine.WorkspaceList{Names: slices.Clone(m.Workspaces), Current: m.CLIDefault}, nil
}

// WorkspaceState returns WorkspaceStates[name], or StateErr.
func (m *Fake) WorkspaceState(_ context.Context, name string) ([]byte, error) {
	m.record("show " + name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.StateErr != nil {
		return nil, m.StateErr
	}
	return m.WorkspaceStates[name], nil
}

// ChangeWorkspace emulates a workspace command. On success it applies the effect the real
// engines have: new adds and selects, select selects, delete removes. ListErrAfter then
// makes later listings fail, for partial-success tests.
func (m *Fake) ChangeWorkspace(ctx context.Context, action engine.WorkspaceAction, name string, line func(stream string, b []byte)) error {
	args, err := engine.WorkspaceArgs(action, name)
	if err != nil {
		return err
	}
	m.record(strings.Join(args, " "))
	for _, l := range m.WorkspaceLines {
		line("stdout", l)
	}
	if m.BlockWorkspace {
		<-ctx.Done()
		return engine.ErrCancelled
	}
	if m.WorkspaceErr != nil {
		return m.WorkspaceErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch action {
	case engine.WorkspaceNew:
		m.Workspaces = append(m.Workspaces, name)
		m.CLIDefault = name
	case engine.WorkspaceSelect:
		m.CLIDefault = name
	case engine.WorkspaceDelete:
		m.Workspaces = slices.DeleteFunc(m.Workspaces, func(n string) bool { return n == name })
	}
	if m.ListErrAfter {
		m.ListErr = errors.New("list failed")
	}
	return nil
}

// Validate returns ValidateOut (JSON) or ValidateHumanOut, or ToolErr.
func (m *Fake) Validate(ctx context.Context, json bool) (engine.Capture, error) {
	args := engine.ToolArgs(engine.ToolValidate, json, false)
	m.record(strings.Join(args, " "))
	if err := m.toolWait(ctx); err != nil {
		return engine.Capture{}, err
	}
	if json {
		return m.ValidateOut, nil
	}
	return m.ValidateHumanOut, nil
}

// FormatCheck returns FormatOut, or ToolErr.
func (m *Fake) FormatCheck(ctx context.Context, recursive bool) (engine.Capture, error) {
	m.record(strings.Join(engine.ToolArgs(engine.ToolFormat, false, recursive), " "))
	if err := m.toolWait(ctx); err != nil {
		return engine.Capture{}, err
	}
	return m.FormatOut, nil
}

// Providers returns ProvidersOut, or ToolErr.
func (m *Fake) Providers(ctx context.Context) (engine.Capture, error) {
	m.record("providers")
	if err := m.toolWait(ctx); err != nil {
		return engine.Capture{}, err
	}
	return m.ProvidersOut, nil
}

// OutputsMetadata parses OutputsOut like the CLI adapter does.
func (m *Fake) OutputsMetadata(ctx context.Context) (engine.Outputs, error) {
	m.record("output -json")
	if err := m.toolWait(ctx); err != nil {
		return engine.Outputs{}, err
	}
	return engine.OutputsFrom(m.OutputsOut)
}

func (m *Fake) toolWait(ctx context.Context) error {
	if m.BlockTool {
		<-ctx.Done()
		return engine.ErrCancelled
	}
	return m.ToolErr
}
