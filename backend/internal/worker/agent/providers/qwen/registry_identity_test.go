package qwen

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const qwenRegistryReplacementSession = "654fbfe5-9769-4517-8dfe-fcfbf19d61ac"

// The two clears install real native UUID shapes, so child-session validation
// participates in the test rather than treating the native identity as absent.
func qwenRegistryIdentityAgent(t *testing.T) (*Agent, *agenttest.ControlSink, agent.ProviderServices) {
	t.Helper()
	var starts atomic.Int64
	a, sink, _ := newQwenAgent(t, testutil.NewQuartzMock(t), func(request agenttest.RecordedRequest) agenttest.RPCReply {
		switch request.Method {
		case acp.MethodSessionNew:
			id := qwenInitialChildSession
			if starts.Add(1) > 1 {
				id = qwenRegistryReplacementSession
			}
			result, err := json.Marshal(map[string]string{"sessionId": id})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: result}
		case qwenTaskListMethod:
			return agenttest.RPCReply{Result: json.RawMessage(`{"tasks":[]}`)}
		default:
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	})
	services := agent.NewProviderServices(sink)
	queue := agent.OutputQueueFor(services)
	require.NotNil(t, queue)
	a.SetSinkForTest(agent.NewOrderedProviderServices(queue, services))
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenInitialChildSession, id)
	require.Equal(t, id, sink.LastSessionID())
	return a, sink, services
}

// Refuse an actual queue stage without holding the native peer or direct
// registry operations. Cleanup releases it even when an assertion fails.
func holdQwenRegistryIdentityOutput(t *testing.T, a *Agent, services agent.ProviderServices) func() {
	t.Helper()
	queue := agent.OutputQueueFor(services)
	require.NotNil(t, queue)
	var blocked atomic.Bool
	blocked.Store(true)
	unavailable := errors.New("the earlier output write is unavailable")
	queue.Append(agent.OutputWrite{Label: "hold Qwen registry identity output", Run: func() error {
		if blocked.Load() {
			return unavailable
		}
		return nil
	}})
	release := func() {
		waitForQwenWorkflowReaders(t, a)
		blocked.Store(false)
		require.NoError(t, agent.FlushPendingOutput(services))
	}
	t.Cleanup(func() {
		a.stopAllBackgroundTranscripts()
		release()
	})
	require.ErrorIs(t, agent.FlushPendingOutput(services), unavailable)
	return release
}

func feedQwenRegistryIdentityUpdate(t *testing.T, a *Agent, sessionID string, update map[string]any) {
	t.Helper()
	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	}))
}

func qwenRegistryIdentitySpawn(title, prompt string) map[string]any {
	return map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "call_shared", "status": "pending", "kind": "other",
		"rawInput": map[string]any{"description": title, "prompt": prompt, "run_in_background": false},
		"_meta":    map[string]any{"toolName": "agent", "provenance": "builtin"},
	}
}

func qwenRegistryIdentityRowByTitle(t *testing.T, sink *agenttest.ControlSink, title string) bgtask.Item {
	t.Helper()
	var matches []bgtask.Item
	for _, row := range sink.BackgroundTasks() {
		if row.Title == title {
			matches = append(matches, row)
		}
	}
	require.Len(t, matches, 1, "the native task keeps its own registry row: %s", title)
	return matches[0]
}

func qwenRegistryIdentityUserPrompts(t *testing.T, child *agenttest.Sink) []string {
	t.Helper()
	var prompts []string
	for _, message := range child.Messages() {
		if message.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			continue
		}
		var content struct {
			Content string `json:"content"`
		}
		require.NoError(t, json.Unmarshal(message.Content, &content))
		prompts = append(prompts, content.Content)
	}
	return prompts
}

