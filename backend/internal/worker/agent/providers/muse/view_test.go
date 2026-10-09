package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewRetirementKeepsEveryReceivedBufferedNativeFrame(t *testing.T) {
	a, sink := testAgent(t)
	state := a.sessions["session"]
	recovery := &viewRecovery{after: "before", next: "after"}
	state.viewRecovery = recovery
	raw := feed(t, a, contracts.MuseMethodItemCompleted, map[string]any{
		"sessionId": "session", "viewCursor": "received",
		"item": map[string]any{"itemId": "native-text", "revision": 1, "kind": contracts.MuseItemKindAgentMessage, "status": contracts.MuseItemStatusCompleted, "text": "received native text"},
	})
	require.Len(t, recovery.buffer, 1)
	require.Empty(t, sink.Messages())
	a.retireHost(agent.MessageCompletionInterrupted)
	a.runViewRecovery("session", state, recovery)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, raw, messages[0].Content)
	assert.Equal(t, "session", messages[0].AgentSessionID)
	assert.Empty(t, sink.TurnActiveCalls)
	assert.Nil(t, state.viewRecovery)
}

func TestViewPagesRejectInvalidAndStalledCursors(t *testing.T) {
	for _, source := range []string{`{"events":[]}`, `{"events":[],"nextCursor":"more"}`, `{"events":[],"nextCursor":1}`, `{"events":[{"method":"future/event","params":{"sessionId":"foreign","viewCursor":"one"}}],"nextCursor":null}`} {
		t.Run(source, func(t *testing.T) {
			a, _ := testAgent(t)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodViewPage, method)
				return agenttest.RPCReply{Result: json.RawMessage(source)}
			}})
			_, err := a.readViewPages("session", "", "")
			require.Error(t, err)
		})
	}
}

func TestViewPagesKeepOpaqueCursorsAndStopAtTheTarget(t *testing.T) {
	a, _ := testAgent(t)
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		pages++
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "opaque:z", params["cursor"])
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"future/event","params":{"sessionId":"session","viewCursor":"opaque:a"}}],"nextCursor":"opaque:a"}`)}
	}})
	events, err := a.readViewPages("session", "opaque:z", "opaque:a")
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, 1, pages)
}

func TestViewPageFailureKeepsItsAlreadyReadNativePrefix(t *testing.T) {
	a, _ := testAgent(t)
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, _ json.RawMessage) agenttest.RPCReply {
		pages++
		if pages == 2 {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"page unavailable"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"future/event","params":{"sessionId":"session","viewCursor":"one"}}],"nextCursor":"one"}`)}
	}})
	events, err := a.readViewPages("session", "", "")
	require.Error(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, 2, pages)
}

func completedViewEvent(t *testing.T, cursor, itemID, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"method": contracts.MuseMethodItemCompleted,
		"params": map[string]any{
			"sessionId": "session", "viewCursor": cursor,
			"item": map[string]any{
				"itemId": itemID, "revision": 1, "kind": contracts.MuseItemKindAgentMessage,
				"status": contracts.MuseItemStatusCompleted, "text": text,
			},
		},
	})
	require.NoError(t, err)
	return raw
}

