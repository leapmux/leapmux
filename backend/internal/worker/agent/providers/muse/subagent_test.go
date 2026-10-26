package muse

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildRestorationReadsEveryForwardPage(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["child"] = &sessionState{sink: a.sink, childID: "child-agent", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child")}
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		if method == methodViewSubscribe {
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"head"}`)}
		}
		require.Equal(t, methodViewPage, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "forward", params["direction"])
		pages++
		cursor := any("next")
		if pages == 2 {
			cursor = nil
			assert.Equal(t, "next", params["cursor"])
		}
		response, err := json.Marshal(map[string]any{"events": []any{map[string]any{"method": "item/completed", "params": map[string]any{"sessionId": "child", "viewCursor": "cursor-" + jsonNumber(pages), "item": map[string]any{"itemId": "text-" + jsonNumber(pages), "revision": 1, "kind": "agentMessage", "status": "completed", "text": "page-" + jsonNumber(pages)}}}}, "nextCursor": cursor})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: response}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, 2, pages)
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), "page-1")
	assert.Contains(t, string(sink.Messages()[1].Content), "page-2")
}

func TestCompletedChildRestorationReadsStoredHistoryWithoutALiveSubscription(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["child"] = &sessionState{sink: a.sink, childID: "child-agent", parentSessionID: "session", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child")}
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodViewSubscribe {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"session child not found","data":{"kind":"sessionNotFound"}}`)}
		}
		require.Equal(t, methodViewPage, method)
		pages++
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"item/completed","params":{"sessionId":"child","viewCursor":"child-cursor","item":{"itemId":"reply","kind":"agentMessage","revision":1,"status":"completed","text":"Stored child reply"}}}],"nextCursor":null}`)}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, 1, pages)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "Stored child reply")
}

func TestNativeChildUserContextDoesNotDuplicateWorkerOwnedInputs(t *testing.T) {
	a, sink := testAgent(t)
	state := &sessionState{sink: a.sink, childID: "child-agent", parentSessionID: "session", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child")}
	a.sessions["child"] = state
	for index, text := range []string{"Actual child assignment", "Human followup"} {
		raw, err := json.Marshal(map[string]string{"content": text})
		require.NoError(t, err)
		require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: raw}, agent.SpanInfo{}))
		turn := "child-turn-" + jsonNumber(index)
		feed(t, a, methodTurnStarted, map[string]any{"sessionId": "child", "turnId": turn})
		feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "child", Item: nativeItem{ID: "native-user-" + jsonNumber(index), Kind: contracts.MuseItemKindUserMessage, TurnID: &turn, Revision: 1, Status: "completed", Text: "Generated native context that wraps the assigned input"}})
		feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "child", Item: nativeItem{ID: "native-answer-" + jsonNumber(index), Kind: contracts.MuseItemKindAgentMessage, TurnID: &turn, Revision: 1, Status: "completed", Text: "The child answered"}})
		feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "child", "turnId": turn, "terminal": "completed"})
	}
	users, answers := 0, 0
	for _, message := range sink.Messages() {
		if message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			users++
		}
		if message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && string(message.Content) != "" {
			var frame struct {
				Params struct {
					Item struct {
						Kind string `json:"kind"`
					} `json:"item"`
				} `json:"params"`
			}
			require.NoError(t, json.Unmarshal(message.Content, &frame))
			if frame.Params.Item.Kind == contracts.MuseItemKindAgentMessage {
				answers++
			}
		}
	}
	assert.Equal(t, 2, users, "spawn metadata and queued input own the user rows")
	assert.Equal(t, 2, answers, "every agent-side child answer remains in its native session")
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

func TestWorkflowAndAddressableChildShareOneRegistryRow(t *testing.T) {
	a, sink := testAgent(t)
	parent := a.sessions["session"]
	workflow := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Kind: contracts.MuseItemKindWorkflow, Status: "inProgress", WorkflowRunID: "run", EntryID: "Probe run", Children: json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"started"}]`)}}
	require.NoError(t, a.observeChildOrTask(workflow, parent, nativeItem{}))
	key := bgtask.NormalizeRowKey("session:child:child-a")
	a.sessions["native-child"] = &sessionState{sink: a.sink.ChildSink("child-agent"), childKey: key, childID: "child-agent", parentSessionID: "session", subagentID: "child-a"}
	var params itemParams
	require.NoError(t, json.Unmarshal([]byte(`{"sessionId":"session","item":{"itemId":"native-child-item","kind":"subagent","status":"inProgress","controlStatus":"resultReady","subagentId":"child-a","childSessionId":"native-child","objective":"Actual assignment","workflowRunId":"run","result":{"summary":"Done","evidenceRefs":[],"artifactRefs":[]}}}`), &params))
	require.NoError(t, a.observeChildOrTask(params, parent, nativeItem{}))
	workflow.Item.Status = "completed"
	workflow.Item.Children = json.RawMessage(`[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"completed"}]`)
	require.NoError(t, a.observeChildOrTask(workflow, parent, nativeItem{}))
	require.NoError(t, a.observeChildOrTask(params, parent, nativeItem{}))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 2, "the workflow fold and native subagent identify the same child")
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, "child-agent", row.ChildAgentID)
	assert.Equal(t, "Actual assignment", row.Title)
	assert.Equal(t, "Probe run", row.GroupLabel)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
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

