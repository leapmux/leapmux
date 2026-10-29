package qwen

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type qwenNoticeOrderSink struct {
	*agenttest.ControlSink
	textBeforeNotice     [][]string
	rowsBeforeResolution []int
	workerNoticeTexts    [][]string
}

func (s *qwenNoticeOrderSink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.workerNoticeTexts = append(s.workerNoticeTexts, assembledTexts(&s.Sink))
	if content[contracts.NotificationFieldType] == contracts.NotificationTypeApiRetryResolved {
		s.rowsBeforeResolution = append(s.rowsBeforeResolution, len(s.Messages()))
	}
	s.ControlSink.PersistLeapMuxNotification(content)
}

func TestQwenWorkerNoticesFollowTheBufferedAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"recording failure", qwenNotifyFrame(t, "qwen/notify/session/recording-degraded", qwenTestSession, map[string]any{"reason": "write_failed"})},
		{"background completion", backgroundNotice(t, contracts.QwenMessageSourceBackgroundTaskCompleted, map[string]any{"kind": qwenBackgroundMonitor, "status": "completed", "taskId": "monitor"})},
		{"notification overflow", backgroundNotice(t, contracts.QwenMessageSourceBackgroundNotification, map[string]any{"kind": qwenBackgroundQueue, "status": "recorded"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink, _ := newQwenAgent(t, nil, nil)
			ordered := &qwenNoticeOrderSink{ControlSink: sink}
			a.SetSinkForTest(agent.NewProviderServices(ordered))
			a.HandleOutput(metaChunk(t, "before", nil))
			a.HandleOutput(tc.frame)
			assert.Equal(t, [][]string{{"before"}}, ordered.workerNoticeTexts)
		})
	}
}

func (s *qwenNoticeOrderSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	s.textBeforeNotice = append(s.textBeforeNotice, assembledTexts(&s.Sink))
	return s.ControlSink.PersistNotification(source, content)
}

func TestQwenDiscreteNoticeIsItsOwnRow(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"vision_bridge_notice", "todo_stop_guard", ""} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newQwenAgent(t, nil, nil)
			ordered := &qwenNoticeOrderSink{ControlSink: sink}
			a.SetSinkForTest(agent.NewProviderServices(ordered))
			a.SetPromptActiveForTest(true)
			a.HandleOutput(metaChunk(t, "before", nil))
			a.HandleOutput(metaChunk(t, "notice", map[string]any{"qwenDiscreteMessage": true, "source": source}))
			a.HandleOutput(metaChunk(t, "after", nil))
			a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
			assert.Equal(t, []string{"before", "after"}, assembledTexts(&sink.Sink))
			assert.Equal(t, [][]string{{"before"}}, ordered.textBeforeNotice)
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.Contains(t, string(sink.PersistedNotifications()[0].Content), `"text":"notice"`)
		})
	}
}

func TestQwenBackgroundResponseIsItsOwnSegment(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(metaChunk(t, "before", nil))
	a.HandleOutput(metaChunk(t, "background answer", map[string]any{"qwenDiscreteMessage": true, "source": "background_notification_response"}))
	a.HandleOutput(metaChunk(t, "second background answer", map[string]any{"qwenDiscreteMessage": true, "source": "background_notification_response"}))
	a.HandleOutput(metaChunk(t, "after", nil))
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
	assert.Equal(t, []string{"before", "background answer", "second background answer", "after"}, assembledTexts(&sink.Sink))
	assert.Empty(t, sink.PersistedNotifications())
}

func TestQwenStopHookLoopPersists(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	// MessageEmitter.emitStopHookLoop sends an empty text chunk with this metadata.
	a.HandleOutput(metaChunk(t, "", map[string]any{"stopHookLoop": map[string]any{
		"iterationCount": 2, "reasons": []string{"Run the checks"}, "stopHookCount": 1,
	}}))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "Run the checks")
	assert.Empty(t, sink.Messages())
}