func TestViewRecoveryPublishesPagesAndBufferedFramesInNativeOrder(t *testing.T) {
	a, sink := testAgent(t)
	state := a.sessions["session"]
	recovery := &viewRecovery{after: "before", next: "overlap"}
	state.viewRecovery = recovery
	first := completedViewEvent(t, "first", "first-item", "First native text")
	second := completedViewEvent(t, "second", "second-item", "Second native text")
	overlap := completedViewEvent(t, "overlap", "overlap-item", "Overlapping native text")
	live := completedViewEvent(t, "live", "live-item", "Later native text")
	a.HandleOutput(overlap)
	a.HandleOutput(live)
	require.Len(t, recovery.buffer, 2)
	require.Empty(t, sink.Messages())
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		pages++
		var params struct {
			Cursor string `json:"cursor"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		var page any
		if pages == 1 {
			assert.Equal(t, "before", params.Cursor)
			page = map[string]any{"events": []json.RawMessage{first}, "nextCursor": "first"}
		} else {
			require.Equal(t, 2, pages)
			assert.Equal(t, "first", params.Cursor)
			page = map[string]any{"events": []json.RawMessage{second, overlap}, "nextCursor": nil}
		}
		result, err := json.Marshal(page)
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})

	a.runViewRecovery("session", state, recovery)
	assert.Equal(t, 2, pages)
	messages := sink.Messages()
	require.Len(t, messages, 4)
	for index, raw := range []json.RawMessage{first, second, overlap, live} {
		assert.Equal(t, []byte(raw), messages[index].Content)
		assert.Equal(t, "session", messages[index].AgentSessionID)
	}
	assert.Nil(t, state.viewRecovery)
	assert.Empty(t, sink.TurnActiveCalls)
}

func TestViewRecoveryExtendsASecondGapBeforeTheFinalDrain(t *testing.T) {
	a, sink := testAgent(t)
	state := a.sessions["session"]
	recovery := &viewRecovery{after: "before", next: "first"}
	state.viewRecovery = recovery
	first := completedViewEvent(t, "first", "first-item", "First native text")
	last := completedViewEvent(t, "last", "last-item", "Last native text")
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		pages++
		var params struct {
			Cursor string `json:"cursor"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		var event json.RawMessage
		if pages == 1 {
			assert.Equal(t, "before", params.Cursor)
			feed(t, a, methodViewGap, map[string]any{"sessionId": "session", "after": "first", "next": "last"})
			a.HandleOutput(last)
			event = first
		} else {
			require.Equal(t, 2, pages)
			assert.Equal(t, "first", params.Cursor)
			event = last
		}
		result, err := json.Marshal(map[string]any{"events": []json.RawMessage{event}, "nextCursor": nil})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})

	a.runViewRecovery("session", state, recovery)
	assert.Equal(t, 2, pages)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, []byte(first), messages[0].Content)
	assert.Equal(t, []byte(last), messages[1].Content)
	assert.Nil(t, state.viewRecovery)
	assert.Empty(t, sink.TurnActiveCalls)
}

func TestViewRecoveryKeepsReceivedFramesAfterPageFailure(t *testing.T) {
	for _, prefix := range []bool{false, true} {
		t.Run(map[bool]string{false: "first page", true: "later page"}[prefix], func(t *testing.T) {
			a, sink := testAgent(t)
			state := a.sessions["session"]
			recovery := &viewRecovery{after: "before", next: "live"}
			state.viewRecovery = recovery
			first := completedViewEvent(t, "first", "first-item", "Read native prefix")
			live := completedViewEvent(t, "live", "live-item", "Received native text")
			a.HandleOutput(live)
			pages := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodViewPage, method)
				pages++
				if prefix && pages == 1 {
					result, err := json.Marshal(map[string]any{"events": []json.RawMessage{first}, "nextCursor": "first"})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: result}
				}
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"The native page is unavailable"}`)}
			}})

			a.runViewRecovery("session", state, recovery)
			want := []json.RawMessage{live}
			if prefix {
				want = append([]json.RawMessage{first}, want...)
			}
			assert.Equal(t, len(want), pages)
			messages := sink.Messages()
			require.Len(t, messages, len(want))
			for index, raw := range want {
				assert.Equal(t, []byte(raw), messages[index].Content)
				assert.Equal(t, "session", messages[index].AgentSessionID)
			}
			assert.Nil(t, state.viewRecovery)
			assert.Empty(t, sink.TurnActiveCalls)
		})
	}
}

func TestViewPagesRejectARepeatedOpaqueCursor(t *testing.T) {
	a, _ := testAgent(t)
	event := completedViewEvent(t, "opaque", "native-item", "Native text")
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		pages++
		result, err := json.Marshal(map[string]any{"events": []json.RawMessage{event}, "nextCursor": "opaque"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})

	_, err := a.readViewPages("session", "", "missing")
	require.ErrorContains(t, err, "cursor did not advance")
	assert.Equal(t, 2, pages)
}