func TestWorkflowFoldProjectsItsRunAndChildrenWithoutASession(t *testing.T) {
	a, sink := testAgent(t)
	parent := &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("parent")}
	a.sessions["parent"] = parent
	feedItem := func(status string, children string) {
		require.NoError(t, a.observeChildOrTask(itemParams{SessionID: "parent", Item: nativeItem{
			ID: "workflow-item", Kind: contracts.MuseItemKindWorkflow, Status: status,
			WorkflowRunID: "workflow-run-1", EntryID: "Probe run", Children: json.RawMessage(children),
		}}, parent, nativeItem{}))
	}
	feedItem(contracts.MuseItemStatusInProgress, `[{"childId":"child-a","attempt":1,"status":"started"},{"childId":"child-b","attempt":1,"status":"scheduled"}]`)
	feedItem(contracts.MuseItemStatusCompleted, `[{"childId":"child-a","attempt":1,"status":"terminal","terminal":"completed"},{"childId":"child-b","attempt":1,"status":"terminal","terminal":"failed","failureReason":"native"}]`)
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 3)
	byKey := make(map[string]bgtask.Item, len(tasks))
	for _, task := range tasks {
		byKey[task.RowKey] = task
	}
	run := bgtask.NormalizeRowKey("parent:workflow:workflow-run-1")
	childA := bgtask.NormalizeRowKey("parent:child:child-a")
	childB := bgtask.NormalizeRowKey("parent:child:child-b")
	runRow, ok := byKey[run]
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, runRow.Kind)
	assert.Equal(t, "Probe run", runRow.Title)
	assert.Equal(t, bgtask.StatusSucceeded, runRow.Status)
	assert.Equal(t, run, runRow.GroupKey)
	for key, expected := range map[string]struct {
		status bgtask.Status
		title  string
	}{
		childA: {status: bgtask.StatusSucceeded, title: "Child child-a"},
		childB: {status: bgtask.StatusFailed, title: "Child child-b"},
	} {
		row, ok := byKey[key]
		require.True(t, ok, key)
		assert.Equal(t, expected.status, row.Status, key)
		assert.Equal(t, bgtask.KindSubagent, row.Kind, key)
		assert.Equal(t, expected.title, row.Title, key)
		assert.Equal(t, run, row.GroupKey, key)
		assert.Equal(t, "Probe run", row.GroupLabel, key)
		assert.Empty(t, row.ChildAgentID, "a fold without an addressable session opens no transcript", key)
	}
}

func TestChildCommandsKeepUncertainIdentityUntilConfirmed(t *testing.T) {
	for _, operation := range []string{"send", "steer", "interrupt"} {
		t.Run(operation, func(t *testing.T) {
			a, _ := testAgent(t)
			child := &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child"}
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
			a.sessions["child"] = &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child", turnID: tc.turn}
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
	a.sessions["child"] = &sessionState{childKey: "key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child"}
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
	a, sink := testAgent(t)
	a.sessions["child"] = &sessionState{sink: a.sink, childID: "child-agent", parentSessionID: "session", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child")}
	prefix := strings.ReplaceAll(string(completedViewEvent(t, "prefix", "first", "Stored prefix")), `"sessionId":"session"`, `"sessionId":"child"`)
	overlap := strings.ReplaceAll(string(completedViewEvent(t, "overlap", "second", "Live overlap")), `"sessionId":"session"`, `"sessionId":"child"`)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodViewSubscribe {
			a.HandleOutput([]byte(overlap))
			assert.Empty(t, sink.Messages(), "the live frame waits for its stored prefix")
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"overlap"}`)}
		}
		require.Equal(t, methodViewPage, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[` + prefix + `,` + overlap + `],"nextCursor":null}`)}
	}})
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, prefix, string(messages[0].Content))
	assert.Equal(t, overlap, string(messages[1].Content))
	assert.Equal(t, "overlap", a.sessions["child"].lastViewCursor)
}