func TestQwenDiscreteMarkerRequiresTrue(t *testing.T) {
	t.Parallel()
	for _, value := range []any{nil, false, 0, "true"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			a, sink, _ := newQwenAgent(t, nil, nil)
			a.HandleOutput(metaChunk(t, "answer", map[string]any{"qwenDiscreteMessage": value}))
			a.FlushBufferedOutput(agent.MessageCompletionComplete)
			assert.Equal(t, []string{"answer"}, assembledTexts(&sink.Sink))
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestQwenBlankDiscreteNoticeDoesNotStoreOrSplitText(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(metaChunk(t, "before", nil))
	a.HandleOutput(metaChunk(t, "", map[string]any{"qwenDiscreteMessage": true}))
	a.HandleOutput(metaChunk(t, "after", nil))
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Empty(t, sink.PersistedNotifications())
	assert.Equal(t, []string{"beforeafter"}, assembledTexts(&sink.Sink))
}

func TestQwenDiscreteMarkerDoesNotSplitThoughts(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	for _, text := range []string{"first ", "second"} {
		a.HandleOutput(sessionUpdate(t, map[string]any{
			"sessionUpdate": "agent_thought_chunk", "messageId": "thought",
			"content": map[string]any{"type": "text", "text": text},
			"_meta":   map[string]any{"qwenDiscreteMessage": true},
		}))
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	require.Len(t, sink.Messages(), 1)
	kind, text, _, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, agent.AssembledMessageKindReasoning, kind)
	assert.Equal(t, "first second", text)
}

func TestQwenDiscreteNoticeOfAnotherSessionStoresNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": "session/update", "params": map[string]any{
			"sessionId": "other-session", "update": map[string]any{
				"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "notice"},
				"_meta": map[string]any{"qwenDiscreteMessage": true},
			},
		},
	}))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func qwenExecutionChunk(t *testing.T, phase, outcome string) []byte {
	t.Helper()
	// Trimmed from qwen/logs/r2-retrylong-1.log. The native lifecycle states no error text or attempt count.
	return metaChunk(t, "", map[string]any{"executionLifecycle": map[string]any{
		"v": 1, "kind": "request", "phase": phase, "outcome": outcome,
	}})
}

// retryFailedRequest feeds one failed attempt that Qwen retries: the request
// ends with an error, and the next request of the same prompt starts.
func retryFailedRequest(t *testing.T, a *Agent) {
	t.Helper()
	a.HandleOutput(qwenExecutionChunk(t, "ended", "error"))
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
}

func TestQwenRetryRoundsFoldIntoOneRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	for range 3 {
		retryFailedRequest(t, a)
	}
	a.HandleOutput(qwenExecutionChunk(t, "ended", "error"))
	require.Len(t, sink.Notifications(), 3, "the last failed attempt states no retry")
	for index, notice := range sink.Notifications() {
		assert.Equal(t, map[string]any{"type": "api_retry", "count": int64(index + 1)}, notice)
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

// Qwen ends the last attempt of a request with an error too, and a fail-fast
// error such as a 401 is that last attempt. The error alone therefore states
// no retry: only a later start of a request of the same prompt does.
func TestQwenFailedRequestWithoutARetryWritesNoAPIRetryRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetSessionIDForTest(qwenFixtureSession)
	a.SetPromptActiveForTest(true)

	a.HandleOutput([]byte(qwenFixtureRequestStarted))
	a.HandleOutput([]byte(qwenFixtureRequestFailed))
	assert.Empty(t, apiRetryRows(sink), "the failed attempt can be the last one")

	a.FinishPromptRequestForTest(qwenFixtureSession, nil, qwenResponseError(t, qwenFixture401Error))
	assert.Empty(t, apiRetryRows(sink), "the prompt failed without a retry")
	assert.Equal(t, []string{"prompt failed: json-rpc error -32603: Internal error: {\"details\":\"401 Incorrect API key provided.\"}"},
		agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestQwenRequestStartAfterAFailureWritesOneAPIRetryRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetSessionIDForTest(qwenFixtureSession)
	a.SetPromptActiveForTest(true)

	a.HandleOutput([]byte(qwenFixtureRequestStarted))
	a.HandleOutput([]byte(qwenFixtureRequestFailed))
	a.HandleOutput([]byte(qwenFixtureRetryStarted))

	assert.Equal(t, []map[string]any{{"type": "api_retry", "count": int64(1)}}, apiRetryRows(sink))
	a.HandleOutput([]byte(qwenFixtureRetryStarted))
	assert.Len(t, apiRetryRows(sink), 1, "a second start of the retry states no second retry")
}

// A start of the next prompt's request is no retry of the failed request.
func TestQwenRequestStartOfAnotherPromptWritesNoAPIRetryRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetSessionIDForTest(qwenFixtureSession)

	a.HandleOutput([]byte(qwenFixtureRequestStarted))
	a.HandleOutput([]byte(qwenFixtureRequestFailed))
	a.HandleOutput([]byte(qwenFixtureNextPromptStarted))

	assert.Empty(t, apiRetryRows(sink))
}

