package service

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// mockResponseWriter counts SendStream calls for testing broadcast deduplication.
type mockResponseWriter struct {
	channelID   string
	streamCount atomic.Int64
	// sendErr, when non-nil, is returned from SendStream to simulate a
	// dead transport for invalidation tests. Stored as a pointer so tests
	// can clear it mid-run with sendErr.Store(nil) — atomic.Value would
	// panic on a nil store.
	sendErr atomic.Pointer[error]
}

func (m *mockResponseWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error { return nil }
func (m *mockResponseWriter) SendError(_ int32, _ string) error                { return nil }
func (m *mockResponseWriter) SendStream(_ *leapmuxv1.InnerStreamMessage) error {
	m.streamCount.Add(1)
	if errPtr := m.sendErr.Load(); errPtr != nil {
		return *errPtr
	}
	return nil
}
func (m *mockResponseWriter) ChannelID() string { return m.channelID }

func newTestWatcher(channelID string) (*EventWatcher, *mockResponseWriter) {
	mock := &mockResponseWriter{channelID: channelID}
	sender := channel.NewSender(mock)
	return &EventWatcher{ChannelID: channelID, Sender: sender}, mock
}

// failSends arms the mock to return err from SendStream. Pass nil to clear.
func (m *mockResponseWriter) failSends(err error) {
	if err == nil {
		m.sendErr.Store(nil)
		return
	}
	m.sendErr.Store(&err)
}

func TestBroadcastTerminalEvent_DeduplicatesWithinPerTerminal(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-1")

	// Register the same watcher 5 times for the same terminal.
	for i := 0; i < 5; i++ {
		m.WatchTerminal("term-1", w)
	}

	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("a")}},
	})

	assert.Equal(t, int64(1), mock.streamCount.Load())
}

func TestBroadcastAgentEvent_DeduplicatesWithinWatchers(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-1")

	// Register the same watcher 5 times.
	for i := 0; i < 5; i++ {
		m.WatchAgent("agent-1", w)
	}

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
			Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		}},
	})

	assert.Equal(t, int64(1), mock.streamCount.Load())
}

func TestBroadcastTerminalEvent_DistinctWatchersAllReceive(t *testing.T) {
	m := NewWatcherManager()
	w1, mock1 := newTestWatcher("ch-1")
	w2, mock2 := newTestWatcher("ch-2")

	m.WatchTerminal("term-1", w1)
	m.WatchTerminal("term-1", w2)

	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("a")}},
	})

	assert.Equal(t, int64(1), mock1.streamCount.Load(), "watcher 1")
	assert.Equal(t, int64(1), mock2.streamCount.Load(), "watcher 2")
}

func TestWatchTerminal_IdempotentRegistration(t *testing.T) {
	m := NewWatcherManager()
	w, _ := newTestWatcher("ch-1")

	for i := 0; i < 5; i++ {
		m.WatchTerminal("term-1", w)
	}

	m.mu.RLock()
	got := len(m.terminals["term-1"])
	m.mu.RUnlock()

	assert.Equal(t, 1, got)
}

func TestWatchAgent_IdempotentRegistration(t *testing.T) {
	m := NewWatcherManager()
	w, _ := newTestWatcher("ch-1")

	for i := 0; i < 5; i++ {
		m.WatchAgent("agent-1", w)
	}

	m.mu.RLock()
	got := len(m.agents["agent-1"])
	m.mu.RUnlock()

	assert.Equal(t, 1, got)
}

func TestAgentEvent_DoesNotReachTerminalWatchers(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-1")

	// Only register for terminal events.
	m.WatchTerminal("term-1", w)

	// Broadcast an agent event.
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
			Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		}},
	})

	assert.Equal(t, int64(0), mock.streamCount.Load(), "expected 0 broadcasts to terminal watcher")
}

func TestTerminalEvent_DoesNotReachAgentWatchers(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-1")

	// Only register for agent events.
	m.WatchAgent("agent-1", w)

	// Broadcast a terminal event.
	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("a")}},
	})

	assert.Equal(t, int64(0), mock.streamCount.Load(), "expected 0 broadcasts to agent watcher")
}

func TestUnwatchAll_RemovesFromAllLists(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-1")

	m.WatchAgent("agent-1", w)
	m.WatchAgent("agent-2", w)
	m.WatchTerminal("term-1", w)
	m.WatchTerminal("term-2", w)

	// Unwatch all for channel ch-1.
	m.UnwatchAll("ch-1")

	// Verify no watchers remain.
	m.mu.RLock()
	agentCount := len(m.agents["agent-1"]) + len(m.agents["agent-2"])
	termCount := len(m.terminals["term-1"]) + len(m.terminals["term-2"])
	m.mu.RUnlock()

	assert.Equal(t, 0, agentCount, "expected 0 agent watchers after UnwatchAll")
	assert.Equal(t, 0, termCount, "expected 0 terminal watchers after UnwatchAll")

	// Verify no broadcasts reach the removed watcher.
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})
	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("a")}},
	})

	assert.Equal(t, int64(0), mock.streamCount.Load(), "expected 0 broadcasts after UnwatchAll")
}

func TestBroadcast_DropsWatcherOnSendError(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-dead")
	mock.failSends(errors.New("transport gone"))

	m.WatchAgent("agent-1", w)

	// First broadcast hits the dead sender once.
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "expected 1 send attempt before invalidation")

	// Watcher should have been dropped.
	m.mu.RLock()
	got := len(m.agents["agent-1"])
	m.mu.RUnlock()
	assert.Equal(t, 0, got, "expected watcher to be removed after SendStream error")

	// Subsequent broadcasts must skip the dead watcher.
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "expected no further sends after invalidation")
}

