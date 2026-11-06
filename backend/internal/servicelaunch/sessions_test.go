package servicelaunch

import (
	"errors"
	"testing"
	"time"

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