// The end of a prompt forgets its failed request, so the first request of the
// next prompt, which Qwen can number like the last one after a restart, states
// no retry.
func TestQwenPromptEndForgetsTheFailedRequest(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetSessionIDForTest(qwenFixtureSession)
	a.SetPromptActiveForTest(true)
	a.HandleOutput([]byte(qwenFixtureRequestStarted))
	a.HandleOutput([]byte(qwenFixtureRequestFailed))
	a.FinishPromptRequestForTest(qwenFixtureSession, nil, qwenResponseError(t, qwenFixture401Error))

	a.HandleOutput([]byte(qwenFixtureRetryStarted))

	assert.Empty(t, apiRetryRows(sink))
}

func TestQwenRetryResolvesOnSuccess(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	retryFailedRequest(t, a)
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	require.Len(t, sink.Notifications(), 2)
	assert.Equal(t, map[string]any{"type": "api_retry_resolved"}, sink.Notifications()[1])
	retryFailedRequest(t, a)
	assert.Equal(t, int64(1), sink.Notifications()[2]["count"])
}

func TestQwenRetryResolvesBeforeModelOutput(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	ordered := &qwenNoticeOrderSink{ControlSink: sink}
	a.SetSinkForTest(agent.NewProviderServices(ordered))
	retryFailedRequest(t, a)
	a.HandleOutput(sessionUpdate(t, map[string]any{
		"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "working"},
	}))
	a.HandleOutput(metaChunk(t, "answer", nil))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	assert.Equal(t, []int{0}, ordered.rowsBeforeResolution)
	require.Len(t, sink.Notifications(), 2)
}

func TestQwenSubagentLifecycleChangesNothing(t *testing.T) {
	t.Parallel()
	for _, lifecycle := range []map[string]any{
		{"v": 1, "kind": "request", "phase": "ended", "outcome": "error", "subagentId": "memory"},
		{"v": 1, "kind": "tool", "phase": "ended", "outcome": "error"},
	} {
		a, sink, _ := newQwenAgent(t, nil, nil)
		a.HandleOutput(sessionUpdate(t, map[string]any{
			"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "working"},
		}))
		a.HandleOutput(metaChunk(t, "", map[string]any{"executionLifecycle": lifecycle}))
		assert.Empty(t, sink.Messages(), "a lifecycle frame must not flush the thought")
		assert.Empty(t, sink.Notifications())
	}
}

func TestQwenRetryResetAtPromptEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	retryFailedRequest(t, a)
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
	a.HandleOutput(metaChunk(t, "new answer", nil))
	require.Len(t, sink.Notifications(), 1)
	retryFailedRequest(t, a)
	require.Len(t, sink.Notifications(), 2)
	assert.Equal(t, int64(1), sink.Notifications()[1]["count"])
}

