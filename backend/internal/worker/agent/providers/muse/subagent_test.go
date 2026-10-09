package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
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
	a.subscribeChild("child")
	assert.Equal(t, 2, pages)
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), "page-1")
	assert.Contains(t, string(sink.Messages()[1].Content), "page-2")
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
