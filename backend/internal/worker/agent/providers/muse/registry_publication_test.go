package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryFinalOutcomeRetriesWithoutAnotherNativeFrame(t *testing.T) {
	a, sink := testAgent(t)
	failure := &workflowRegistryFailureSink{Sink: sink, upsertKey: museWorkflowRowKey("session", "run"), remaining: 1}
	var queue agent.OutputQueue
	clock := testutil.NewQuartzMock(t)
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	a.sink = agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(failure))
	a.sessions["session"].sink = a.sink
	feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "completed", Children: json.RawMessage(`[{"childId":"child","attempt":1,"status":"terminal","terminal":"failed"}]`)}})
	require.Len(t, sink.Messages(), 1)
	require.True(t, queue.HasPending(), "the worker retry queue must own the refused final registry projection")
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, created)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	queue.StopRetries()
	row, ok := sink.BackgroundTask(museChildRowKey("session", "child"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Len(t, sink.Messages(), 1, "retrying metadata must not repeat the native transcript")
	assert.False(t, queue.HasPending())
}

func TestRegistryRetryWaitsUntilTheOutputDispatcherUnlocks(t *testing.T) {
	a, sink := testAgent(t)
	failure := &workflowRegistryFailureSink{Sink: sink, upsertKey: museWorkflowRowKey("session", "run"), remaining: 1}
	a.sink = agent.NewProviderServices(failure)
	a.sessions["session"].sink = a.sink
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: 1, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", Status: "inProgress", Children: json.RawMessage(`[]`)}})
	queue := agent.OutputQueueFor(a.sink)
	require.True(t, queue.HasPending())
	a.dispatcher.mu.Lock()
	err := queue.Flush()
	a.dispatcher.mu.Unlock()
	assert.Error(t, err, "a transcript writer can flush this queue while it still holds the dispatcher")
	assert.Empty(t, sink.BackgroundTasks(), "registry observers must run after the dispatcher unlocks")
	require.NoError(t, queue.Flush())
	row, ok := sink.BackgroundTask(museWorkflowRowKey("session", "run"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}