func TestQwenOrdinaryChildIdentitySurvivesContextReplacementAndRetainedRetirement(t *testing.T) {
	t.Parallel()
	a, sink, services := qwenRegistryIdentityAgent(t)
	feedQwenRegistryIdentityUpdate(t, a, qwenInitialChildSession, qwenRegistryIdentitySpawn("Source child", "Read the source session task."))
	source := qwenRegistryIdentityRowByTitle(t, sink, "Source child")
	require.Equal(t, bgtask.StatusRunning, source.Status)
	require.NotEmpty(t, source.ChildAgentID)
	sourceChild := sink.Child(source.ChildAgentID)
	require.Equal(t, qwenInitialChildSession, sourceChild.LastSessionID())
	require.Equal(t, []string{"Read the source session task."}, qwenRegistryIdentityUserPrompts(t, sourceChild))

	release := holdQwenRegistryIdentityOutput(t, a, services)
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenRegistryReplacementSession, id)
	feedQwenRegistryIdentityUpdate(t, a, id, qwenRegistryIdentitySpawn("Replacement child", "Read the replacement session task."))

	// Capture the replacement's actual linkage before the old retirement can
	// reach storage. The native call ID is legal in both independent sessions.
	replacement := qwenRegistryIdentityRowByTitle(t, sink, "Replacement child")
	require.NotEmpty(t, replacement.ChildAgentID)
	require.NotEqual(t, source.ChildAgentID, replacement.ChildAgentID)
	require.Equal(t, bgtask.StatusRunning, replacement.Status)
	release()

	after := qwenRegistryIdentityRowByTitle(t, sink, "Replacement child")
	assert.Equal(t, replacement.RowKey, after.RowKey)
	assert.Equal(t, replacement.ChildAgentID, after.ChildAgentID)
	assert.Equal(t, bgtask.KindSubagent, after.Kind)
	assert.Equal(t, bgtask.StatusRunning, after.Status)
	assert.True(t, after.EndedAt.IsZero())
	assert.Empty(t, after.GroupKey)
	replacementChild := sink.Child(after.ChildAgentID)
	assert.Equal(t, id, replacementChild.LastSessionID())
	assert.Equal(t, []string{"Read the replacement session task."}, qwenRegistryIdentityUserPrompts(t, replacementChild))
	assert.Equal(t, []string{"Read the source session task."}, qwenRegistryIdentityUserPrompts(t, sourceChild))
	retired := qwenRegistryIdentityRowByTitle(t, sink, "Source child")
	assert.Equal(t, source.ChildAgentID, retired.ChildAgentID)
	assert.Equal(t, bgtask.StatusStopped, retired.Status)
	assert.NotEqual(t, retired.RowKey, after.RowKey)
}

func TestQwenWorkflowIdentitySurvivesContextReplacementAndRetainedRetirement(t *testing.T) {
	t.Parallel()
	a, sink, services := qwenRegistryIdentityAgent(t)
	release := holdQwenRegistryIdentityOutput(t, a, services)
	feedQwenRegistryIdentityUpdate(t, a, qwenInitialChildSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "call_shared", "status": "pending", "kind": "other",
		"rawInput": map[string]any{"name": "Source workflow"},
		"_meta":    map[string]any{"toolName": "workflow", "provenance": "builtin"},
	})
	require.Empty(t, sink.BackgroundTasks(), "the source workflow is accepted before its registry write reaches storage")
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenRegistryReplacementSession, id)
	feedQwenRegistryIdentityUpdate(t, a, id, qwenRegistryIdentitySpawn("Replacement child", "Read the replacement session task."))

	replacement := qwenRegistryIdentityRowByTitle(t, sink, "Replacement child")
	require.NotEmpty(t, replacement.ChildAgentID)
	require.Equal(t, bgtask.KindSubagent, replacement.Kind)
	require.Equal(t, bgtask.StatusRunning, replacement.Status)
	require.Empty(t, replacement.GroupKey)
	release()

	after := qwenRegistryIdentityRowByTitle(t, sink, "Replacement child")
	assert.Equal(t, replacement.RowKey, after.RowKey)
	assert.Equal(t, replacement.ChildAgentID, after.ChildAgentID)
	assert.Equal(t, bgtask.KindSubagent, after.Kind)
	assert.Equal(t, bgtask.StatusRunning, after.Status)
	assert.True(t, after.EndedAt.IsZero())
	assert.Empty(t, after.GroupKey)
	child := sink.Child(after.ChildAgentID)
	assert.Equal(t, id, child.LastSessionID())
	assert.Equal(t, []string{"Read the replacement session task."}, qwenRegistryIdentityUserPrompts(t, child))
	retired := qwenRegistryIdentityRowByTitle(t, sink, "Source workflow")
	assert.Equal(t, bgtask.KindWorkflow, retired.Kind)
	assert.Equal(t, bgtask.StatusStopped, retired.Status)
	assert.Empty(t, retired.ChildAgentID)
	assert.Equal(t, retired.RowKey, retired.GroupKey)
	assert.NotEqual(t, retired.RowKey, after.RowKey)
}

func TestQwenWorkflowNativeIDCanRepeatInTheReplacementSession(t *testing.T) {
	t.Parallel()
	a, sink, services := qwenRegistryIdentityAgent(t)
	release := holdQwenRegistryIdentityOutput(t, a, services)
	workflow := func(title string) map[string]any {
		return map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": "call_shared", "status": "pending", "kind": "other",
			"rawInput": map[string]any{"name": title}, "_meta": map[string]any{"toolName": "workflow"},
		}
	}
	feedQwenRegistryIdentityUpdate(t, a, qwenInitialChildSession, workflow("Source workflow"))
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenRegistryReplacementSession, id)
	waitForQwenWorkflowReaders(t, a)
	feedQwenRegistryIdentityUpdate(t, a, id, workflow("Replacement workflow"))
	// This test concerns registry identity. Join the empty native readers before
	// releasing storage, so a concurrent read cannot delay its explicit flush.
	a.stopAllWorkflowPollers()
	release()

	source := qwenRegistryIdentityRowByTitle(t, sink, "Source workflow")
	replacement := qwenRegistryIdentityRowByTitle(t, sink, "Replacement workflow")
	assert.NotEqual(t, source.RowKey, replacement.RowKey)
	assert.Equal(t, bgtask.StatusStopped, source.Status)
	assert.Equal(t, bgtask.StatusRunning, replacement.Status)
	assert.Equal(t, replacement.RowKey, replacement.GroupKey)
	feedQwenRegistryIdentityUpdate(t, a, id, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_shared", "status": "completed",
		"_meta": map[string]any{"toolName": "workflow"},
	})
	after := qwenRegistryIdentityRowByTitle(t, sink, "Replacement workflow")
	assert.Equal(t, replacement.RowKey, after.RowKey)
	assert.Equal(t, bgtask.StatusSucceeded, after.Status)
	assert.Equal(t, bgtask.StatusStopped, qwenRegistryIdentityRowByTitle(t, sink, "Source workflow").Status)
}

