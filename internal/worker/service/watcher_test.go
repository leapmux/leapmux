package service

import (
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// mockResponseWriter counts SendStream calls for testing broadcast deduplication.
type mockResponseWriter struct {
	channelID   string
	streamCount atomic.Int64
}

func (m *mockResponseWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error { return nil }
func (m *mockResponseWriter) SendError(_ int32, _ string) error                { return nil }
func (m *mockResponseWriter) SendStream(_ *leapmuxv1.InnerStreamMessage) error {
	m.streamCount.Add(1)
	return nil
}
func (m *mockResponseWriter) ChannelID() string { return m.channelID }

func newTestWatcher(channelID string) (*EventWatcher, *mockResponseWriter) {
	mock := &mockResponseWriter{channelID: channelID}
	sender := channel.NewSender(mock)
	return &EventWatcher{ChannelID: channelID, Sender: sender}, mock
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

	if got := mock.streamCount.Load(); got != 1 {
		t.Errorf("expected 1 broadcast, got %d", got)
	}
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

	if got := mock.streamCount.Load(); got != 1 {
		t.Errorf("expected 1 broadcast, got %d", got)
	}
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

	if got := mock1.streamCount.Load(); got != 1 {
		t.Errorf("watcher 1: expected 1 broadcast, got %d", got)
	}
	if got := mock2.streamCount.Load(); got != 1 {
		t.Errorf("watcher 2: expected 1 broadcast, got %d", got)
	}
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

	if got != 1 {
		t.Errorf("expected 1 registered watcher, got %d", got)
	}
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

	if got != 1 {
		t.Errorf("expected 1 registered watcher, got %d", got)
	}
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

	if got := mock.streamCount.Load(); got != 0 {
		t.Errorf("expected 0 broadcasts to terminal watcher, got %d", got)
	}
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

	if got := mock.streamCount.Load(); got != 0 {
		t.Errorf("expected 0 broadcasts to agent watcher, got %d", got)
	}
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

	if agentCount != 0 {
		t.Errorf("expected 0 agent watchers after UnwatchAll, got %d", agentCount)
	}
	if termCount != 0 {
		t.Errorf("expected 0 terminal watchers after UnwatchAll, got %d", termCount)
	}

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

	if got := mock.streamCount.Load(); got != 0 {
		t.Errorf("expected 0 broadcasts after UnwatchAll, got %d", got)
	}
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

	if got := mock1.streamCount.Load(); got != 0 {
		t.Errorf("ch-1: expected 0 broadcasts, got %d", got)
	}
	if got := mock2.streamCount.Load(); got != 2 {
		t.Errorf("ch-2: expected 2 broadcasts (agent+terminal), got %d", got)
	}
}
