package muse

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildRestorationReadsEveryForwardPage(t *testing.T) {
	a, root := testAgent(t)
	_, sink := nativeHistoryChild(t, a, root)
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		if method == methodSessionRead {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child","status":"idle","activeTurnId":null},"viewCursor":"cursor-2","history":{"mode":"none","items":null,"snapshot":null}}`)}
		}
		if method == methodViewSubscribe {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"completed child has no live view"}`)}
		}
		require.Equal(t, methodViewPage, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "forward", params["direction"])
		pages++
		var next any = "cursor-1"
		if pages == 1 {
			assert.NotContains(t, params, "cursor")
		} else {
			require.Equal(t, 2, pages)
			next = nil
			assert.Equal(t, "cursor-1", params["cursor"])
		}
		event := childHistoryItemEvent(t, "cursor-"+jsonNumber(pages), json.RawMessage(fmt.Sprintf(`{"itemId":"text-%d","revision":1,"kind":"agentMessage","status":"completed","text":"page-%d"}`, pages, pages)), int64(pages))
		reply, err := json.Marshal(map[string]any{"events": []json.RawMessage{event}, "nextCursor": next})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: reply}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, 2, pages)
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), "page-1")
	assert.Contains(t, string(sink.Messages()[1].Content), "page-2")
}

func TestCompletedChildRestorationReadsStoredHistoryWithoutALiveSubscription(t *testing.T) {
	a, root := testAgent(t)
	_, sink := nativeHistoryChild(t, a, root)
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionRead {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child","status":"idle","activeTurnId":null},"viewCursor":"child-cursor","history":{"mode":"none","items":null,"snapshot":null}}`)}
		}
		if method == methodViewSubscribe {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"session child not found","data":{"kind":"sessionNotFound"}}`)}
		}
		require.Equal(t, methodViewPage, method)
		pages++
		frame := childHistoryItemEvent(t, "child-cursor", json.RawMessage(`{"itemId":"reply","kind":"agentMessage","revision":1,"status":"completed","text":"Stored child reply"}`), 1)
		result, err := json.Marshal(map[string]any{"events": []json.RawMessage{frame}, "nextCursor": nil})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, 1, pages)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "Stored child reply")
	assert.Empty(t, root.LeapMuxNotifications())
}

func TestNativeChildUserContextDoesNotDuplicateWorkerOwnedInputs(t *testing.T) {
	a, root := testAgent(t)
	state, sink := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.childViewPrefix = childViewPrefixReady
	for index, text := range []string{"Actual child assignment", "Human followup"} {
		raw, err := json.Marshal(map[string]string{"content": text})
		require.NoError(t, err)
		require.NoError(t, state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: raw}, agent.SpanInfo{}))
		turn := "child-turn-" + jsonNumber(index)
		frames := []struct {
			method string
			params any
		}{
			{methodTurnStarted, map[string]any{"sessionId": "child", "turnId": turn}},
			{contracts.MuseMethodItemCompleted, itemParams{SessionID: "child", Item: nativeItem{ID: "native-user-" + jsonNumber(index), Kind: contracts.MuseItemKindUserMessage, TurnID: &turn, Revision: 1, Status: "completed", Text: "Generated native context that wraps the assigned input"}}},
			{contracts.MuseMethodItemCompleted, itemParams{SessionID: "child", Item: nativeItem{ID: "native-answer-" + jsonNumber(index), Kind: contracts.MuseItemKindAgentMessage, TurnID: &turn, Revision: 1, Status: "completed", Text: "The child answered"}}},
			{contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "child", "turnId": turn, "terminal": "completed"}},
		}
		for _, frame := range frames {
			raw, err := json.Marshal(map[string]any{"method": frame.method, "params": frame.params})
			require.NoError(t, err)
			a.HandleOutput(provedChildFrame(t, a, state, raw))
		}
		a.syncView("child")
	}
	users, answers := 0, 0
	for _, message := range sink.Messages() {
		if message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			users++
		}
		if message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && len(message.Content) > 0 {
			var frame struct {
				Params itemParams `json:"params"`
			}
			require.NoError(t, json.Unmarshal(message.Content, &frame))
			if frame.Params.Item.Kind == contracts.MuseItemKindAgentMessage {
				answers++
			}
		}
	}
	assert.Equal(t, 2, users, "opening assignment and queued input own the user rows")
	assert.Equal(t, 2, answers, "each native child answer keeps its own session")
	assert.Empty(t, root.Messages())
}

func TestUnknownFinalChildStatusEndsWithoutAnOutcome(t *testing.T) {
	status := resolvedChildStatus(nativeItem{Status: "futureFinal", ControlStatus: "running"})
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, status)
	assert.True(t, status.IsFinished())
	assert.False(t, status.IsWorking())
	assert.NotEqual(t, bgtask.StatusSucceeded, status)
	assert.NotEqual(t, bgtask.StatusFailed, status)
	assert.NotEqual(t, bgtask.StatusStopped, status)
	assert.NotEqual(t, bgtask.StatusInterrupted, status)
	assert.Equal(t, bgtask.StatusRunning, resolvedChildStatus(nativeItem{Status: "inProgress", ControlStatus: "futureControl"}))
}

func TestReadySubagentResultsSettleTheCurrentTask(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, result, status string
		want                 bgtask.Status
	}{
		{name: "successful", result: `{"summary":"Done","evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusSucceeded},
		{name: "empty successful", result: `{"summary":"","evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusSucceeded},
		{name: "cancelled", result: `{"summary":"","errorKind":"generated_child_cancelled","evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusStopped},
		{name: "failed", result: `{"summary":"Failed","errorKind":"native_failure","evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusFailed},
		{name: "empty failure kind", result: `{"summary":"Failed","errorKind":"","evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusFailed},
		{name: "explicit item failure", status: "failed", result: `{"summary":"Done","evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusFailed},
		{name: "null error kind", result: `{"summary":"Done","errorKind":null,"evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusRunning},
		{name: "null reference", result: `{"summary":"Done","evidenceRefs":[null],"artifactRefs":[]}`, want: bgtask.StatusRunning},
		{name: "null result", result: `null`, want: bgtask.StatusRunning},
		{name: "scalar result", result: `"Done"`, want: bgtask.StatusRunning},
		{name: "missing summary", result: `{"evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusRunning},
		{name: "missing evidence", result: `{"summary":"Done","artifactRefs":[]}`, want: bgtask.StatusRunning},
		{name: "null evidence", result: `{"summary":"Done","evidenceRefs":null,"artifactRefs":[]}`, want: bgtask.StatusRunning},
		{name: "missing artifacts", result: `{"summary":"Done","evidenceRefs":[]}`, want: bgtask.StatusRunning},
		{name: "invalid error kind", result: `{"summary":"Done","errorKind":0,"evidenceRefs":[],"artifactRefs":[]}`, want: bgtask.StatusRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status := tc.status
			if status == "" {
				status = "inProgress"
			}
			var item nativeItem
			require.NoError(t, json.Unmarshal([]byte(`{"kind":"subagent","status":"`+status+`","controlStatus":"resultReady","result":`+tc.result+`}`), &item))
			assert.Equal(t, tc.want, resolvedChildStatus(item))
		})
	}
}

func TestNonFinalChildStatusCannotInventAFinalControlOutcome(t *testing.T) {
	t.Parallel()
	for _, control := range []string{"accepted", "starting", "running", "resultReady", "closing", "closed", "recoveryPending", "manualReconciliation", "futureControl"} {
		t.Run(control, func(t *testing.T) {
			status := resolvedChildStatus(nativeItem{Status: contracts.MuseItemStatusInProgress, ControlStatus: control})
			assert.False(t, status.IsFinished())
			assert.NotEqual(t, bgtask.StatusSucceeded, status)
			assert.NotEqual(t, bgtask.StatusFailed, status)
			assert.NotEqual(t, bgtask.StatusStopped, status)
			assert.NotEqual(t, bgtask.StatusInterrupted, status)
		})
	}
}

func TestUnknownFinalBackgroundShellStatusEndsWithoutAnOutcome(t *testing.T) {
	a, sink := testAgent(t)
	item := resultItem("background-shell", "call", "origin")
	item.Item.Background = true
	item.Item.Revision = 1
	item.Item.Status = "futureFinal"
	feed(t, a, contracts.MuseMethodItemCompleted, item)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.KindShell, rows[0].Kind)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, rows[0].Status)
	assert.False(t, rows[0].Status.IsWorking())
}

func TestChildCommandsKeepUncertainIdentityUntilConfirmed(t *testing.T) {
	for _, operation := range []string{"send", "steer", "interrupt"} {
		t.Run(operation, func(t *testing.T) {
			a, _ := testAgent(t)
			child := &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child", childViewPrefix: childViewPrefixReady}
			a.sessions["child"] = child
			// Keep this unit test focused on command admission rather than asynchronous reads.
			a.viewWork.CloseAndWait()
			if operation == "steer" {
				child.turnID = "active"
			}
			var ids []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				assert.Equal(t, "session", params["sessionId"])
				assert.Equal(t, "native-child", params["subagentId"])
				ids = append(ids, params["commandId"].(string))
				if len(ids) == 1 {
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"commandId":"` + ids[len(ids)-1] + `","status":"accepted"}`)}
			}})
			invoke := func() error {
				switch operation {
				case "steer":
					return a.SteerChildInput("key", "note", nil)
				case "interrupt":
					return a.InterruptChild("key", agent.StopContext{})
				default:
					return a.SendChildInput("key", "note", nil)
				}
			}
			require.ErrorIs(t, invoke(), agent.ErrDeliveryUncertain)
			require.NoError(t, invoke())
			require.NoError(t, invoke())
			require.Len(t, ids, 3)
			assert.Equal(t, ids[0], ids[1])
			assert.NotEqual(t, ids[1], ids[2])
		})
	}
}