func TestChildViewReadsItsPrefixAfterAnEarlyIncompleteNativePage(t *testing.T) {
	for name, early := range map[string]string{
		"empty":   `{"events":[],"nextCursor":null}`,
		"partial": `{"events":[{"method":"session/branchChanged","params":{"sessionId":"child","viewCursor":"before"}}],"nextCursor":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			a, sink := testAgent(t)
			state := &sessionState{sink: a.sink, childID: "child-agent", parentSessionID: "session", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child"), childViewRequested: true}
			a.sessions["child"] = state
			pages := 0
			progress := strings.ReplaceAll(string(completedViewEvent(t, "progress", "text", "Native child progress")), `"sessionId":"session"`, `"sessionId":"child"`)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				if method == methodViewSubscribe {
					head := "head"
					if pages > 0 {
						head = "progress"
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"` + head + `"}`)}
				}
				require.Equal(t, methodViewPage, method)
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				assert.NotContains(t, params, "cursor", "a live frame cannot replace the unread history prefix")
				pages++
				if pages == 1 {
					return agenttest.RPCReply{Result: json.RawMessage(early)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"turn/started","params":{"sessionId":"child","turnId":"native-child-turn","viewCursor":"started"}},` + progress + `],"nextCursor":null}`)}
			}})
			a.syncView("child")
			require.Empty(t, state.turnID)
			// The native view can expose its head before its stored page. A later real
			// notification provides the event that makes the stored prefix readable.
			a.HandleOutput([]byte(progress))
			a.viewWork.CloseAndWait()
			assert.Equal(t, 2, pages)
			assert.Equal(t, "native-child-turn", state.turnID)
			assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
			require.Len(t, sink.Messages(), 1, "live progress and its paged overlap persist once")
			assert.Equal(t, progress, string(sink.Messages()[0].Content))
		})
	}
}

func TestChildViewRefreshContinuesAfterItsRestoredPrefix(t *testing.T) {
	a, sink := testAgent(t)
	state := &sessionState{sink: a.sink, childID: "child-agent", parentSessionID: "session", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child"), childViewRequested: true}
	a.sessions["child"] = state
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		if method == methodViewSubscribe {
			head := "first"
			if pages > 0 {
				head = "second"
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"` + head + `"}`)}
		}
		require.Equal(t, methodViewPage, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		pages++
		cursor, itemID, text := "first", "first-item", "First child result"
		if pages == 1 {
			assert.NotContains(t, params, "cursor")
		} else {
			require.Equal(t, 2, pages)
			assert.Equal(t, "first", params["cursor"])
			cursor, itemID, text = "second", "second-item", "Second child result"
		}
		event := strings.ReplaceAll(string(completedViewEvent(t, cursor, itemID, text)), `"sessionId":"session"`, `"sessionId":"child"`)
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[` + event + `],"nextCursor":null}`)}
	}})
	a.syncView("child")
	assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
	a.requestChildView("child")
	a.viewWork.CloseAndWait()
	assert.Equal(t, 2, pages)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "First child result")
	assert.Contains(t, string(messages[1].Content), "Second child result")
	assert.Equal(t, "second", state.lastViewCursor)
}

func TestChildViewGapKeepsTheUnresolvedInitialPrefix(t *testing.T) {
	a, sink := testAgent(t)
	state := &sessionState{sink: a.sink, childID: "child-agent", parentSessionID: "session", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child"), childViewPrefix: childViewPrefixPending, lastViewCursor: "observed"}
	a.sessions["child"] = state
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.NotContains(t, params, "cursor", "a gap after a live frame cannot discard the unresolved initial prefix")
		if params["cursor"] != nil {
			return agenttest.RPCReply{Result: json.RawMessage(`{"events":[],"nextCursor":null}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"turn/started","params":{"sessionId":"child","viewCursor":"head","turnId":"child-turn"}}],"nextCursor":null}`)}
	}})
	feed(t, a, methodViewGap, map[string]any{"sessionId": "child", "after": "observed", "next": "head"})
	a.viewWork.CloseAndWait()
	assert.Equal(t, "child-turn", state.turnID)
	assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
	assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
}
