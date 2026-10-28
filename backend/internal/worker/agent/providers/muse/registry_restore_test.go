package muse

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestColdRegistryRestoreRejectsAnAttemptRegressionInEitherArrayOrder(t *testing.T) {
	first := `{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"child","attempt":2,"status":"started"}]}`
	last := `{"itemId":"workflow","revision":2,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"child","attempt":1,"status":"terminal","terminal":"failed"}]}`
	for _, objects := range []string{first + "," + last, last + "," + first} {
		t.Run(objects, func(t *testing.T) {
			a, sink := testAgent(t)
			require.ErrorContains(t, a.restoreChildRoutes("session", json.RawMessage(`{"mode":"inline","items":[`+objects+`]}`), "head"), "attempt moved backward")
			assert.Empty(t, sink.BackgroundTasks())
			assert.Empty(t, a.sessions["session"].items)
		})
	}
}

func TestMuseResumeRequiresAnExplicitNativeHeadCursor(t *testing.T) {
	for _, field := range []string{"", `,"viewCursor":null`, `,"viewCursor":0`} {
		t.Run(fmt.Sprint(field), func(t *testing.T) {
			a, _ := testAgent(t)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodSessionResume, method)
				return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"history":{"mode":"inline","items":[]}` + field + `}`)}
			}})
			require.Error(t, a.openSession("session", a.APITimeout()))
		})
	}
}

func TestRestoredRegistryHeadKeepsCanonicalItemsAndTheFirstNativeFrame(t *testing.T) {
	a, sink := testAgent(t)
	item := json.RawMessage(`{"itemId":"workflow","revision":2,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"child","attempt":0,"status":"started"}],"futureField":{"value":42}}`)
	require.NoError(t, a.restoreChildRoutes("session", json.RawMessage(`{"mode":"inline","items":[`+string(item)+`]}`), "head"))
	assert.Empty(t, sink.Messages(), "a folded item is metadata, not a native notification")
	canonical := a.sessions["session"].items["workflow"]
	require.NotNil(t, canonical)
	assert.JSONEq(t, string(item), string(canonical.nativeItemRaw))
	assert.Empty(t, canonical.raw)
	raw := feed(t, a, contracts.MuseMethodItemUpdated, map[string]any{"sessionId": "session", "item": item})
	require.Len(t, sink.Messages(), 0, "an active workflow waits for its final native frame")
	assert.Equal(t, raw, canonical.raw)
	feed(t, a, contracts.MuseMethodItemUpdated, map[string]any{"sessionId": "session", "item": item})
	assert.Empty(t, sink.Messages())
	final := json.RawMessage(`{"itemId":"workflow","revision":3,"kind":"workflow","workflowRunId":"run","status":"completed","children":[{"childId":"child","attempt":0,"status":"terminal","terminal":"completed"}],"futureField":{"value":42}}`)
	frame := feed(t, a, contracts.MuseMethodItemCompleted, map[string]any{"sessionId": "session", "item": final})
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, frame, sink.Messages()[0].Content)
}

func TestRestoredActiveHeadReopensRowsOnceEvenAfterALiveProjection(t *testing.T) {
	a, sink := testAgent(t)
	for _, key := range []string{museWorkflowRowKey("session", "run"), museChildRowKey("session", "child"), museBackgroundTaskRowKey("session", "shell")} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, Status: bgtask.StatusStopped}))
	}
	workflow := json.RawMessage(`{"itemId":"workflow","revision":2,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"child","attempt":0,"status":"started"}]}`)
	shell := json.RawMessage(`{"itemId":"shell","revision":1,"kind":"toolCall","tool":"shell","background":true,"status":"inProgress"}`)
	feed(t, a, contracts.MuseMethodItemUpdated, map[string]any{"sessionId": "session", "item": workflow})
	row, _ := sink.BackgroundTask(museChildRowKey("session", "child"))
	require.Equal(t, bgtask.StatusStopped, row.Status, "an ordinary first live observation does not prove a retry")
	history := json.RawMessage(`{"mode":"inline","items":[` + string(workflow) + `,` + string(shell) + `]}`)
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	require.Len(t, sink.RevivedTasks(), 3)
	for _, key := range sink.RevivedTasks() {
		row, ok := sink.BackgroundTask(key)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
		assert.True(t, row.EndedAt.IsZero())
	}
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	assert.Len(t, sink.RevivedTasks(), 3)
}

func TestRetirementClosesEveryCanonicalRegistryRow(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage(`[{"childId":"child","attempt":1,"status":"started"}]`)}})
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "shell", Revision: 1, Kind: contracts.MuseItemKindToolCall, Tool: "shell", Background: true, Status: "inProgress"}})
	require.Len(t, sink.BackgroundTasks(), 3)
	a.retireHost(agent.MessageCompletionInterrupted)
	for _, row := range sink.BackgroundTasks() {
		assert.Equal(t, bgtask.StatusStopped, row.Status, row.RowKey)
	}
}

func TestRestoredFoldOwnsChildOutcomeIndependentlyOfItemOrder(t *testing.T) {
	workflow := json.RawMessage(`{"itemId":"workflow","revision":3,"kind":"workflow","workflowRunId":"run","entryId":"Native workflow","status":"completed","children":[{"childId":"child","attempt":2,"status":"terminal","terminal":"failed"}]}`)
	child := json.RawMessage(`{"itemId":"addressable","revision":1,"kind":"subagent","subagentId":"child","childSessionId":"native-child","status":"completed","objective":"Native assignment"}`)
	for _, objects := range [][]json.RawMessage{{workflow, child}, {child, workflow}} {
		t.Run(string(objects[0]), func(t *testing.T) {
			a, sink := testAgent(t)
			a.viewWork.CloseAndWait()
			history, err := json.Marshal(map[string]any{"mode": "inline", "items": objects})
			require.NoError(t, err)
			require.NoError(t, a.restoreChildRoutes("session", history, "head"))
			row, ok := sink.BackgroundTask(museChildRowKey("session", "child"))
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusFailed, row.Status)
			assert.Equal(t, "Native assignment", row.Title)
			assert.Equal(t, "Native workflow", row.GroupLabel)
			assert.NotEmpty(t, row.ChildAgentID)
			assert.Empty(t, sink.RevivedTasks())
		})
	}
}

func TestRegistryHeadPreservesANewerLiveRevision(t *testing.T) {
	a, sink := testAgent(t)
	current := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 3, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage(`[{"childId":"child","attempt":2,"status":"started"}]`)}}
	feed(t, a, contracts.MuseMethodItemUpdated, current)
	require.NoError(t, a.restoreChildRoutes("session", json.RawMessage(`{"mode":"inline","items":[{"itemId":"workflow","revision":2,"kind":"workflow","workflowRunId":"run","status":"completed","children":[{"childId":"child","attempt":1,"status":"terminal","terminal":"failed"}]}]}`), "older-head"))
	row, ok := sink.BackgroundTask(museChildRowKey("session", "child"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, int64(3), a.sessions["session"].items["workflow"].params.Item.Revision)
	assert.Empty(t, sink.RevivedTasks())
}

func TestRegistryHeadRejectsContradictoryUnknownNativeFields(t *testing.T) {
	a, sink := testAgent(t)
	history := json.RawMessage(`{"mode":"inline","items":[{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","futureField":1}]}`)
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	before := sink.BackgroundTasks()
	require.ErrorContains(t, a.restoreChildRoutes("session", json.RawMessage(`{"mode":"inline","items":[{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","futureField":2}]}`), "head"), "conflicts")
	assert.Equal(t, before, sink.BackgroundTasks())
	assert.Empty(t, sink.Messages())
}

func TestRegistryHeadRetainsReconciliationAfterARefusal(t *testing.T) {
	a, sink := testAgent(t)
	key := museChildRowKey("session", "child")
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, Status: bgtask.StatusStopped}))
	failure := &workflowRegistryFailureSink{Sink: sink, reviveKey: key, remaining: 1}
	a.sink = agent.NewProviderServices(failure)
	history := json.RawMessage(`{"mode":"inline","items":[{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"child","attempt":0,"status":"scheduled"}]}]}`)
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	require.True(t, agent.OutputQueueFor(a.sink).HasPending())
	assert.Equal(t, int64(1), a.sessions["session"].items["workflow"].registry.registryHeadRevision)
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	require.NoError(t, agent.OutputQueueFor(a.sink).Flush())
	row, ok := sink.BackgroundTask(key)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusPending, row.Status)
	assert.True(t, row.EndedAt.IsZero())
	assert.Equal(t, []string{key}, sink.RevivedTasks())
	assert.Zero(t, a.sessions["session"].items["workflow"].registry.registryHeadRevision)
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	assert.Equal(t, []string{key}, sink.RevivedTasks())
}