func TestChildCommandsRejectRetiredRoutesBeforeWriting(t *testing.T) {
	for _, target := range []string{"child", "parent"} {
		t.Run(target, func(t *testing.T) {
			a, _ := testAgent(t)
			child := &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child", turnID: "active"}
			a.sessions["child"] = child
			if target == "child" {
				child.retired = true
			} else {
				a.sessions["session"].retired = true
			}
			writes := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
				writes++
				var p map[string]any
				require.NoError(t, json.Unmarshal(raw, &p))
				data, _ := json.Marshal(map[string]any{"commandId": p["commandId"], "status": "accepted"})
				return agenttest.RPCReply{Result: data}
			}})
			assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("key"))
			require.ErrorIs(t, a.SteerChildInput("key", "note", nil), agent.ErrChildRouteNotReady)
			require.ErrorIs(t, a.InterruptChild("key", agent.StopContext{}), agent.ErrChildRouteNotReady)
			assert.Zero(t, writes)
		})
	}
}

func TestChildInputValidatesBeforeNativeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, text, turn string
		steer            bool
		attachment       *leapmuxv1.Attachment
		want             error
		wantText         string
	}{
		{name: "empty", text: " \t\n", wantText: "empty"},
		{name: "busy", text: "note", turn: "active", want: agent.ErrAgentBusy},
		{name: "idle steer", text: "note", steer: true, want: agent.ErrNoActiveTurn},
		{name: "image", text: "note", attachment: &leapmuxv1.Attachment{Filename: "image.png", MimeType: "image/png", Data: []byte{1}}, wantText: "text attachments only"},
		{name: "binary", text: "note", attachment: &leapmuxv1.Attachment{Filename: "data.bin", MimeType: "application/octet-stream", Data: []byte{255}}, wantText: "text attachments only"},
		{name: "text attachment", attachment: &leapmuxv1.Attachment{Filename: "note.txt", MimeType: "text/plain", Data: []byte("real child attachment")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := testAgent(t)
			a.sessions["child"] = &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child", turnID: tc.turn, childViewPrefix: childViewPrefixReady}
			a.viewWork.CloseAndWait()
			writes := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				writes++
				assert.Equal(t, methodChildFollowup, method)
				var p map[string]any
				require.NoError(t, json.Unmarshal(raw, &p))
				assert.Contains(t, p["body"], "real child attachment")
				assert.Contains(t, p["body"], "note.txt")
				data, _ := json.Marshal(map[string]any{"commandId": p["commandId"], "status": "accepted"})
				return agenttest.RPCReply{Result: data}
			}})
			var attachments []*leapmuxv1.Attachment
			if tc.attachment != nil {
				attachments = append(attachments, tc.attachment)
			}
			var err error
			if tc.steer {
				err = a.SteerChildInput("key", tc.text, attachments)
			} else {
				err = a.SendChildInput("key", tc.text, attachments)
			}
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else if tc.wantText != "" {
				require.ErrorContains(t, err, tc.wantText)
			} else {
				require.NoError(t, err)
			}
			if tc.want != nil || tc.wantText != "" {
				assert.Zero(t, writes)
			} else {
				assert.Equal(t, 1, writes)
			}
		})
	}
}

