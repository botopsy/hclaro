package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/rootlock"
	"hclaro/internal/workspace"
)

func waitStatus(t *testing.T, m *Manager) workspace.Snapshot {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if s := m.store.Get(); s.Status != "loading" {
			return s
		}
	}
	t.Fatal("store did not finish loading")
	return workspace.Snapshot{}
}

// A launch project read before init stays "uninitialized" until it is inspected and read
// again; Reread does both, and planning becomes available.
func TestRereadPicksUpAnInitializedProject(t *testing.T) {
	fake := &engine.Fake{}
	m, _ := setup(t, fake, workspace.Info{Project: "demo", Backend: "unknown", ExecutionMode: "unsupported"})
	if s := m.store.Get(); s.Diagnostic == nil || s.Diagnostic.Code != "uninitialized" || s.Info.Capabilities.CanPlan {
		t.Fatalf("%+v", s)
	}
	if err := m.Reread(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("without Reinspect: %v", err)
	}
	m.Reload = func(ctx context.Context) { m.store.Load(ctx, fake, "terraform") }
	initialized := workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"}
	m.Reinspect = func() workspace.Info { return initialized }
	if err := m.Reread(); err != nil {
		t.Fatal(err)
	}
	if s := waitStatus(t, m); s.Status != "ready" || s.Diagnostic != nil || !s.Info.Capabilities.CanPlan || !s.Info.Initialized {
		t.Fatalf("%+v", s)
	}
	for _, call := range fake.Calls() {
		if call == "init" || call == "plan" {
			t.Fatalf("reread ran %s", call)
		}
	}
}

func TestRereadIsRefusedWhileBusy(t *testing.T) {
	fake := &engine.Fake{Block: true}
	m, root := setup(t, fake, workspace.Info{})
	m.Reload = func(ctx context.Context) { m.store.Load(ctx, fake, "terraform") }
	m.Reinspect = func() workspace.Info {
		return workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"}
	}
	// Another operation (for example a Hub init) holds the root.
	m.Locks = rootlock.New()
	release, _ := m.Locks.Acquire(root, "init")
	if err := m.Reread(); !errors.Is(err, ErrBusy) {
		t.Fatalf("root claimed: %v", err)
	}
	release()
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Reread(); !errors.Is(err, ErrBusy) {
		t.Fatalf("plan running: %v", err)
	}
	if _, err := m.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, m, op.ID)
	block := make(chan struct{})
	m.Reload = func(ctx context.Context) { <-block; m.store.Load(ctx, fake, "terraform") }
	if err := m.Reread(); err != nil {
		t.Fatal(err)
	}
	if err := m.Reread(); !errors.Is(err, ErrLoading) {
		t.Fatalf("second reread while loading: %v", err)
	}
	if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrLoading) {
		t.Fatalf("plan while rereading: %v", err)
	}
	close(block)
	waitStatus(t, m)
}
