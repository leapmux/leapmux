package qwen

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openQwenWorkflowMetadataCall(t *testing.T, a *Agent, sessionID string, input map[string]any) *workflowPoller {
	t.Helper()
	feedQwenRegistryIdentityUpdate(t, a, sessionID, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "call_wf", "status": "pending", "kind": "other",
		"rawInput": input, "_meta": map[string]any{"toolName": "workflow"},
	})
	a.stateMu.Lock()
	poller := a.children.workflows["call_wf"]
	a.stateMu.Unlock()
	require.NotNil(t, poller)
	return poller
}

// This frame deliberately carries no rawInput or metadata. ACP folds the
// opener's inline arguments into the update before Qwen receives its hook.
func sparseQwenWorkflowMetadataProgress(t *testing.T, a *Agent, sessionID string) {
	t.Helper()
	feedQwenRegistryIdentityUpdate(t, a, sessionID, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_wf", "status": "in_progress",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Native workflow progress"}}},
	})
}

func finishQwenWorkflowMetadataCall(t *testing.T, a *Agent, sessionID string) {
	t.Helper()
	feedQwenRegistryIdentityUpdate(t, a, sessionID, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_wf", "status": "completed",
	})
}

func qwenWorkflowMetadataRows(sink *agenttest.ControlSink, sessionID string) map[string]bgtask.Item {
	key := qwenWorkflowRowKey(sessionID, "call_wf")
	rows := make(map[string]bgtask.Item)
	for _, row := range sink.BackgroundTasks() {
		if row.GroupKey == key {
			rows[row.RowKey] = row
		}
	}
	return rows
}

func TestQwenWorkflowCanonicalCompletionMetadataSurvivesSparseProgress(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, requests := newQwenAgent(t, clock, qwenWorkflowTaskList(qwenWorkflowPublicationFinalEntry))
	poller := openQwenWorkflowMetadataCall(t, a, qwenTestSession, map[string]any{"script": "return await agent('Native assignment.')"})
	clock.Advance(workflowPollInterval).MustWait(testutil.DeadlineContext(t))
	waitForQwenWorkflowStorage(t, a, poller)
	before := qwenWorkflowMetadataRows(sink, qwenTestSession)
	require.Len(t, before, 4)
	root := before[poller.rowKey]
	require.Equal(t, "final triage", root.Title)
	require.Equal(t, "final triage", root.GroupLabel)
	require.Equal(t, bgtask.StatusSucceeded, root.Status)
	a.stateMu.Lock()
	remaining := len(a.children.workflows)
	a.stateMu.Unlock()
	require.Zero(t, remaining, "the native snapshot retires its reader before the final wire frame")

	sparseQwenWorkflowMetadataProgress(t, a, qwenTestSession)
	finishQwenWorkflowMetadataCall(t, a, qwenTestSession)
	waitForQwenRegistryStorage(t, a)

	assert.Equal(t, before, qwenWorkflowMetadataRows(sink, qwenTestSession), "inherited inline arguments cannot replace the native snapshot's canonical metadata or outcomes")
	syncPeer(t, a)
	assert.Len(t, requestsFor(requests(), qwenTaskListMethod), 1)
}

func TestQwenWorkflowCanonicalRunningMetadataSurvivesArgumentProgress(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(qwenWorkflowRunningEntry))
	poller := openQwenWorkflowMetadataCall(t, a, qwenTestSession, map[string]any{"script": "return await agent('Native assignment.')"})
	clock.Advance(workflowPollInterval).MustWait(testutil.DeadlineContext(t))
	testutil.RequireEventually(t, func() bool {
		row, ok := sink.BackgroundTask(poller.rowKey)
		return ok && row.Title == "triage"
	})
	waitForQwenRegistryStorage(t, a)
	before := qwenWorkflowMetadataRows(sink, qwenTestSession)
	require.Len(t, before, 3)
	require.Equal(t, bgtask.StatusRunning, before[poller.rowKey].Status)

	feedQwenRegistryIdentityUpdate(t, a, qwenTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_wf", "status": "in_progress",
		"rawInput": map[string]any{"scriptPath": "late-workflow.js"},
	})
	waitForQwenRegistryStorage(t, a)

	assert.Equal(t, before, qwenWorkflowMetadataRows(sink, qwenTestSession), "a running native snapshot owns metadata before the final outcome exists")
}

