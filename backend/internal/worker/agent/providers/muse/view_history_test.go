package muse

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Seed an accepted raw-log subscription and a complete prefix. These view
// tests exercise transport and projection after native source admission.
func seedChildNativeSources(t *testing.T, a *Agent, id string, state *sessionState, count int64) {
	t.Helper()
	if state.log == nil {
		state.log = newNativeLog(id)
	}
	for sequence := int64(1); sequence <= count; sequence++ {
		raw, err := json.Marshal(map[string]any{"schema_version": 1, "id": fmt.Sprintf("source-%d", sequence), "sequence": sequence, "stream": nativeStream{Kind: "session", ID: id}, "record_type": "event", "durability": "durable", "payload_type": "future.native", "payload_schema_version": 1, "payload": map[string]string{"kind": "future_native_event"}})
		require.NoError(t, err)
		require.NoError(t, state.log.add(raw))
	}
	if state.log.subscriptionID == 0 {
		state.log.subscriptionID = int64(len(a.sessions) + 1)
	}
	if !slices.Contains(a.handshake.GrantedCapabilities, "rawLog") {
		a.handshake.GrantedCapabilities = append(a.handshake.GrantedCapabilities, "rawLog")
	}
}

func nativeHistoryChild(t *testing.T, a *Agent, sink *agenttest.Sink) (*sessionState, *agenttest.Sink) {
	t.Helper()
	key := museChildRowKey("session", "sub")
	id, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: key, AgentSessionID: "child"})
	require.NoError(t, err)
	state := &sessionState{sink: a.sink.ChildSink(id), childID: id, childKey: key, parentSessionID: "session", subagentID: "sub", items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog("child"), childViewPrefix: childViewPrefixPending}
	a.sessions["child"] = state
	seedChildNativeSources(t, a, "child", state, 3)
	return state, sink.Child(id)
}

func childHistoryItemEvent(t *testing.T, cursor string, item json.RawMessage, sequence int64) json.RawMessage {
	t.Helper()
	position := nativePosition{ID: fmt.Sprintf("source-%d", sequence), Sequence: sequence}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.MuseMethodItemUpdated, "params": map[string]any{"sessionId": "child", "viewCursor": cursor, "sourceRange": nativeSourceRange{Stream: nativeStream{Kind: "session", ID: "child"}, First: position, Last: position}, "item": item}})
	require.NoError(t, err)
	return raw
}

func TestCoherentChildPrefixPrecedesTheLiveSubscription(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "history", true: "empty history"}[empty], func(t *testing.T) {
			a, sink := testAgent(t)
			state, child := nativeHistoryChild(t, a, sink)
			var methods []string
			items := []json.RawMessage{}
			if !empty {
				items = append(items, json.RawMessage(`{"itemId":"first","revision":1,"kind":"agentMessage","status":"completed","text":"A","futureField":9007199254740993}`), json.RawMessage(`{"itemId":"second","revision":1,"kind":"agentMessage","status":"completed","text":"B"}`))
			}
			var historyEvents []json.RawMessage
			if !empty {
				historyEvents = append(historyEvents, childHistoryItemEvent(t, "opaque:prefix", items[0], 1), childHistoryItemEvent(t, "opaque:accepted", items[1], 2))
			}
			var live []byte
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				methods = append(methods, method)
				switch method {
				case methodSessionRead:
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.Equal(t, "child", params["sessionId"])
					assert.Equal(t, false, params["excludeItems"])
					session := map[string]any{"sessionId": "child"}
					if empty {
						session["status"], session["activeTurnId"] = "idle", nil
					}
					reply, err := json.Marshal(map[string]any{"session": session, "viewCursor": "opaque:accepted", "history": map[string]any{"mode": "snapshot", "snapshot": map[string]any{"schemaVersion": 1, "viewCursor": "opaque:accepted", "state": map[string]any{"items": items}}}})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: reply}
				case methodViewPage:
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.NotContains(t, params, "cursor")
					if empty {
						return agenttest.RPCReply{Result: json.RawMessage(`{"events":[],"nextCursor":null}`)}
					}
					reply, err := json.Marshal(map[string]any{"events": historyEvents[:1], "nextCursor": "opaque:prefix"})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: reply}
				case methodViewSubscribe:
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					after := "opaque:accepted"
					if !empty {
						after = "opaque:prefix"
						a.HandleOutput(historyEvents[1])
					}
					assert.Equal(t, after, params["after"])
					live = childHistoryItemEvent(t, "opaque:live", json.RawMessage(`{"itemId":"third","revision":1,"kind":"agentMessage","status":"completed","text":"C"}`), 3)
					a.HandleOutput(live)
					return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"opaque:live"}`)}
				default:
					t.Errorf("unexpected native history method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
				}
			}})
			a.requestChildView("child")
			a.viewWork.CloseAndWait()
			wantMethods := []string{methodSessionRead, methodViewPage, methodViewSubscribe}
			assert.Equal(t, wantMethods, methods)
			rows := child.Messages()
			require.Len(t, rows, len(items)+1)
			for index, original := range historyEvents {
				assert.Equal(t, []byte(original), rows[index].Content)
				assert.Empty(t, rows[index].SupplementalContent, "genuine view frames require no invented history envelope")
			}

			assert.Equal(t, live, rows[len(items)].Content)
			assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
			assert.Empty(t, state.viewOverlap)
		})
	}
}

func TestCoherentChildHeadReconcilesItsOwnRegistryRows(t *testing.T) {
	a, sink := testAgent(t)
	_, child := nativeHistoryChild(t, a, sink)
	keys := []string{museBackgroundTaskRowKey("child", "shell"), museWorkflowRowKey("child", "run"), museChildRowKey("child", "fold-only")}
	for _, key := range keys {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, Status: bgtask.StatusStopped}))
	}
	shell := json.RawMessage(`{"itemId":"shell","revision":1,"kind":"toolCall","tool":"shell","status":"inProgress","background":true}`)
	workflow := json.RawMessage(`{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"fold-only","attempt":1,"status":"scheduled"}]}`)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"head","history":{"mode":"inline","items":[{"itemId":"shell","revision":1,"kind":"toolCall","tool":"shell","status":"inProgress","background":true},{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"fold-only","attempt":1,"status":"scheduled"}]}]}}`)}
		case methodViewPage:
			data, err := json.Marshal(map[string]any{"events": []json.RawMessage{childHistoryItemEvent(t, "prefix", shell, 1)}, "nextCursor": "prefix"})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: data}
		case methodViewSubscribe:
			a.HandleOutput(childHistoryItemEvent(t, "head", workflow, 2))
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"head"}`)}
		default:
			t.Errorf("unexpected native history method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	for _, key := range keys {
		row, ok := sink.BackgroundTask(key)
		require.True(t, ok)
		want := bgtask.StatusRunning
		if key == museChildRowKey("child", "fold-only") {
			want = bgtask.StatusPending
		}
		assert.Equal(t, want, row.Status, key)
		assert.True(t, row.EndedAt.IsZero())
	}
	assert.ElementsMatch(t, keys, sink.RevivedTasks())
	assert.NotEmpty(t, child.Messages())
}
