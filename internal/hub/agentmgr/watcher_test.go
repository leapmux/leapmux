package agentmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestManager_WatchAndBroadcast(t *testing.T) {
	m := New()
	w := m.Watch("a1")
	defer m.Unwatch("a1", w)

	event := &leapmuxv1.AgentEvent{
		AgentId: "a1",
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: &leapmuxv1.AgentStatusChange{
				AgentId: "a1",
				Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
			},
		},
	}
	m.Broadcast("a1", event)

	select {
	case got := <-w.C():
		require.NotNil(t, got.GetStatusChange())
		assert.Equal(t, "a1", got.GetStatusChange().GetAgentId())
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, got.GetStatusChange().GetStatus())
	default:
		require.Fail(t, "expected event on channel")
	}
}

func TestManager_Unwatch(t *testing.T) {
	m := New()
	w := m.Watch("a1")
	m.Unwatch("a1", w)

	// After unwatch, broadcast should not deliver.
	m.Broadcast("a1", &leapmuxv1.AgentEvent{
		AgentId: "a1",
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: &leapmuxv1.AgentStatusChange{
				AgentId: "a1",
				Status:  leapmuxv1.AgentStatus_AGENT_STATUS_CLOSED,
			},
		},
	})

	select {
	case <-w.C():
		require.Fail(t, "did not expect event after unwatch")
	default:
	}
}

func TestManager_BroadcastNoWatchers(t *testing.T) {
	m := New()
	// Should not panic.
	m.Broadcast("nonexistent", &leapmuxv1.AgentEvent{AgentId: "nonexistent"})
}

func TestManager_BufferOverflow(t *testing.T) {
	m := New()
	w := m.Watch("a1")
	defer m.Unwatch("a1", w)

	event := &leapmuxv1.AgentEvent{
		AgentId: "a1",
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: &leapmuxv1.AgentStatusChange{
				AgentId: "a1",
				Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
			},
		},
	}

	// Fill the buffer (64 capacity).
	for i := 0; i < 64; i++ {
		m.Broadcast("a1", event)
	}

	// Next broadcast should drop silently, not panic.
	m.Broadcast("a1", event)
}

func TestManager_BroadcastMany(t *testing.T) {
	m := New()
	w1 := m.Watch("a1")
	w2 := m.Watch("a2")
	defer m.Unwatch("a1", w1)
	defer m.Unwatch("a2", w2)

	events := []AgentBroadcast{
		{
			AgentID: "a1",
			Event: &leapmuxv1.AgentEvent{
				AgentId: "a1",
				Event: &leapmuxv1.AgentEvent_StatusChange{
					StatusChange: &leapmuxv1.AgentStatusChange{
						AgentId: "a1",
						Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
					},
				},
			},
		},
		{
			AgentID: "a2",
			Event: &leapmuxv1.AgentEvent{
				AgentId: "a2",
				Event: &leapmuxv1.AgentEvent_StatusChange{
					StatusChange: &leapmuxv1.AgentStatusChange{
						AgentId: "a2",
						Status:  leapmuxv1.AgentStatus_AGENT_STATUS_CLOSED,
					},
				},
			},
		},
	}
	m.BroadcastMany(events)

	select {
	case got := <-w1.C():
		require.NotNil(t, got.GetStatusChange())
		assert.Equal(t, "a1", got.GetStatusChange().GetAgentId())
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, got.GetStatusChange().GetStatus())
	default:
		require.Fail(t, "expected event on w1 channel")
	}

	select {
	case got := <-w2.C():
		require.NotNil(t, got.GetStatusChange())
		assert.Equal(t, "a2", got.GetStatusChange().GetAgentId())
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_CLOSED, got.GetStatusChange().GetStatus())
	default:
		require.Fail(t, "expected event on w2 channel")
	}
}

func TestManager_MultipleWatchers(t *testing.T) {
	m := New()
	w1 := m.Watch("a1")
	w2 := m.Watch("a1")
	defer m.Unwatch("a1", w1)
	defer m.Unwatch("a1", w2)

	event := &leapmuxv1.AgentEvent{
		AgentId: "a1",
		Event: &leapmuxv1.AgentEvent_StatusChange{
			StatusChange: &leapmuxv1.AgentStatusChange{
				AgentId: "a1",
				Status:  leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
			},
		},
	}
	m.Broadcast("a1", event)

	// Both watchers should receive the event.
	for _, w := range []*Watcher{w1, w2} {
		select {
		case got := <-w.C():
			require.NotNil(t, got.GetStatusChange())
			assert.Equal(t, "a1", got.GetStatusChange().GetAgentId())
			assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, got.GetStatusChange().GetStatus())
		default:
			require.Fail(t, "expected event on channel")
		}
	}
}