func TestQwenRetryCancellationDoesNotResolve(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"cancelled", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			a, sink, _ := newQwenAgent(t, nil, nil)
			retryFailedRequest(t, a)
			a.HandleOutput(qwenExecutionChunk(t, "ended", outcome))
			a.HandleOutput(metaChunk(t, "next request", nil))
			require.Len(t, sink.Notifications(), 1)
			retryFailedRequest(t, a)
			assert.Equal(t, int64(1), sink.Notifications()[1]["count"])
		})
	}
}

func TestQwenRetryUnknownOutcomeKeepsThePendingRetry(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	retryFailedRequest(t, a)
	for _, outcome := range []string{"", "future"} {
		a.HandleOutput(qwenExecutionChunk(t, "ended", outcome))
	}
	require.Len(t, sink.Notifications(), 1)
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	require.Len(t, sink.Notifications(), 2)
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, sink.Notifications()[1]["type"])
}

func TestQwenRetryResetAtNativeTurnEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	require.True(t, a.BeginAgentTurn())
	retryFailedRequest(t, a)
	a.HandleOutput(frame(t, map[string]any{"method": contracts.QwenMethodEndTurn, "params": map[string]any{"sessionId": qwenTestSession}}))
	a.HandleOutput(metaChunk(t, "next request", nil))
	require.Len(t, sink.Notifications(), 1)
}

func qwenThoughtChunk(t *testing.T, text string) []byte {
	t.Helper()
	return sessionUpdate(t, map[string]any{
		"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": text},
	})
}

func TestQwenThoughtReplayKeepsTheSecondAttempt(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	// Trimmed from qwen/logs/thoughtcut-1.log. Qwen retries the request after it drops the first thought.
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	a.HandleOutput(qwenThoughtChunk(t, "THINK-ATTEMPT-1 "))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "error"))
	assert.Empty(t, sink.Messages())
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetModelScopeProgress("acp:"+contracts.ACPUpdateAgentThoughtChunk))
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	a.HandleOutput(qwenThoughtChunk(t, "THINK-ATTEMPT-2 "))
	a.HandleOutput(metaChunk(t, "OK", nil))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	require.Len(t, sink.Messages(), 2)
	kind, text, _, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, agent.AssembledMessageKindReasoning, kind)
	assert.Equal(t, "THINK-ATTEMPT-2 ", text)
	assert.Equal(t, []string{"OK"}, assembledTexts(&sink.Sink))
}

func TestQwenTextCutKeepsBothTexts(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	// Trimmed from qwen/logs/textcut-1.log. Qwen retains the first answer text in its own history.
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	a.HandleOutput(metaChunk(t, "PARTIAL-ATTEMPT-1 ", nil))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "error"))
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	a.HandleOutput(metaChunk(t, "OK", nil))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Equal(t, []string{"PARTIAL-ATTEMPT-1 OK"}, assembledTexts(&sink.Sink))
}

func TestQwenThoughtReplayResetsTheTextFlagForEachRequest(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	a.HandleOutput(metaChunk(t, "first answer", nil))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "success"))
	a.HandleOutput(qwenExecutionChunk(t, "started", ""))
	a.HandleOutput(qwenThoughtChunk(t, "discard this thought"))
	a.HandleOutput(qwenExecutionChunk(t, "ended", "error"))
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, []string{"first answer"}, assembledTexts(&sink.Sink))
}

// metaChunk is an agent message that carries `_meta` beside its text.
func metaChunk(t *testing.T, text string, meta map[string]any) []byte {
	t.Helper()
	return sessionUpdate(t, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}, "_meta": meta,
	})
}

// assembledTexts reads the assembled text rows of one transcript.
func assembledTexts(sink *agenttest.Sink) []string {
	var texts []string
	for _, message := range sink.Messages() {
		kind, text, _, ok := agenttest.DecodeAssembledMessage(message.Content)
		if ok && kind == agent.AssembledMessageKindText {
			texts = append(texts, text)
		}
	}
	return texts
}

