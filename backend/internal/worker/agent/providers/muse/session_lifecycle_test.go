package muse

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

func TestMuseClearContextRefusesABusyNativeSessionBeforeWriting(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["session"].turnID = "busy-turn"
	writes := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(string, json.RawMessage) agenttest.RPCReply {
		writes++
		return agenttest.RPCReply{}
	}})
	id, err := a.ClearContext()
	require.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, id)
	assert.Zero(t, writes)
	assert.Equal(t, "session", a.sessionID)
	assert.Zero(t, sink.ResetSpanCount())
	assert.Empty(t, sink.SessionIDs())
}

func TestMuseClearContextPreservesTheOldSessionWhenNativeStartFails(t *testing.T) {
	a, sink := testAgent(t)
	old := a.sessions["session"]
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionStart, method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"The native start failed"}`)}
	}})
	id, err := a.ClearContext()
	require.ErrorContains(t, err, "The native start failed")
	assert.Empty(t, id)
	assert.Equal(t, "session", a.sessionID)
	assert.Same(t, old, a.sessions["session"])
	assert.False(t, old.retired)
	assert.Zero(t, sink.ResetSpanCount())
	assert.Empty(t, sink.SessionIDs())
}

func TestMuseClearContextRetiresTheOldSessionAndBothNativeSubscriptions(t *testing.T) {
	a, sink := testAgent(t)
	old := a.sessions["session"]
	old.log.subscriptionID = 7
	var methods []string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		methods = append(methods, method)
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement","modelId":null,"activeTurnId":null},"viewCursor":"replacement-cursor"}`)}
		}
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		if method == methodViewUnsubscribe {
			assert.Equal(t, "session", params["sessionId"])
		}
		if method == methodLogUnsubscribe {
			assert.Equal(t, float64(7), params["subscriptionId"])
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	id, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "replacement", id)
	assert.Equal(t, []string{"replacement"}, sink.SessionIDs())
	assert.Equal(t, 1, sink.ResetSpanCount())
	assert.True(t, old.retired)
	assert.Contains(t, methods, methodViewUnsubscribe)
	assert.Contains(t, methods, methodLogUnsubscribe)
	feed(t, a, methodTurnStarted, map[string]any{"sessionId": "session", "turnId": "late-old-turn"})
	assert.Empty(t, old.turnID)
	assert.Empty(t, sink.TurnActiveCalls)
}

func TestMuseClearContextKeepsTheCommittedReplacementAfterCleanupRefusal(t *testing.T) {
	a, sink := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement","modelId":null,"activeTurnId":null},"viewCursor":"replacement-cursor"}`)}
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"The old native subscription is unavailable"}`)}
	}})
	id, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "replacement", id)
	assert.Equal(t, "replacement", a.sessionID)
	assert.Equal(t, []string{"replacement"}, sink.SessionIDs())
	assert.True(t, a.sessions["session"].retired)
}

func TestMuseResumeUsesTheExactNativeSessionAndRejectsInvalidHandlesBeforeWriting(t *testing.T) {
	a, _ := testAgent(t)
	const id = "01a11719-71f4-76d1-b7d0-2dd11166e831"
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		calls++
		require.Equal(t, methodSessionResume, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, id, params["sessionId"])
		assert.Equal(t, true, params["excludeItems"])
		assert.NotContains(t, params, "workspaceRoot")
		assert.NotEmpty(t, params["commandId"])
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"` + id + `","modelId":null,"activeTurnId":null},"viewCursor":"native-cursor"}`)}
	}})
	require.Error(t, a.openSession("../invalid", time.Second))
	assert.Zero(t, calls)
	require.NoError(t, a.openSession(id, time.Second))
	assert.Equal(t, 1, calls)
	assert.Equal(t, id, a.sessionID)
}

func TestMuseCompactionRequiresIdleStateAndPreservesNativeNoop(t *testing.T) {
	a, _ := testAgent(t)
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		calls++
		require.Equal(t, methodSessionCompact, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "session", params["sessionId"])
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": contracts.MuseCompactionOutcomeNoop})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	a.sessions["session"].turnID = "busy"
	require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	assert.Zero(t, calls)
	a.sessions["session"].turnID = ""
	require.NoError(t, a.CompactContext())
	assert.Equal(t, 1, calls)
}
