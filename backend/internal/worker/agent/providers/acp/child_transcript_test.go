package acp

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviveChildRowValidatesItsExactNativeRoute(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"valid", "root", "child", "row", "write refused"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			b, sink := newChildRouteBase(t)
			spawn := &SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning}
			b.ApplySubagentObservation(spawn)
			b.ApplySubagentObservation(&SubagentObservation{RowKey: "native-key", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})
			b.ApplySubagentObservation(spawn)
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			target := ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}
			refused := errors.New("native row write refused")
			switch changed {
			case "root":
				target.RootSessionID = "another-root"
			case "child":
				target.SessionID = "another-child"
			case "row":
				target.RowKey = "another-row"
			case "write refused":
				sink.Child(rows[0].ChildAgentID).ReviveErr = refused
			}
			err := b.ReviveChildRow(target)
			if changed == "valid" {
				require.NoError(t, err)
				assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)
			} else {
				if changed == "write refused" {
					require.ErrorIs(t, err, refused)
				} else {
					require.ErrorIs(t, err, agent.ErrChildRouteNotReady)
				}
				assert.Equal(t, bgtask.StatusSucceeded, sink.BackgroundTasks()[0].Status)
			}
		})
	}
}

func TestWithChildTranscriptCapturesTheExactNativeDestination(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning})
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	var selected agent.ProviderServices
	err := b.WithChildTranscript(ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}, func(writer agent.ProviderServices) error {
		selected = writer
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "native-child-session", selected.MessageSessionID())
	assert.NotEqual(t, sink.LastSessionID(), selected.MessageSessionID())
}

func TestWithChildTranscriptRefusesForeignAndClosedTargets(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"root", "child", "row", "closed", "nil callback"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			b, _ := newChildRouteBase(t)
			b.ApplySubagentObservation(&SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning})
			target := ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}
			called := false
			capture := func(agent.ProviderServices) error { called = true; return nil }
			switch test {
			case "root":
				target.RootSessionID = "other-root"
			case "child":
				target.SessionID = "other-child"
			case "row":
				target.RowKey = "other-row"
			case "closed":
				b.ApplySubagentObservation(&SubagentObservation{RowKey: "native-key", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})
			case "nil callback":
				capture = nil
			}
			require.ErrorIs(t, b.WithChildTranscript(target, capture), agent.ErrChildRouteNotReady)
			assert.False(t, called)
		})
	}
}

func TestReviveChildRowReopensClosedFinishedChildDirectly(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	spawn := &SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning}
	b.ApplySubagentObservation(spawn)
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "native-key", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.Equal(t, bgtask.StatusSucceeded, rows[0].Status)

	target := ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}
	require.NoError(t, b.ReviveChildRow(target))
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)

	var selected agent.ProviderServices
	require.NoError(t, b.WithChildTranscript(target, func(services agent.ProviderServices) error {
		selected = services
		return nil
	}))
	require.NotNil(t, selected)
	assert.Equal(t, "native-child-session", selected.MessageSessionID())
	assert.Contains(t, b.openRows.takeAll(), "native-key")
}

func TestReviveChildRowRejectsRefusedMismatchedAndFailingClosedChild(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"refused", "session_mismatch", "revive_error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			b, sink := newChildRouteBase(t)
			spawn := &SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning}
			b.ApplySubagentObservation(spawn)
			b.ApplySubagentObservation(&SubagentObservation{RowKey: "native-key", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly})
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			target := ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}
			reviveErr := errors.New("revive refused")
			switch mode {
			case "refused":
				b.refuseChild("native-key")
			case "session_mismatch":
				target.SessionID = "other-child-session"
			case "revive_error":
				sink.Child(rows[0].ChildAgentID).ReviveErr = reviveErr
			}

			err := b.ReviveChildRow(target)
			if mode == "revive_error" {
				require.ErrorIs(t, err, reviveErr)
			} else {
				require.ErrorIs(t, err, agent.ErrChildRouteNotReady)
			}
			b.children.mu.Lock()
			_, stillClosed := b.children.closed["native-key"]
			b.children.mu.Unlock()
			assert.True(t, stillClosed)
			assert.NotContains(t, b.openRows.takeAll(), "native-key")
			assert.Equal(t, bgtask.StatusSucceeded, sink.BackgroundTasks()[0].Status)
		})
	}
}