func TestQwenUsageBroadcastsTheTokenCounts(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(metaChunk(t, "", map[string]any{
		"usage": map[string]any{"inputTokens": 27588, "outputTokens": 34, "totalTokens": 27622, "thoughtTokens": 30, "cachedReadTokens": 20000},
	}))

	usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(7588),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(20000),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
		contracts.ContextUsageFieldOutputTokens:             int64(34),
	}, usage, "the cached tokens are part of Qwen's input count, so they are counted once")
}

func TestQwenUsageNeverStatesANegativeInput(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(metaChunk(t, "", map[string]any{"usage": map[string]any{"inputTokens": 5, "cachedReadTokens": 9}}))
	usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, int64(0), usage.(map[string]any)[contracts.ContextUsageFieldInputTokens])

	a.HandleOutput(metaChunk(t, "", map[string]any{"usage": "text"}))
	assert.Equal(t, 1, sink.SessionInfoCount(), "an unreadable usage broadcasts nothing")
}

func TestQwenCompactionStoresNativeEndsAndKeepsUnknownPhases(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(metaChunk(t, "Compressing context...", map[string]any{"contextCompression": map[string]any{"phase": "progress"}, "source": "slash_command"}))
	live, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, ok)
	assert.NotNil(t, live)
	assert.Empty(t, sink.Notifications())
	assert.Empty(t, sink.PersistedNotifications())
	a.HandleOutput(metaChunk(t, "Context compressed (900 -> 300).", map[string]any{"contextCompression": map[string]any{
		"phase": "done", "originalTokenCount": 900, "newTokenCount": 300,
	}}))
	a.HandleOutput(metaChunk(t, "Context compressed (900 -> 850).", map[string]any{"contextCompression": map[string]any{
		"phase": "done", "originalTokenCount": 900, "newTokenCount": 850, "warning": "little was saved",
	}}))
	a.HandleOutput(metaChunk(t, "x", map[string]any{"contextCompression": map[string]any{"phase": "unknown"}}))

	assert.Empty(t, sink.Notifications())
	require.Len(t, sink.PersistedNotifications(), 2)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), `"originalTokenCount":900`)
	assert.Contains(t, string(sink.PersistedNotifications()[1].Content), `"warning":"little was saved"`)
	for _, notice := range sink.PersistedNotifications() {
		assert.Equal(t, agent.CompactionBoundaryNotification(), (qwenProvider{}).Classify(notice.Content))
	}
	live, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, ok)
	assert.Nil(t, live)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), `"phase":"unknown"`)
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, assembledTexts(&sink.Sink), "Qwen's progress words do not become the agent's text")
}

func TestQwenCompactionDoneFollowsTheBufferedAnswer(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	ordered := &qwenNoticeOrderSink{ControlSink: sink}
	a.SetSinkForTest(agent.NewProviderServices(ordered))
	a.HandleOutput(metaChunk(t, "before", nil))
	// Trimmed from qwen/logs/r2-compress-3.log. The native end can arrive without a stored start.
	a.HandleOutput(metaChunk(t, "Context compressed.", map[string]any{"contextCompression": map[string]any{
		"phase": "done", "originalTokenCount": 60000, "newTokenCount": 44624,
	}}))
	assert.Equal(t, [][]string{{"before"}}, ordered.textBeforeNotice)
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Equal(t, agent.CompactionBoundaryNotification(), (qwenProvider{}).Classify(sink.PersistedNotifications()[0].Content))
}

func TestQwenOrdinaryTextIsNotConsumed(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(metaChunk(t, "The answer.", map[string]any{"source": "background_notification_response"}))
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
	assert.Equal(t, []string{"The answer."}, assembledTexts(&sink.Sink), "the model's answer to a background notice is its own text")
}

// backgroundNotice is Qwen's notice about one background task, under source.
func backgroundNotice(t *testing.T, source string, task map[string]any) []byte {
	t.Helper()
	return metaChunk(t, "Qwen's own words for the notice.", map[string]any{
		"source": source, "qwenDiscreteMessage": true, "backgroundTask": task,
	})
}

