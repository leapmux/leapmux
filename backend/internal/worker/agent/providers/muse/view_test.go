package muse

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func BenchmarkBufferViewFrameWithoutOverlap(b *testing.B) {
	a := &Agent{}
	state := &sessionState{}
	line := providerkit.ParseLine([]byte(`{"method":"item/updated","params":{"sessionId":"session","viewCursor":"cursor","item":{"itemId":"item","revision":1,"kind":"toolCall","status":"inProgress","tool":"bash","args":"{\"command\":\"printf native\"}"}}}`))
	b.ReportAllocs()
	for b.Loop() {
		a.bufferViewFrame(line, state)
	}
}

func TestViewFrameKeyPreservesDistinctEventsSharingACursor(t *testing.T) {
	first, valid := viewFrameKey([]byte(`{"jsonrpc":"2.0","method":"item/completed","params":{"sessionId":"session","viewCursor":"same","item":{"itemId":"first"}},"emittedAtMs":1}`))
	require.True(t, valid)
	duplicate, valid := viewFrameKey([]byte(`{"method":"item/completed","params":{"item":{"itemId":"first"},"viewCursor":"same","sessionId":"session"},"emittedAtMs":2}`))
	require.True(t, valid)
	assert.Equal(t, first, duplicate)
	other, valid := viewFrameKey([]byte(`{"method":"item/completed","params":{"sessionId":"session","viewCursor":"same","item":{"itemId":"second"}}}`))
	require.True(t, valid)
	assert.NotEqual(t, first, other)
}

func TestViewPageRejectsACursorThatDoesNotIdentifyItsLastEvent(t *testing.T) {
	a, _ := testAgent(t)
	event := completedViewEvent(t, "actual-last", "item", "Native text")
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		raw, err := json.Marshal(map[string]any{"events": []json.RawMessage{event}, "nextCursor": "unrelated"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: raw}
	}})
	_, _, err := a.readViewPage("session", nil, a.APITimeout())
	require.Error(t, err)
}

func TestViewRecoveryDoesNotRetainTheHistoricalPrefixAsLiveOverlap(t *testing.T) {
	a, sink := testAgent(t)
	state := a.sessions["session"]
	recovery := &viewRecovery{next: "head"}
	state.viewRecovery = recovery
	var events []json.RawMessage
	for index := range 250 {
		cursor := fmt.Sprintf("historical-%d", index)
		if index == 249 {
			cursor = "head"
		}
		events = append(events, completedViewEvent(t, cursor, fmt.Sprintf("item-%d", index), "Historical native text"))
	}
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		raw, err := json.Marshal(map[string]any{"events": events, "nextCursor": nil})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: raw}
	}})
	a.runViewRecovery("session", state, recovery)
	assert.Len(t, sink.Messages(), len(events))
	assert.Empty(t, state.viewOverlap)
	a.HandleOutput(events[len(events)-1])
	assert.Len(t, sink.Messages(), len(events), "native item identity also prevents a delayed duplicate")
}

func TestViewRecoveryRunsGoalObserversWithoutAnotherNativeFrame(t *testing.T) {
	a, sink := goalTestAgent(t)
	observed := 0
	observer := &museGoalObserverSink{ProviderServices: a.sink, observe: func() {
		observed++
		if !a.dispatcher.mu.TryLock() {
			t.Error("a recovered goal observer holds dispatcher.mu")
			return
		}
		a.dispatcher.mu.Unlock()
		feed(t, a, contracts.MuseMethodTodoListChanged, map[string]any{"sessionId": "session", "items": []any{}})
	}}
	a.sink = agent.NewProviderServices(observer)
	state := a.sessions["session"]
	state.sink = a.sink
	recovery := &viewRecovery{after: "before", next: "goal-cursor"}
	state.viewRecovery = recovery
	event, err := json.Marshal(map[string]any{"method": methodSessionGoalChanged, "params": goalEventParams("session", map[string]any{"objective": "Recovered native goal", "status": "active"})})
	require.NoError(t, err)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		raw, err := json.Marshal(map[string]any{"events": []json.RawMessage{event}, "nextCursor": nil})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: raw}
	}})
	a.runViewRecovery("session", state, recovery)
	require.Len(t, sink.Goals(), 1)
	assert.Equal(t, "Recovered native goal", sink.Goals()[0].Objective)
	assert.Equal(t, 1, observed)
}

