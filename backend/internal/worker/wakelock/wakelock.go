// Package wakelock provides a platform-agnostic mechanism to prevent
// system sleep while the worker has stdio activity. An ActivityTracker
// acquires a wakelock on the first activity and releases it after an
// idle timeout (default 60s).
package wakelock

import (
	"log/slog"
	"sync"
	"time"
)

// WakeLock is the platform-specific sleep inhibitor.
type WakeLock interface {
	Acquire() error
	Release()
}

const defaultIdleTimeout = 60 * time.Second

// ActivityTracker holds a WakeLock while there is recent activity and
// releases it after an idle period.
type ActivityTracker struct {
	wl           WakeLock
	mu           sync.Mutex
	held         bool
	timer        *time.Timer
	timeout      time.Duration
	lastActivity time.Time
}

// NewActivityTracker creates a tracker with the platform WakeLock and
// the default 60-second idle timeout.
func NewActivityTracker() *ActivityTracker {
	return &ActivityTracker{
		wl:      newPlatformWakeLock(),
		timeout: defaultIdleTimeout,
	}
}

// RecordActivity acquires the wakelock if not held and resets the idle
// timer. To avoid excessive timer churn on hot paths (e.g. terminal
// output), the timer is only reset when at least half the timeout has
// elapsed since the last reset.
func (t *ActivityTracker) RecordActivity() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.held {
		if err := t.wl.Acquire(); err != nil {
			slog.Warn("failed to acquire wakelock", "error", err)
			return
		}
		t.held = true
		t.lastActivity = time.Now()
		t.timer = time.AfterFunc(t.timeout, t.releaseOnIdle)
		return
	}

	// Throttle timer resets: only reset when half the timeout has elapsed.
	now := time.Now()
	if now.Sub(t.lastActivity) < t.timeout/2 {
		return
	}
	t.lastActivity = now

	if t.timer != nil {
		t.timer.Stop()
	}
	t.timer = time.AfterFunc(t.timeout, t.releaseOnIdle)
}

func (t *ActivityTracker) releaseOnIdle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held {
		t.wl.Release()
		t.held = false
	}
}

// Close releases the wakelock unconditionally. Safe to call multiple
// times.
func (t *ActivityTracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	if t.held {
		t.wl.Release()
		t.held = false
	}
}