// statusTexts reads the agent-status lines of one transcript.
func statusTexts(sink *agenttest.ControlSink) []string {
	var texts []string
	for _, notification := range sink.Notifications() {
		if notification[contracts.NotificationFieldType] == contracts.NotificationTypeAgentStatus {
			texts = append(texts, notification[contracts.NotificationFieldText].(string))
		}
	}
	return texts
}

// The notice about a background subagent or command whose registry row closes
// for it is not shown again: the row states the end. Nor is its repeat at the
// start of the turn that answers it.
func TestQwenBackgroundNoticeOfARowIsConsumed(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.ApplySubagentObservation(&acp.SubagentObservation{RowKey: qwenAgentRowKey(qwenTestSession, "call_1"), Title: "Helper", Status: bgtask.StatusRunning})
	a.ApplySubagentObservation(&acp.SubagentObservation{RowKey: "shell:bg_1a2b", Kind: bgtask.KindShell, Title: "npm test", Status: bgtask.StatusRunning})
	a.SetPromptActiveForTest(true)
	for _, task := range []map[string]any{
		{"taskId": "general-purpose-call_1", "status": "completed", "kind": "agent", "toolUseId": "call_1", "description": "Helper"},
		{"taskId": "bg_1a2b", "status": "completed", "kind": "shell", "commandLabel": "npm test"},
	} {
		for _, source := range []string{"background_task_completed", "background_notification"} {
			a.HandleOutput(backgroundNotice(t, source, task))
		}
	}
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, assembledTexts(&sink.Sink))
	assert.Empty(t, statusTexts(sink))
	for _, rowKey := range []string{qwenAgentRowKey(qwenTestSession, "call_1"), "shell:bg_1a2b"} {
		row, ok := sink.BackgroundTask(rowKey)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status, rowKey)
	}
}

// The notice about background work that no row tracks reaches the transcript,
// as a status line of its own. Without it, a monitor or a workflow ended with
// no trace, and the turn that Qwen then started to answer it had no stated
// cause. Its repeat at the start of that turn is not shown twice.
func TestQwenBackgroundNoticeWithoutARowReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	for _, task := range []map[string]any{
		{"taskId": "mon_1", "status": "completed", "kind": "monitor", "toolUseId": "call_mon", "description": "Watch the build", "eventCount": 3},
		{"taskId": "wf_9f", "status": "failed", "kind": "workflow"},
		{"taskId": "general-purpose-call_2", "status": "cancelled", "kind": "agent", "description": "Lost helper"},
		{"taskId": "bg_ff", "status": "completed", "kind": "shell"},
		{"taskId": "x-1", "status": "completed", "kind": "future-kind"},
	} {
		a.HandleOutput(backgroundNotice(t, "background_task_completed", task))
		a.HandleOutput(backgroundNotice(t, "background_notification", task))
	}
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)

	assert.Equal(t, []string{
		`Background monitor "Watch the build" completed`,
		`Background workflow wf_9f failed`,
		`Background agent "Lost helper" was stopped`,
		`Background command bg_ff completed`,
		`Background task x-1 completed`,
	}, statusTexts(sink))
	assert.Empty(t, assembledTexts(&sink.Sink), "a notice never joins the model's own text")
}

// Qwen states that its notification queue overflowed only at the start of a
// turn, so that notice is no repeat, and it reaches the transcript.
func TestQwenQueueOverflowNoticeReachesTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(backgroundNotice(t, "background_notification", map[string]any{"kind": "queue", "status": "dropped"}))
	a.HandleOutput(backgroundNotice(t, "background_notification", map[string]any{"kind": "queue", "status": "recorded"}))

	assert.Equal(t, []string{
		"Qwen Code dropped background notifications, because its notification queue was full",
		"Qwen Code recorded background results but did not deliver them live, because its notification queue was full",
	}, statusTexts(sink))
}