func TestChildCommandIdentityChangesWithInputAndConfirmedRefusal(t *testing.T) {
	a, _ := testAgent(t)
	a.sessions["child"] = &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child", childViewPrefix: childViewPrefixReady}
	a.viewWork.CloseAndWait()
	var ids []string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var p map[string]any
		require.NoError(t, json.Unmarshal(raw, &p))
		ids = append(ids, p["commandId"].(string))
		if len(ids) <= 2 {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"invalid input"}`)}
	}})
	require.ErrorIs(t, a.SendChildInput("key", "one", nil), agent.ErrDeliveryUncertain)
	require.ErrorIs(t, a.SendChildInput("key", "two", nil), agent.ErrDeliveryUncertain)
	require.Error(t, a.SendChildInput("key", "two", nil))
	require.Error(t, a.SendChildInput("key", "two", nil))
	require.Len(t, ids, 4)
	assert.NotEqual(t, ids[0], ids[1])
	assert.Equal(t, ids[1], ids[2])
	assert.NotEqual(t, ids[2], ids[3])
}

func TestChildViewRefreshBuffersLiveOverlapBeforeItsStoredPrefix(t *testing.T) {
	a, root := testAgent(t)
	state, sink := nativeHistoryChild(t, a, root)
	prefix := childHistoryItemEvent(t, "prefix", json.RawMessage(`{"itemId":"first","revision":1,"kind":"agentMessage","status":"completed","text":"Stored prefix"}`), 1)
	overlap := childHistoryItemEvent(t, "overlap", json.RawMessage(`{"itemId":"second","revision":1,"kind":"agentMessage","status":"completed","text":"Live overlap"}`), 2)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"overlap","history":{"mode":"none","items":null,"snapshot":null}}`)}
		case methodViewPage:
			reply, err := json.Marshal(map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": nil})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: reply}
		case methodViewSubscribe:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.Equal(t, "prefix", params["after"])
			a.HandleOutput(overlap)
			a.HandleOutput(overlap)
			assert.Empty(t, sink.Messages(), "live frames wait for the accepted prefix")
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"overlap"}`)}
		default:
			t.Errorf("unexpected native method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, []byte(prefix), messages[0].Content)
	assert.Equal(t, []byte(overlap), messages[1].Content)
	require.NotNil(t, state.acceptedViewCursor)
	assert.Equal(t, "overlap", *state.acceptedViewCursor)
}

func TestChildViewRefusesAnEmptyUnprovedPrefixAndRetriesOnlyWhenRequested(t *testing.T) {
	a, root := testAgent(t)
	state, sink := nativeHistoryChild(t, a, root)
	reads, pages, subscriptions := 0, 0, 0
	first := childHistoryItemEvent(t, "first", json.RawMessage(`{"itemId":"first","revision":1,"kind":"agentMessage","status":"completed","text":"Earlier child text"}`), 1)
	progress := childHistoryItemEvent(t, "progress", json.RawMessage(`{"itemId":"progress","revision":1,"kind":"agentMessage","status":"completed","text":"Native child progress"}`), 2)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			reads++
			if reads == 1 {
				a.HandleOutput(progress)
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"progress","history":{"mode":"none","items":null,"snapshot":null}}`)}
		case methodViewPage:
			pages++
			if pages == 1 {
				return agenttest.RPCReply{Result: json.RawMessage(`{"events":[],"nextCursor":null}`)}
			}
			reply, err := json.Marshal(map[string]any{"events": []json.RawMessage{first}, "nextCursor": nil})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: reply}
		case methodViewSubscribe:
			subscriptions++
			a.HandleOutput(progress)
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"progress"}`)}
		default:
			t.Errorf("unexpected native method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	state.childViewRequested = true
	a.syncView("child")
	assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
	assert.Empty(t, sink.Messages())
	assert.Equal(t, 1, pages)
	assert.Zero(t, subscriptions)
	a.HandleOutput(progress)
	assert.Equal(t, 1, pages, "unrelated native output cannot restart failed admission")
	require.Len(t, root.Messages(), 1, "exact native evidence is retained once on the root")
	assert.Equal(t, []byte(progress), root.Messages()[0].Content)
	require.Len(t, root.LeapMuxNotifications(), 1)
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, 2, reads)
	assert.Equal(t, 2, pages)
	assert.Equal(t, 1, subscriptions)
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, []byte(first), sink.Messages()[0].Content)
	assert.Equal(t, []byte(progress), sink.Messages()[1].Content)
	assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
}

func TestChildViewAcceptsANonrenderedPrefixBeforeItsSubscriptionSuffix(t *testing.T) {
	a, root := testAgent(t)
	state, sink := nativeHistoryChild(t, a, root)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"progress","history":{"mode":"none","items":null,"snapshot":null}}`)}
		case methodViewPage:
			return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"session/branchChanged","params":{"sessionId":"child","viewCursor":"before"}}],"nextCursor":null}`)}
		case methodViewSubscribe:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.Equal(t, "before", params["after"])
			a.HandleOutput(provedChildFrame(t, a, state, []byte(mustMuseChoiceJSON(t, map[string]any{"method": methodTurnStarted, "params": map[string]any{"sessionId": "child", "viewCursor": "started", "turnId": "native-child-turn"}}))))
			a.HandleOutput(childHistoryItemEvent(t, "progress", json.RawMessage(`{"itemId":"text","revision":1,"kind":"agentMessage","status":"completed","text":"Native child progress"}`), 2))
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"progress"}`)}
		default:
			t.Errorf("unexpected native method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, "native-child-turn", state.turnID)
	assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "Native child progress")
}