func TestBroadcast_TerminalDropsWatcherOnSendError(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-dead")
	mock.failSends(errors.New("transport gone"))

	m.WatchTerminal("term-1", w)

	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("a")}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load())

	m.mu.RLock()
	got := len(m.terminals["term-1"])
	m.mu.RUnlock()
	assert.Equal(t, 0, got, "expected terminal watcher to be removed after SendStream error")

	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("b")}},
	})
	assert.Equal(t, int64(1), mock.streamCount.Load(), "expected no further sends after invalidation")
}

func TestBroadcast_DropsOnlyDeadWatcher(t *testing.T) {
	m := NewWatcherManager()
	wDead, mockDead := newTestWatcher("ch-dead")
	mockDead.failSends(errors.New("transport gone"))
	wLive, mockLive := newTestWatcher("ch-live")

	m.WatchAgent("agent-1", wDead)
	m.WatchAgent("agent-1", wLive)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})
	assert.Equal(t, int64(1), mockDead.streamCount.Load())
	assert.Equal(t, int64(1), mockLive.streamCount.Load())

	m.mu.RLock()
	remaining := len(m.agents["agent-1"])
	m.mu.RUnlock()
	assert.Equal(t, 1, remaining, "live watcher should remain registered")

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})
	assert.Equal(t, int64(1), mockDead.streamCount.Load(), "dead watcher should not receive further events")
	assert.Equal(t, int64(2), mockLive.streamCount.Load(), "live watcher should receive subsequent events")
}

// TestWatcher_ReSubscribeAfterInvalidate pins that a channel that lost
// its watcher to a SendStream failure can re-register on the same
// agent and receive subsequent broadcasts. Without this the registry
// slot would stay stuck closed and reconnect would silently keep
// missing events.
func TestWatcher_ReSubscribeAfterInvalidate(t *testing.T) {
	m := NewWatcherManager()

	// First registration: send fails, watcher gets dropped.
	wDead, mockDead := newTestWatcher("ch-1")
	mockDead.failSends(errors.New("transport gone"))
	m.WatchAgent("agent-1", wDead)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})

	m.mu.RLock()
	got := len(m.agents["agent-1"])
	m.mu.RUnlock()
	assert.Equal(t, 0, got, "precondition: dead watcher should be dropped")

	// Re-subscribe on the same channel ID with a fresh sender.
	wAlive, mockAlive := newTestWatcher("ch-1")
	m.WatchAgent("agent-1", wAlive)

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})

	assert.Equal(t, int64(1), mockAlive.streamCount.Load(), "re-subscribed watcher should receive broadcasts")
}

// TestWatcher_InvalidateScopedToEntity pins the chosen semantic that a
// SendStream failure invalidates the watcher only for the failing
// entity, leaving the same channel's other-entity registrations intact
// AND functional. A future "drop the whole channel on first failure"
// change would surface here as a test failure rather than a behavior
// shift.
func TestWatcher_InvalidateScopedToEntity(t *testing.T) {
	m := NewWatcherManager()
	w, mock := newTestWatcher("ch-multi")
	mock.failSends(errors.New("transport gone"))

	m.WatchAgent("agent-1", w)
	m.WatchAgent("agent-2", w)

	// First send to agent-1 fails — should drop the agent-1 registration
	// but leave agent-2's intact (same channel, same sender).
	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})

	m.mu.RLock()
	agent1Count := len(m.agents["agent-1"])
	agent2Count := len(m.agents["agent-2"])
	m.mu.RUnlock()
	assert.Equal(t, 0, agent1Count, "agent-1 watcher should be dropped after its send failed")
	assert.Equal(t, 1, agent2Count, "agent-2 watcher should remain registered")

	// With the transient error cleared, agent-2 broadcasts must reach
	// the surviving watcher — proving the registration isn't merely
	// present but still functional.
	mock.failSends(nil)
	beforeAgent2 := mock.streamCount.Load()
	m.BroadcastAgentEvent("agent-2", &leapmuxv1.AgentEvent{
		AgentId: "agent-2",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-2",
		}},
	})
	assert.Equal(t, beforeAgent2+1, mock.streamCount.Load(), "agent-2 broadcast should reach the surviving watcher")
}

func TestUnwatchAll_PreservesOtherChannels(t *testing.T) {
	m := NewWatcherManager()
	w1, mock1 := newTestWatcher("ch-1")
	w2, mock2 := newTestWatcher("ch-2")

	m.WatchAgent("agent-1", w1)
	m.WatchAgent("agent-1", w2)
	m.WatchTerminal("term-1", w1)
	m.WatchTerminal("term-1", w2)

	// Unwatch only ch-1.
	m.UnwatchAll("ch-1")

	m.BroadcastAgentEvent("agent-1", &leapmuxv1.AgentEvent{
		AgentId: "agent-1",
		Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{
			AgentId: "agent-1",
		}},
	})
	m.BroadcastTerminalEvent("term-1", &leapmuxv1.TerminalEvent{
		TerminalId: "term-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("a")}},
	})

	assert.Equal(t, int64(0), mock1.streamCount.Load(), "ch-1: expected 0 broadcasts")
	assert.Equal(t, int64(2), mock2.streamCount.Load(), "ch-2: expected 2 broadcasts (agent+terminal)")
}