func TestQwenWorkflowArgumentsEnrichBeforeTheFirstNativeSnapshot(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, requests := newQwenAgent(t, clock, qwenWorkflowTaskList(""))
	poller := openQwenWorkflowMetadataCall(t, a, qwenTestSession, map[string]any{})
	waitForQwenRegistryStorage(t, a)
	initial, ok := sink.BackgroundTask(poller.rowKey)
	require.True(t, ok)
	require.Equal(t, "Workflow", initial.Title)

	feedQwenRegistryIdentityUpdate(t, a, qwenTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_wf", "status": "in_progress",
		"rawInput": map[string]any{"scriptPath": "before-snapshot.js"},
	})
	waitForQwenRegistryStorage(t, a)
	row, ok := sink.BackgroundTask(poller.rowKey)
	require.True(t, ok)
	assert.Equal(t, "before-snapshot.js", row.Title)
	assert.Equal(t, "before-snapshot.js", row.GroupLabel)
	assert.Equal(t, poller.rowKey, row.GroupKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), qwenTaskListMethod), "the unadvanced mock clock supplies no native metadata")
}

func TestQwenWorkflowMetadataAuthorityResetsWithTheNativeSession(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, requests := newQwenAgent(t, clock, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"` + qwenRegistryReplacementSession + `"}`)}
		}
		return qwenWorkflowTaskList(qwenWorkflowRunningEntry)(request)
	})
	source := openQwenWorkflowMetadataCall(t, a, qwenTestSession, map[string]any{"script": "return await agent('Source assignment.')"})
	clock.Advance(workflowPollInterval).MustWait(testutil.DeadlineContext(t))
	testutil.RequireEventually(t, func() bool {
		row, ok := sink.BackgroundTask(source.rowKey)
		return ok && row.Title == "triage"
	})
	waitForQwenRegistryStorage(t, a)
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenRegistryReplacementSession, id)
	waitForQwenWorkflowReaders(t, a)
	replacement := openQwenWorkflowMetadataCall(t, a, id, map[string]any{})
	feedQwenRegistryIdentityUpdate(t, a, id, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_wf", "status": "in_progress",
		"rawInput": map[string]any{"scriptPath": "replacement-before-snapshot.js"},
	})
	waitForQwenRegistryStorage(t, a)

	row, ok := sink.BackgroundTask(replacement.rowKey)
	require.True(t, ok)
	assert.NotEqual(t, source.rowKey, row.RowKey)
	assert.Equal(t, "replacement-before-snapshot.js", row.Title)
	assert.Equal(t, row.Title, row.GroupLabel)
	assert.Equal(t, row.RowKey, row.GroupKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	retired, ok := sink.BackgroundTask(source.rowKey)
	require.True(t, ok)
	assert.Equal(t, "triage", retired.Title)
	assert.Equal(t, "triage", retired.GroupLabel)
	assert.Equal(t, bgtask.StatusStopped, retired.Status)
	syncPeer(t, a)
	for _, request := range requestsFor(requests(), qwenTaskListMethod) {
		assert.Equal(t, qwenTestSession, request.Params["sessionId"], "replacement fallback enrichment precedes its first native snapshot")
	}
}

func TestQwenWorkflowRetainedSnapshotMetadataSurvivesSparseProgress(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, _ := newQwenAgent(t, clock, qwenWorkflowTaskList(qwenWorkflowPublicationFinalEntry))
	poller := openQwenWorkflowMetadataCall(t, a, qwenTestSession, map[string]any{"script": "return await agent('Native assignment.')"})
	waitForQwenRegistryStorage(t, a)
	release := holdQwenRegistryIdentityOutput(t, a, a.Sink())
	clock.Advance(workflowPollInterval).MustWait(testutil.DeadlineContext(t))
	select {
	case <-poller.stopped:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the admitted native snapshot did not retire its reader")
	}
	waitForQwenWorkflowReaders(t, a)
	initial, ok := sink.BackgroundTask(poller.rowKey)
	require.True(t, ok)
	require.Equal(t, "Workflow", initial.Title, "storage still refuses the canonical snapshot")

	sparseQwenWorkflowMetadataProgress(t, a, qwenTestSession)
	finishQwenWorkflowMetadataCall(t, a, qwenTestSession)
	release()

	rows := qwenWorkflowMetadataRows(sink, qwenTestSession)
	require.Len(t, rows, 4)
	assert.Equal(t, "final triage", rows[poller.rowKey].Title)
	assert.Equal(t, "final triage", rows[poller.rowKey].GroupLabel)
	assert.Equal(t, bgtask.StatusSucceeded, rows[poller.rowKey].Status)
	for id, status := range map[string]bgtask.Status{"dispatch-1": bgtask.StatusSucceeded, "dispatch-2": bgtask.StatusFailed, "dispatch-3": bgtask.StatusSucceeded} {
		key := qwenWorkflowDispatchRowKey(qwenTestSession, "call_wf", id)
		row, ok := rows[key]
		require.True(t, ok, id)
		assert.Equal(t, status, row.Status, id)
		assert.Equal(t, "final triage", row.GroupLabel, id)
		assert.Equal(t, poller.rowKey, row.GroupKey, id)
	}
}