func TestQwenBackgroundNoticeText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		task qwenBackgroundTask
		want string
	}{
		{task: qwenBackgroundTask{Kind: "shell", TaskID: "bg_1", CommandLabel: "npm test", Status: "completed"}, want: `Background command "npm test" completed`},
		{task: qwenBackgroundTask{Kind: "agent", TaskID: "t", Description: "Helper", CommandLabel: "ignored", Status: "failed"}, want: `Background agent "Helper" failed`},
		{task: qwenBackgroundTask{Kind: "monitor", TaskID: " mon_1 ", Description: "  ", Status: "paused"}, want: `Background monitor mon_1 ended`},
		{task: qwenBackgroundTask{}, want: `Background task ended`},
	} {
		assert.Equal(t, tc.want, backgroundNoticeText(tc.task), "%+v", tc.task)
	}
}

// The agent shows a notice as a status line when it cannot read the registry
// row of the notice, and it leaves the row as it was. A failed read of the
// registry thus loses no notice.
func TestQwenBackgroundNoticeWithAnUnreadableRegistryIsShown(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.ApplySubagentObservation(&acp.SubagentObservation{RowKey: qwenAgentRowKey(qwenTestSession, "call_1"), Title: "Helper", Status: bgtask.StatusRunning})
	sink.LookupErr = errors.New("the registry is down")

	a.HandleOutput(backgroundNotice(t, "background_task_completed", map[string]any{
		"taskId": "general-purpose-call_1", "status": "completed", "kind": "agent", "toolUseId": "call_1", "description": "Helper",
	}))

	assert.Equal(t, []string{`Background agent "Helper" completed`}, statusTexts(sink))
	assert.Positive(t, sink.LookupBackgroundTaskCalls(qwenAgentRowKey(qwenTestSession, "call_1")), "the notice asked the registry for its row")
	row, ok := sink.BackgroundTask(qwenAgentRowKey(qwenTestSession, "call_1"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a row that could not be read is not closed on a guess")
}

// The agent consumes a notice whose task it cannot read, and states that some
// background work ended.
func TestQwenBackgroundNoticeWithAnUnreadableTaskIsShown(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(metaChunk(t, "Qwen's own words.", map[string]any{"source": "background_task_completed", "backgroundTask": "text"}))
	a.HandleOutput(metaChunk(t, "Qwen's own words again.", map[string]any{"source": "background_task_completed"}))
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)

	assert.Equal(t, []string{"Background task ended", "Background task ended"}, statusTexts(sink))
	assert.Empty(t, assembledTexts(&sink.Sink))
}

// A compaction report that the agent cannot read states nothing, and its words
// still do not become the agent's text.
func TestQwenUnreadableCompressionIsConsumedSilently(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(metaChunk(t, "Compressing context...", map[string]any{"contextCompression": "text"}))
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)

	assert.Empty(t, sink.Notifications())
	assert.Empty(t, assembledTexts(&sink.Sink))
}

func TestQwenRetryAttemptCase(t *testing.T) {
	t.Parallel()
	// The recorded mid-stream retry: the failed attempt's answer is discarded
	// and the retry row resolves at the successful attempt's first content.
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name:       "a retry discards the failed attempt and resolves at the next content",
		FramesPath: "testdata/retry_attempt.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a, _, _ := newQwenAgent(t, nil, nil)
			a.SetSinkForTest(agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
			a.SetPromptActiveForTest(true)
			return func(frame []byte) {
				var envelope struct {
					Result *json.RawMessage                  `json:"result"`
					Error  *providerkit.JSONRPCResponseError `json:"error"`
					ID     *int64                            `json:"id"`
				}
				if json.Unmarshal(frame, &envelope) != nil {
					return
				}
				if envelope.Result != nil && envelope.ID != nil {
					a.FinishPromptRequestForTest(qwenTestSession, *envelope.Result, nil)
					return
				}
				a.HandleOutput(frame)
			}
		},
		Want:      []agenttest.RetryAttemptText{{Text: "ANSWER-AFTER-RETRY", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"partial reasoning "},
	})
}