func TestChildViewRefreshContinuesAfterItsRestoredPrefix(t *testing.T) {
	a, root := testAgent(t)
	state, sink := nativeHistoryChild(t, a, root)
	reads, pages, subscriptions := 0, 0, 0
	refreshed := make(chan struct{})
	firstSubscribed := make(chan struct{})
	first := childHistoryItemEvent(t, "first", json.RawMessage(`{"itemId":"first-item","revision":1,"kind":"agentMessage","status":"completed","text":"First child result"}`), 1)
	second := childHistoryItemEvent(t, "second", json.RawMessage(`{"itemId":"second-item","revision":1,"kind":"agentMessage","status":"completed","text":"Second child result"}`), 2)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			reads++
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"first","history":{"mode":"none","items":null,"snapshot":null}}`)}
		case methodViewPage:
			pages++
			reply, err := json.Marshal(map[string]any{"events": []json.RawMessage{first}, "nextCursor": nil})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: reply}
		case methodViewSubscribe:
			subscriptions++
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.Equal(t, "first", params["after"])
			head := "first"
			if subscriptions == 1 {
				close(firstSubscribed)
			}
			if subscriptions == 2 {
				close(refreshed)
				head = "second"
				a.HandleOutput(second)
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"` + head + `"}`)}
		default:
			t.Errorf("unexpected native method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	state.childViewRequested = true
	a.syncView("child")
	a.requestChildView("child")
	ctx := testutil.DeadlineContext(t)
	select {
	case <-refreshed:
	case <-ctx.Done():
		t.Fatal("the requested child refresh did not subscribe")
	}
	a.viewWork.CloseAndWait()
	require.Equal(t, childViewPrefixReady, state.childViewPrefix)
	assert.Equal(t, 2, reads, "each subscription reads its coherent native activity head")
	assert.Equal(t, 1, pages)
	assert.Equal(t, 2, subscriptions)
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, []byte(first), sink.Messages()[0].Content)
	assert.Equal(t, []byte(second), sink.Messages()[1].Content)
	require.NotNil(t, state.acceptedViewCursor)
	assert.Equal(t, "second", *state.acceptedViewCursor)
}

func TestChildViewGapKeepsTheUnresolvedInitialPrefix(t *testing.T) {
	a, root := testAgent(t)
	state, sink := nativeHistoryChild(t, a, root)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"head","history":{"mode":"none","items":null,"snapshot":null}}`)}
		case methodViewPage:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.NotContains(t, params, "cursor", "a later gap cannot discard an unread initial prefix")
			frame := provedChildFrame(t, a, state, []byte(`{"method":"turn/started","params":{"sessionId":"child","viewCursor":"head","turnId":"child-turn"}}`))
			result, err := json.Marshal(map[string]any{"events": []json.RawMessage{frame}, "nextCursor": nil})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: result}
		case methodViewSubscribe:
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"head"}`)}
		default:
			t.Errorf("unexpected native method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	feed(t, a, methodViewGap, map[string]any{"sessionId": "child", "after": "observed", "next": "head"})
	a.viewWork.CloseAndWait()
	assert.Equal(t, "child-turn", state.turnID)
	assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
	assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
}
