package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/workspace"
)

// FU-WIN-STATE-READ: a state read blocked by another process's state lock is read again,
// bounded and cancellable, and nothing is planned on unread state meanwhile.

func shortRetries(m *Manager, n int, d time.Duration) {
	m.lockRetryDelays = make([]time.Duration, n)
	for i := range m.lockRetryDelays {
		m.lockRetryDelays[i] = d
	}
}

func shows(f *enginetest.Fake) int {
	n := 0
	for _, c := range f.Calls() {
		if c == "show" {
			n++
		}
	}
	return n
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lockedManager returns a manager whose workspace read is blocked by a lock (locked reads
// left), with Reload and Reinspect wired as in the CLI.
func lockedManager(t *testing.T, locked int) (*Manager, *enginetest.Fake) {
	t.Helper()
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog), LockedReads: 1 + locked}
	m, _ := setup(t, fake, workspace.Info{})
	m.Reload = func(ctx context.Context) { m.store.Load(ctx, fake, "terraform", nil) }
	info := m.store.Get().Info
	m.Reinspect = func() workspace.Info { return info }
	if !m.store.Get().Locked() {
		t.Fatalf("setup read not locked: %+v", m.store.Get())
	}
	return m, fake
}

// Apply → the post-apply read is locked → bounded re-read → ready → a plan reaches the engine.
func TestLockedReloadAfterApplyIsReadAgain(t *testing.T) {
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog)}
	m, _ := setup(t, fake, workspace.Info{})
	shortRetries(m, 10, 20*time.Millisecond)
	m.Reload = func(ctx context.Context) { m.store.Load(ctx, fake, "terraform", nil) }
	id := planned(t, m)
	fake.SetLockedReads(3) // the post-apply read and two re-reads
	done := wait(t, m, approveAndApply(t, m, id).ID)
	if done.Outcome != "applied" || done.StateReloaded || !done.StateReloadFailed {
		t.Fatalf("%+v", done)
	}
	eventually(t, "ready", func() bool { return m.store.Get().Status == "ready" })
	if caps := m.store.Get().Info.Capabilities; !caps.CanPlan || !caps.CanReadState {
		t.Fatalf("%+v", caps)
	}
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, m, op.ID); got.Status != Succeeded {
		t.Fatalf("plan after the lock was released: %+v", got)
	}
}

// While locked, a plan is refused before the engine with ErrStateLocked.
func TestPlanRefusedWhileStateLocked(t *testing.T) {
	m, fake := lockedManager(t, 100)
	shortRetries(m, 1, time.Hour)
	m.RetryLockedRead()
	before := len(fake.Requests())
	if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("plan while locked: %v", err)
	}
	if _, _, err := m.StartDriftScan(DriftStart{}); rejected(err) != "state_locked" {
		t.Fatalf("drift scan while locked: %v", err)
	}
	if _, _, err := m.StartGraph(GraphStart{}); rejected(err) != "state_locked" {
		t.Fatalf("graph while locked: %v", err)
	}
	if len(fake.Requests()) != before {
		t.Fatal("the engine planned on unread state")
	}
}

// The re-read stops at its bound and the snapshot stays state_locked; no further reads.
func TestLockedReadRetryIsBounded(t *testing.T) {
	m, fake := lockedManager(t, 100)
	shortRetries(m, 4, 5*time.Millisecond)
	before := shows(fake)
	m.RetryLockedRead()
	eventually(t, "the bound", func() bool { return shows(fake) == before+4 })
	time.Sleep(50 * time.Millisecond)
	if got := shows(fake); got != before+4 || !m.store.Get().Locked() {
		t.Fatalf("reads %d, want %d; snapshot %+v", got-before, 4, m.store.Get())
	}
}

// Close cancels a pending re-read promptly, and the manager waits for it (no leak).
func TestLockedReadRetryStopsOnClose(t *testing.T) {
	m, fake := lockedManager(t, 100)
	shortRetries(m, 3, time.Hour)
	before := shows(fake)
	m.RetryLockedRead()
	start := time.Now()
	m.Close(10 * time.Second)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Close waited %v", time.Since(start))
	}
	if shows(fake) != before {
		t.Fatal("state read after Close")
	}
}

// A user's Reread replaces the scheduled re-read: one read now, then one new bounded series.
func TestLockedReadRetryReplacedByReread(t *testing.T) {
	m, fake := lockedManager(t, 100)
	shortRetries(m, 3, 80*time.Millisecond)
	before := shows(fake)
	m.RetryLockedRead()
	// Half a period later, so a series that kept running would not coincide with the new one.
	time.Sleep(40 * time.Millisecond)
	if err := m.Reread(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new series", func() bool { return shows(fake) == before+1+3 })
	time.Sleep(300 * time.Millisecond)
	if got := shows(fake) - before; got != 4 {
		t.Fatalf("reads %d, want 4 (the replaced series must not run)", got)
	}
}

// A running operation stops the re-read without reading state alongside it.
func TestLockedReadRetryStopsForAnOperation(t *testing.T) {
	m, fake := lockedManager(t, 100)
	shortRetries(m, 3, 20*time.Millisecond)
	before := shows(fake)
	m.mu.Lock()
	m.running = &entry{}
	m.mu.Unlock()
	m.RetryLockedRead()
	time.Sleep(150 * time.Millisecond)
	m.mu.Lock()
	m.running = nil
	m.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if got := shows(fake); got != before {
		t.Fatalf("%d reads beside or after an operation", got-before)
	}
}

// A read that fails for another reason is not retried.
func TestUnlockedFailureIsNotRetried(t *testing.T) {
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	shortRetries(m, 3, 5*time.Millisecond)
	m.Reload = func(ctx context.Context) { m.store.Load(ctx, nil, "terraform", nil) }
	m.Reload(context.Background())
	before := shows(fake)
	m.RetryLockedRead()
	time.Sleep(60 * time.Millisecond)
	if shows(fake) != before || m.store.Get().Locked() {
		t.Fatal("a non-lock failure was retried")
	}
}