type reviveInspectServices struct {
	agent.ProviderServices
	onRevive func(rowKey string)
}

func (s *reviveInspectServices) ChildSink(childAgentID string) agent.ProviderServices {
	return &reviveInspectServices{
		ProviderServices: s.ProviderServices.ChildSink(childAgentID),
		onRevive:         s.onRevive,
	}
}

func (s *reviveInspectServices) ReviveBackgroundTask(rowKey string) error {
	if s.onRevive != nil {
		s.onRevive(rowKey)
	}
	return s.ProviderServices.ReviveBackgroundTask(rowKey)
}

func TestReviveChildRowReleasesLocksDuringRegistryRevive(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	var sessionLockFree, feedLockFree bool
	var active *childConversation
	var mu sync.Mutex
	b.sink = &reviveInspectServices{
		ProviderServices: b.sink,
		onRevive: func(string) {
			mu.Lock()
			c := active
			mu.Unlock()
			if b.sessionMu.TryLock() {
				sessionLockFree = true
				b.sessionMu.Unlock()
			}
			if c != nil && c.feedMu.TryLock() {
				feedLockFree = true
				c.feedMu.Unlock()
			}
		},
	}

	spawn := &SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning}
	b.ApplySubagentObservation(spawn)
	require.True(t, b.FeedChildUpdate("native-key", json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step","status":"pending"}]}`)))
	require.NoError(t, sink.CloseBackgroundTask("native-key", bgtask.StatusSucceeded))
	b.children.mu.Lock()
	mu.Lock()
	active = b.children.active["native-key"]
	mu.Unlock()
	b.children.mu.Unlock()
	require.NotNil(t, active)

	target := ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}
	require.NoError(t, b.ReviveChildRow(target))
	assert.True(t, sessionLockFree, "sessionMu must not be held across ReviveBackgroundTask")
	assert.True(t, feedLockFree, "active.feedMu must not be held across ReviveBackgroundTask")
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)
}

func TestReviveChildRowRejectsRouteInvalidatedDuringRevive(t *testing.T) {
	t.Parallel()
	b, sink := newChildRouteBase(t)
	b.sink = &reviveInspectServices{
		ProviderServices: b.sink,
		onRevive: func(string) {
			// Simulate concurrent close of the child route while ReviveBackgroundTask runs.
			b.finishChildConversation("native-key", bgtask.StatusSucceeded)
			b.forgetChild("native-key")
		},
	}

	spawn := &SubagentObservation{RowKey: "native-key", ChildAgentKey: "native-key", ChildAgentSessionID: "native-child-session", Title: "Native child", Status: bgtask.StatusRunning}
	b.ApplySubagentObservation(spawn)
	require.True(t, b.FeedChildUpdate("native-key", json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step","status":"pending"}]}`)))
	require.NoError(t, sink.CloseBackgroundTask("native-key", bgtask.StatusSucceeded))
	b.openRows.closed("native-key")

	target := ChildSessionTarget{RootSessionID: "session-1", SessionID: "native-child-session", RowKey: "native-key"}
	err := b.ReviveChildRow(target)
	require.ErrorIs(t, err, agent.ErrChildRouteNotReady)
	b.children.mu.Lock()
	_, closed := b.children.closed["native-key"]
	b.children.mu.Unlock()
	assert.True(t, closed, "a route closed during revive must stay closed")
	assert.NotContains(t, b.openRows.takeAll(), "native-key")
}