func TestQwenWorkflowPublicationKeepsItsCapturedNativeOwner(t *testing.T) {
	t.Parallel()
	a, sink, services := qwenRegistryIdentityAgent(t)
	release := holdQwenRegistryIdentityOutput(t, a, services)
	poller := newWorkflowPoller(a, "call_shared", "Captured source workflow")
	retired, err := poller.withOwningSession(func(string) error {
		return poller.apply(&qwenWorkflowEntry{
			ToolUseID: "call_shared", Label: "Captured source workflow", Status: "running",
			Dispatches: []qwenWorkflowDispatch{{ID: "dispatch-1", Label: "Captured source dispatch", Status: "running"}},
		})
	})
	require.NoError(t, err)
	require.False(t, retired)
	require.Empty(t, sink.BackgroundTasks())
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenRegistryReplacementSession, id)
	assert.True(t, poller.poll(), "the old native selector cannot query the replacement session")
	release()

	source := qwenRegistryIdentityRowByTitle(t, sink, "Captured source workflow")
	dispatch := qwenRegistryIdentityRowByTitle(t, sink, "Captured source dispatch")
	assert.Equal(t, "qwen-registry-v1:ec431696f1d789f6ccb726221e7502121b4aaab7ceb01ab62c91f4d2a6b9c38d", source.RowKey)
	assert.Equal(t, "qwen-registry-v1:efe480f6869b94a40378e0cb3290f9521f28095e627ea9ce09fe63af44e078b4", dispatch.RowKey)
	assert.Equal(t, source.RowKey, dispatch.GroupKey)
	assert.Equal(t, bgtask.StatusStopped, source.Status)
	assert.Equal(t, bgtask.StatusStopped, dispatch.Status)
	assert.Equal(t, qwenInitialChildSession, poller.sessionID)
	assert.Equal(t, "call_shared", poller.toolCallID)
}

func TestQwenChildInterruptRejectsTheOldOwnerAndKeepsNativeSelectors(t *testing.T) {
	t.Parallel()
	a, sink, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		switch request.Method {
		case acp.MethodSessionNew:
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"` + qwenRegistryReplacementSession + `"}`)}
		case qwenTaskListMethod:
			if request.Params["sessionId"] == qwenRegistryReplacementSession {
				return agenttest.RPCReply{Result: json.RawMessage(`{"tasks":[{"kind":"agent","id":"native-replacement-task","toolUseId":"call_shared","status":"running"}]}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"tasks":[]}`)}
		case qwenTaskCancelMethod:
			return agenttest.RPCReply{Result: json.RawMessage(`{"cancelled":true}`)}
		default:
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	})
	a.SetSessionIDForTest(qwenInitialChildSession)
	id, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, qwenRegistryReplacementSession, id)
	feedQwenRegistryIdentityUpdate(t, a, id, qwenRegistryIdentitySpawn("Replacement child", "Read the replacement session task."))
	row := qwenRegistryIdentityRowByTitle(t, sink, "Replacement child")
	oldKey := qwenAgentRowKey(qwenInitialChildSession, "call_shared")
	require.Error(t, a.InterruptChild(oldKey, agent.StopContext{}))
	syncPeer(t, a)
	assert.Empty(t, taskCancels(requests()), "the old owner must not cancel the replacement task")
	require.NoError(t, a.InterruptChild(row.RowKey, agent.StopContext{}))
	syncPeer(t, a)
	assert.Equal(t, []map[string]any{{"sessionId": id, "taskId": "native-replacement-task", "taskKind": "agent"}}, taskCancels(requests()))
	a.stateMu.Lock()
	stopped := a.children.stopped["call_shared"]
	_, qualifiedStop := a.children.stopped[row.RowKey]
	a.stateMu.Unlock()
	assert.True(t, stopped, "native completion consumes the raw tool-call stop identity")
	assert.False(t, qualifiedStop)
	feedQwenRegistryIdentityUpdate(t, a, id, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_shared", "status": "failed",
		"rawOutput": map[string]any{"type": "task_execution", "executionMode": "foreground", "status": "failed"},
	})
	assert.Equal(t, bgtask.StatusInterrupted, qwenRegistryIdentityRowByTitle(t, sink, "Replacement child").Status)
}