func TestFinalNativeFrameRetriesRegistryProjectionWithoutRepeatingItsTranscript(t *testing.T) {
	a, sink := testAgent(t)
	failure := &workflowRegistryFailureSink{Sink: sink, upsertKey: museWorkflowRowKey("session", "run"), remaining: 1}
	a.sink = agent.NewProviderServices(failure)
	item := itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "completed", Children: json.RawMessage(`[{"childId":"child","attempt":1,"status":"terminal","terminal":"failed"}]`)}}
	raw := feed(t, a, contracts.MuseMethodItemCompleted, item)
	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, sink.BackgroundTasks())
	a.HandleOutput(raw)
	require.NoError(t, agent.OutputQueueFor(a.sink).Flush())
	require.Len(t, sink.Messages(), 1)
	row, ok := sink.BackgroundTask(museChildRowKey("session", "child"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Equal(t, int64(1), a.sessions["session"].items["workflow"].registry.registryRevision)
}

func TestRegistryRestoreRefusesAPagedPrefixThatDoesNotReachTheHead(t *testing.T) {
	a, sink := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"events":[{"method":"item/updated","params":{"sessionId":"session","viewCursor":"earlier","item":{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[]}}}],"nextCursor":null}`)}
	}})
	require.ErrorContains(t, a.restoreChildRoutes("session", json.RawMessage(`{"mode":"none"}`), "unread-head"), "did not reach its head")
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, a.sessions["session"].items)
}

func TestMuseResumeAcceptsAnExplicitEmptyNativeHead(t *testing.T) {
	a, _ := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionResume, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"viewCursor":"","history":{"mode":"inline","items":[]}}`)}
	}})
	require.NoError(t, a.openSession("session", a.APITimeout()))
}

func TestRetirementPreservesFinishedOutcomesAndClosesRestoredRowsDuringClear(t *testing.T) {
	a, sink := testAgent(t)
	history := json.RawMessage(`{"mode":"inline","items":[{"itemId":"workflow","revision":1,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"active","attempt":1,"status":"started"},{"childId":"done","attempt":1,"status":"terminal","terminal":"failed"}]},{"itemId":"shell","revision":1,"kind":"toolCall","tool":"shell","background":true,"status":"inProgress"}]}`)
	require.NoError(t, a.restoreChildRoutes("session", history, "head"))
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement"},"viewCursor":"new-head"}`)}
		}
		require.Equal(t, methodViewUnsubscribe, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	_, err := a.ClearContext()
	require.NoError(t, err)
	require.Len(t, sink.BackgroundTasks(), 4)
	for _, row := range sink.BackgroundTasks() {
		want := bgtask.StatusStopped
		if row.RowKey == museChildRowKey("session", "done") {
			want = bgtask.StatusFailed
		}
		assert.Equal(t, want, row.Status, row.RowKey)
	}
	assert.Empty(t, sink.Messages(), "metadata-only items cannot produce an empty native transcript frame")
}
