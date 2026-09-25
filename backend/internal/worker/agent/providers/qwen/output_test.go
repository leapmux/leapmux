package qwen

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

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
		kind, text, ok := decodeAssembledText(message.Content)
		if ok && kind == "text" {
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

func TestQwenCompressionReachesTheTranscriptAsStatus(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(metaChunk(t, "Compressing context...", map[string]any{"contextCompression": map[string]any{"phase": "progress"}, "source": "slash_command"}))
	a.HandleOutput(metaChunk(t, "Context compressed (900 -> 300).", map[string]any{"contextCompression": map[string]any{
		"phase": "done", "originalTokenCount": 900, "newTokenCount": 300,
	}}))
	a.HandleOutput(metaChunk(t, "Context compressed (900 -> 850).", map[string]any{"contextCompression": map[string]any{
		"phase": "done", "originalTokenCount": 900, "newTokenCount": 850, "warning": "little was saved",
	}}))
	a.HandleOutput(metaChunk(t, "x", map[string]any{"contextCompression": map[string]any{"phase": "unknown"}}))

	var texts []string
	for _, notification := range sink.Notifications() {
		assert.Equal(t, contracts.NotificationTypeAgentStatus, notification[contracts.NotificationFieldType])
		texts = append(texts, notification[contracts.NotificationFieldText].(string))
	}
	assert.Equal(t, []string{
		"Compacting the context",
		"Context compacted from 900 to 300 tokens",
		"Context compacted from 900 to 850 tokens: little was saved",
	}, texts)
	a.FinishPromptRequestForTest(qwenTestSession, []byte(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, assembledTexts(&sink.Sink), "Qwen's progress words do not become the agent's text")
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
	a.ApplySubagentObservation(&acp.SubagentObservation{RowKey: "call_1", Title: "Helper", Status: bgtask.StatusRunning})
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
	for _, rowKey := range []string{"call_1", "shell:bg_1a2b"} {
		row, ok := sink.BackgroundTask(rowKey)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusCompleted, row.Status, rowKey)
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
	a.ApplySubagentObservation(&acp.SubagentObservation{RowKey: "call_1", Title: "Helper", Status: bgtask.StatusRunning})
	sink.LookupErr = errors.New("the registry is down")

	a.HandleOutput(backgroundNotice(t, "background_task_completed", map[string]any{
		"taskId": "general-purpose-call_1", "status": "completed", "kind": "agent", "toolUseId": "call_1", "description": "Helper",
	}))

	assert.Equal(t, []string{`Background agent "Helper" completed`}, statusTexts(sink))
	assert.Positive(t, sink.LookupBackgroundTaskCalls("call_1"), "the notice asked the registry for its row")
	row, ok := sink.BackgroundTask("call_1")
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
