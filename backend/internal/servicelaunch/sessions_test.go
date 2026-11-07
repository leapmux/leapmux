package servicelaunch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLastSessionControlsShutdown drives the last-session rule across
// the plan's session matrix: two sessions, logout of one, screen lock,
// remote disconnect, the final logout, and the enumeration error that
// is never zero sessions.
func TestLastSessionControlsShutdown(t *testing.T) {
	now := time.Now()
	session := func(id string, kind SessionKind) Session {
		return Session{ID: id, Kind: kind, StartedAt: now}
	}

	t.Run("two applicable sessions keep the instance", func(t *testing.T) {
		mayStop, reason := LastSessionRule([]Session{
			session("1", SessionInteractive), session("2", SessionInteractive),
		}, nil)
		assert.False(t, mayStop)
		assert.Contains(t, reason, "2")
	})

	t.Run("the last applicable session ends and the instance may stop", func(t *testing.T) {
		mayStop, _ := LastSessionRule([]Session{session("2", SessionService)}, nil)
		assert.True(t, mayStop)
	})

	t.Run("a locked screen is still a session", func(t *testing.T) {
		// Locking produces no session-list change at all: the locked
		// session stays interactive.
		mayStop, _ := LastSessionRule([]Session{session("1", SessionInteractive)}, nil)
		assert.False(t, mayStop)
	})

	t.Run("a disconnected remote session remains logged on", func(t *testing.T) {
		mayStop, _ := LastSessionRule([]Session{session("1", SessionDisconnected)}, nil)
		assert.False(t, mayStop, "disconnecting a remote session is not logging out")
	})

	t.Run("service and batch contexts never count", func(t *testing.T) {
		mayStop, _ := LastSessionRule([]Session{
			session("1", SessionService), session("2", SessionBatch), session("3", SessionListener),
		}, nil)
		assert.True(t, mayStop)
	})

	t.Run("an enumeration error is not zero sessions", func(t *testing.T) {
		mayStop, reason := LastSessionRule(nil, errors.New("access denied"))
		require.False(t, mayStop, "an unreadable session list must not stop a login instance")
		assert.Contains(t, reason, "never")
	})
}

type sessionMonitorFunc func(context.Context, AccountRef) ([]Session, error)

func (f sessionMonitorFunc) Sessions(ctx context.Context, account AccountRef) ([]Session, error) {
	return f(ctx, account)
}

func TestLoginSupervisorWaitsForLastSession(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	created := clock.Trap().NewTicker(loginSessionPollTag)
	defer created.Close()
	observed := make(chan int, 8)
	var calls atomic.Int32
	monitor := sessionMonitorFunc(func(_ context.Context, account AccountRef) ([]Session, error) {
		require.Equal(t, AccountRef{ID: "501", Name: "user"}, account)
		call := int(calls.Add(1))
		observed <- call
		switch call {
		case 1:
			return []Session{{ID: "first", Kind: SessionInteractive}, {ID: "second", Kind: SessionInteractive}}, nil
		case 2, 3:
			return []Session{{ID: "second", Kind: SessionInteractive}}, nil // First logout, then screen lock.
		case 4:
			return []Session{{ID: "second", Kind: SessionDisconnected}}, nil
		case 5:
			return nil, assert.AnError
		default:
			return nil, nil
		}
	})
	reported := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- WatchLoginSessions(ctx, monitor, AccountRef{ID: "501", Name: "user"}, clock, func(err error) { reported <- err })
	}()
	created.MustWait(ctx).MustRelease(ctx)
	for call := 1; call <= 6; call++ {
		select {
		case got := <-observed:
			require.Equal(t, call, got)
		case <-ctx.Done():
			t.Fatal("session supervisor did not enumerate")
		}
		if call < 6 {
			select {
			case err := <-done:
				t.Fatalf("supervisor ended before final logout: %v", err)
			default:
			}
			clock.Advance(time.Second).MustWait(ctx)
		}
	}
	select {
	case err := <-reported:
		require.ErrorIs(t, err, assert.AnError)
	case <-ctx.Done():
		t.Fatal("enumeration error was not reported")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("final logout did not end supervision")
	}
}

func TestLoginSupervisorCancellation(t *testing.T) {
	waitCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(waitCtx)
	clock := testutil.NewQuartzMock(t)
	created := clock.Trap().NewTicker(loginSessionPollTag)
	defer created.Close()
	done := make(chan error, 1)
	go func() {
		done <- WatchLoginSessions(ctx, sessionMonitorFunc(func(context.Context, AccountRef) ([]Session, error) {
			return []Session{{Kind: SessionInteractive}}, nil
		}), AccountRef{}, clock, nil)
	}()
	created.MustWait(ctx).MustRelease(ctx)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-waitCtx.Done():
		t.Fatal("supervisor did not join cancellation")
	}
}

// SessionKind.Applicable is exactly the interactive-and-disconnected
// set.
func TestSessionKindApplicable(t *testing.T) {
	for kind, want := range map[SessionKind]bool{
		SessionInteractive:  true,
		SessionDisconnected: true,
		SessionService:      false,
		SessionBatch:        false,
		SessionListener:     false,
	} {
		assert.Equal(t, want, kind.Applicable(), string(kind))
	}
}

func TestLoginSupervisorCancelsDuringNativeEnumeration(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	var enteredOnce, returnedOnce sync.Once
	monitor := &windowsSessionMonitor{enumerate: func() ([]windowsSessionEntry, error) {
		enteredOnce.Do(func() { close(entered) })
		defer returnedOnce.Do(func() { close(returned) })
		<-release
		return []windowsSessionEntry{{ID: 1, Kind: SessionInteractive, SID: "S-1-5-21-1"}}, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- WatchLoginSessions(ctx, monitor, AccountRef{ID: "S-1-5-21-1"}, quartz.NewReal(), nil) }()
	<-entered
	cancel()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		close(release)
		<-returned
	case <-deadline.C:
		close(release)
		<-returned
		require.ErrorIs(t, <-done, context.Canceled)
		t.Fatal("session supervision could not cancel during native enumeration")
	}
}
