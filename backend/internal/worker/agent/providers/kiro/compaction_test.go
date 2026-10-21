package kiro

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manual compaction is live state with its trigger, and no stored row.
func TestKiroManualCompactionIsLiveState(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == kiroCompactMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	_ = requests
	require.NoError(t, a.CompactContext())
	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "manual"},
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	for _, row := range sink.PersistedNotifications() {
		assert.NotContains(t, string(row.Content), "compacting", "no stored start row")
	}
	assert.Empty(t, sink.Notifications(), "no worker start row")
}

// The automatic summarization is live state, and its native end stores one
// boundary and ends the live key.
func TestKiroAutomaticCompactionIsLiveThenOneEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind string
	}{
		{"completed", "summarization_completed"},
		{"failed", "summarization_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			a.HandleOutput(kiroSessionInfo(t, "summarization_started"))
			a.HandleOutput(kiroSessionInfo(t, tc.kind))
			values := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
			require.Len(t, values, 2)
			assert.Equal(t, map[string]interface{}{contracts.CompactionFieldTrigger: "auto"}, values[0])
			assert.Nil(t, values[1])
			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1, "one native end row")
			assert.Contains(t, string(rows[0].Content), tc.kind)
			assert.Empty(t, sink.Notifications(), "no worker status row")
		})
	}
}

// Each finishCompaction outcome ends the live state and keeps its own row,
// because Kiro answers a manual compaction with a bare result.
func TestKiroCompactionFinishEndsLiveStateAndKeepsItsRow(t *testing.T) {
	t.Parallel()
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method == kiroCompactMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"success":false}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
		require.NoError(t, a.CompactContext())
		require.Eventually(t, func() bool {
			values := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
			return len(values) >= 2 && values[len(values)-1] == nil
		}, time.Second, 5*time.Millisecond, "the refusal ends the live state")
		require.NotEmpty(t, sink.Notifications(), "the refusal keeps its own row")
	})
	t.Run("nothing to compact", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newKiroAgent(t, agent.Options{}, func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method == kiroCompactMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
		require.NoError(t, a.CompactContext())
		require.Eventually(t, func() bool {
			return len(sink.Notifications()) > 0
		}, time.Second, 5*time.Millisecond)
		values := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
		require.Len(t, values, 2)
		assert.Nil(t, values[1])
	})
}

// An interrupt during a manual compaction ends the live state and keeps its
// row.
func TestKiroInterruptDuringACompactionEndsLiveState(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.stateMu.Lock()
	a.compaction = compactionState{id: 7, lastID: 7}
	a.stateMu.Unlock()
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	values := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
	require.Len(t, values, 1)
	assert.Nil(t, values[0])
	require.NotEmpty(t, sink.Notifications(), "the interrupt keeps its own row")
}

func kiroSessionInfo(t *testing.T, kind string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"kiro": map[string]any{"kind": kind}},
			},
		},
	})
}
