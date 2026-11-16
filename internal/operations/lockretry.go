package operations

import (
	"context"
	"time"
)

// FU-WIN-STATE-READ: on Windows the local backend's lock blocks other processes from reading
// terraform.tfstate, so a state read while another process holds the lock fails
// (workspace diagnostic state_locked). Hclaro then reads the state again on its own, a
// bounded number of times, and never unlocks anything.

// lockRetryDelays are the waits before each re-read: about two minutes in all. A variable
// only so tests can shorten it.
var lockRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}

// RetryLockedRead starts the bounded re-read when the workspace's last state read was
// blocked by a state lock. It is called after every read the manager or its owner starts.
func (m *Manager) RetryLockedRead() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retryLockedReadLocked()
}

// retryLockedReadLocked replaces any earlier re-read. Caller holds mu.
func (m *Manager) retryLockedReadLocked() {
	if m.closed || m.Reload == nil || m.root == "" || !m.store.Get().Locked() {
		return
	}
	m.stopLockRetryLocked()
	ctx, cancel := context.WithCancel(m.base)
	m.lockRetry = cancel
	m.wg.Add(1)
	go m.lockRetryLoop(ctx, cancel)
}

// stopLockRetryLocked cancels a scheduled re-read (a user's Reread replaces it). Caller holds mu.
func (m *Manager) stopLockRetryLocked() {
	if m.lockRetry != nil {
		m.lockRetry()
		m.lockRetry = nil
	}
}

func (m *Manager) lockRetryLoop(ctx context.Context, cancel context.CancelFunc) {
	defer m.wg.Done()
	defer cancel()
	for _, d := range lockRetryDelays {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		m.mu.Lock()
		// Never alongside an operation, after Close, or once the snapshot is no longer
		// locked (read again by the user, or changed by anything else).
		if ctx.Err() != nil || m.closed || m.running != nil || m.starting || !m.store.Get().Locked() {
			m.mu.Unlock()
			return
		}
		// A shared claim, as for Reread: a Hub init or workspace command keeps the root.
		release, ok := m.Locks.AcquireShared(m.root)
		m.mu.Unlock()
		if !ok {
			continue
		}
		m.Reload(ctx)
		release()
		if !m.store.Get().Locked() {
			return
		}
	}
}