func TestViewRecoveryAppliesADispatchedGoalBeforeALaterPageFailure(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "upsert", true: "clear"}[clear], func(t *testing.T) {
			a, sink := goalTestAgent(t)
			if clear {
				sink.UpsertGoal(agent.GoalUpdate{Objective: "Previous goal", Status: agent.GoalStatusActive})
			}
			state := a.sessions["session"]
			recovery := &viewRecovery{after: "before", next: "unavailable"}
			state.viewRecovery = recovery
			var goal any = map[string]any{"objective": "Recovered goal", "status": "active"}
			if clear {
				goal = nil
			}
			event, err := json.Marshal(map[string]any{"method": methodSessionGoalChanged, "params": goalEventParams("session", goal)})
			require.NoError(t, err)
			pages := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodViewPage, method)
				pages++
				if pages == 1 {
					raw, err := json.Marshal(map[string]any{"events": []json.RawMessage{event}, "nextCursor": "goal-cursor"})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: raw}
				}
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"page unavailable"}`)}
			}})
			a.runViewRecovery("session", state, recovery)
			if clear {
				assert.Equal(t, 1, sink.GoalClears())
				before := len(sink.Goals())
				sink.UpdateGoalStatus(agent.GoalStatusActive, agent.GoalStatusDone)
				assert.Len(t, sink.Goals(), before)
			} else {
				require.Len(t, sink.Goals(), 1)
				assert.Equal(t, "Recovered goal", sink.Goals()[0].Objective)
			}
			assert.Empty(t, a.dispatcher.actions)
		})
	}
}

func TestViewRecoverySerializesGoalObserversWithConcurrentNativeInput(t *testing.T) {
	a, sink := goalTestAgent(t)
	state := a.sessions["session"]
	recovery := &viewRecovery{after: "before", next: "second"}
	state.viewRecovery = recovery
	var events []json.RawMessage
	for index, objective := range []string{"First goal", "Second goal"} {
		params := goalEventParams("session", map[string]any{"objective": objective, "status": "active"})
		params["viewCursor"] = []string{"first", "second"}[index]
		position := nativePosition{ID: fmt.Sprintf("goal-%d", index), Sequence: int64(index + 1)}
		params["sourceRange"] = nativeSourceRange{Stream: nativeStream{Kind: "session", ID: "session"}, First: position, Last: position}
		event, err := json.Marshal(map[string]any{"method": methodSessionGoalChanged, "params": params})
		require.NoError(t, err)
		events = append(events, event)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseObserver := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseObserver)
	var first atomic.Bool
	observer := &museGoalObserverSink{ProviderServices: a.sink, observe: func() {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}}
	a.sink, state.sink = observer, observer
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		raw, err := json.Marshal(map[string]any{"events": events, "nextCursor": nil})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: raw}
	}})
	done := make(chan struct{})
	go func() { a.runViewRecovery("session", state, recovery); close(done) }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the goal observer did not start")
	}
	params := goalEventParams("session", map[string]any{"objective": "Third goal", "status": "active"})
	params["viewCursor"] = "third"
	params["sourceRange"] = nativeSourceRange{Stream: nativeStream{Kind: "session", ID: "session"}, First: nativePosition{ID: "third-record", Sequence: 3}, Last: nativePosition{ID: "third-record", Sequence: 3}}
	feed(t, a, methodSessionGoalChanged, params)
	assert.Empty(t, sink.Goals(), "the concurrent caller cannot overtake the paused observer")
	releaseObserver()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the serial goal drain did not finish")
	}
	goals := sink.Goals()
	require.Len(t, goals, 3)
	assert.Equal(t, "First goal", goals[0].Objective)
	assert.Equal(t, "Second goal", goals[1].Objective)
	assert.Equal(t, "Third goal", goals[2].Objective)
	assert.Empty(t, a.dispatcher.actions)
	assert.False(t, a.dispatcher.draining)
}

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
