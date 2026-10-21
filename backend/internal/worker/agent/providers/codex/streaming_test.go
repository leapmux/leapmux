package codex

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture trims PROBE_CUT_TEXT from .tmp/probe/codex/logs/run1-frames.log, captured on 2026-10-17.
func codexRetryFrames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/retry_attempt.jsonl")
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func codexRetryText(row agenttest.Message) (agenttest.RetryAttemptText, bool) {
	if kind, text, completion, ok := agenttest.DecodeAssembledMessage(row.Content); ok {
		return agenttest.RetryAttemptText{Text: text, Completion: completion}, kind == agent.AssembledMessageKindText
	}
	var payload struct {
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(row.Content, &payload) != nil || payload.Item.Type != "agentMessage" {
		return agenttest.RetryAttemptText{}, false
	}
	return agenttest.RetryAttemptText{Text: payload.Item.Text, Completion: agent.MessageCompletionComplete}, true
}

func TestCodexRetryAttemptCase(t *testing.T) {
	t.Parallel()
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "failed streamed attempt", FramesPath: "testdata/retry_attempt.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			t.Helper()
			a := newCodexAgentWithSink(agent.NewProviderServices(sink))
			return func(frame []byte) { handleCodexOutput(a, providerkit.ParseLine(frame)) }
		},
		TextOf:    codexRetryText,
		Want:      []agenttest.RetryAttemptText{{Text: "OK", Completion: agent.MessageCompletionComplete}},
		Discarded: []string{"partial-4"},
	})
}

func TestHandleCodexOutput_ARetriedStreamDiscardsTheFailedAttempt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a, codexRetryFrames(t)...)
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), `"text":"OK"`)
	assert.Contains(t, string(sink.Messages()[1].Content), `"status":"completed"`)
}

func TestHandleCodexOutput_ASilentWebSocketRetryDiscardsTheFailedAttempt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	for _, frame := range codexRetryFrames(t) {
		if !strings.Contains(frame, `"method":"error"`) {
			feedCodexLines(a, frame)
		}
	}
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), `"text":"OK"`)
}

func TestHandleCodexOutput_ARetriedReasoningStreamDiscardsTheFailedReasoning(t *testing.T) {
	t.Parallel()
	// PROBE_CUT_REASONING from the same recorded run as codexRetryFrames.
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"reasoning","id":"rs_6","summary":[],"content":[]}}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"rs_6","delta":"partial-reasoning-6 ","summaryIndex":0}}`,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"Reconnecting... 1/3"}}}`,
	)
	assert.Empty(t, a.generationBuffer.ScopeIDs())
	assert.NotContains(t, a.reasoningStreamKind, codexReasoningKey("main-thread", "rs_6"))
	assert.NotContains(t, a.reasoningSummaryIndex, codexReasoningKey("main-thread", "rs_6"))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"msg_7","text":""}}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"msg_7","text":"OK"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`,
	)
	require.Len(t, sink.Messages(), 2)
	for _, row := range sink.Messages() {
		assert.NotContains(t, string(row.Content), "partial-reasoning")
	}
}

func TestHandleCodexOutput_ARetryOfAnUnroutedChildDiscardsItsFailedAttempt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	for _, frame := range codexRetryFrames(t) {
		feedCodexLines(a, strings.ReplaceAll(frame, "main-thread", "child-1"))
	}
	assert.Empty(t, sink.Messages())
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)
	child := sink.Child("child-of-spawn-1")
	require.NotNil(t, child)
	require.Len(t, child.Messages(), 2)
	assert.Contains(t, string(child.Messages()[0].Content), `"text":"OK"`)
	for _, row := range child.Messages() {
		assert.NotContains(t, string(row.Content), "partial-4")
	}
}

func TestHandleCodexOutput_ExhaustedRetriesKeepOnlyTheLastAttemptAsAnError(t *testing.T) {
	t.Parallel()
	// PROBE_CUT_ALWAYS from the same recorded run as codexRetryFrames.
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"msg_11","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"msg_11","delta":"partial-11 "}}`,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"Reconnecting... 1/1"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"msg_12","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"msg_12","delta":"partial-12 "}}`,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":false,"error":{"message":"stream disconnected"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","error":{"message":"stream disconnected"}}}}`,
	)
	require.Len(t, sink.Messages(), 2)
	kind, text, completion, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, agent.AssembledMessageKindText, kind)
	assert.Equal(t, "partial-12 ", text)
	assert.Equal(t, agent.MessageCompletionError, completion)
}

func TestHandleCodexOutput_ARetriedPlanItemRestartsItsScope(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"plan","id":"turn-plan","text":""}}}`,
		`{"method":"item/plan/delta","params":{"threadId":"main-thread","itemId":"turn-plan","delta":"discarded plan"}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"plan","id":"turn-plan","text":""}}}`,
		`{"method":"item/plan/delta","params":{"threadId":"main-thread","itemId":"turn-plan","delta":"new plan"}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed"}}}`,
	)
	require.Len(t, sink.Messages(), 2)
	_, text, _, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, "new plan", text)
}

func TestHandleCodexOutput_APlanAndAMessageOpenTogetherDiscardNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/plan/delta","params":{"threadId":"main-thread","itemId":"turn-plan","delta":"plan"}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"msg-1","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"msg-1","delta":"answer"}}`,
	)
	assert.Equal(t, []string{"turn-plan", "msg-1"}, a.generationBuffer.ScopeIDs())
	assert.NotContains(t, sink.ProgressUpdates(), agent.ResetModelScopeProgress("turn-plan"))
}

func TestHandleCodexOutput_ARetryDiscardResetsTheScopeProgress(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a, codexRetryFrames(t)[:3]...)
	assert.Empty(t, a.generationBuffer.ScopeIDs())
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetModelScopeProgress("msg_4"))
}

func TestHandleCodexOutput_ARetryReleasesTheUnroutedChildBufferBudget(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.bufferCodexModelText("first", "child-1", strings.Repeat("x", codexPendingChildGenerationLimit), agent.AssembledMessageKindText, providerkit.JoinVerbatim, true)
	feedCodexLines(a, `{"method":"error","params":{"threadId":"child-1","willRetry":true,"error":{"message":"Reconnecting... 1/3"}}}`)
	a.bufferCodexModelText("second", "child-1", "OK", agent.AssembledMessageKindText, providerkit.JoinVerbatim, true)
	buffer := a.codexChildGenerationBuffer("child-1")
	assert.Equal(t, []string{"second"}, buffer.ScopeIDs())
	_, text, present := buffer.ScopeSnapshot("second")
	assert.True(t, present)
	assert.Equal(t, "OK", text)
}

type failedGenerationSink struct {
	agenttest.Sink
	writes int
}

func (s *failedGenerationSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.writes++
	if s.writes == 2 {
		return errors.New("the second write failed")
	}
	return s.Sink.PersistMessage(source, content, span)
}

func TestCodexGenerationFlushStopsAtFailureAndCompletesProgress(t *testing.T) {
	t.Parallel()
	for _, dropped := range []bool{false, true} {
		t.Run(map[bool]string{false: "turn end", true: "dropped response"}[dropped], func(t *testing.T) {
			t.Parallel()
			sink := &failedGenerationSink{}
			a := newCodexAgentWithSink(agent.NewProviderServices(sink))
			for _, scope := range []string{"first", "second", "third"} {
				a.generationBuffer.Append(scope, agent.AssembledMessageKindText, scope, providerkit.JoinVerbatim)
			}
			if dropped {
				a.persistDroppedCodexGeneration(&a.generationBuffer, sink, a.generationBuffer.ScopeIDs())
			} else {
				a.persistCodexGeneration(&a.generationBuffer, sink, agent.MessageCompletionComplete)
			}
			assert.Equal(t, 2, sink.writes)
			require.Len(t, sink.Messages(), 1)
			assert.Contains(t, string(sink.Messages()[0].Content), `"text":"first"`)
			assert.Empty(t, a.generationBuffer.ScopeIDs())
			assert.True(t, a.generationBuffer.HasPendingWrites())
			assert.Equal(t, []agent.ProgressUpdate{
				agent.CompleteModelProgress("first"), agent.CompleteModelProgress("second"), agent.CompleteModelProgress("third"),
			}, sink.ProgressUpdates())
		})
	}
}

func TestCodexRetryDiscardKeepsTextWhoseInterruptedWriteFailed(t *testing.T) {
	t.Parallel()
	sink := &failedGenerationSink{writes: 1}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"first","delta":"text the model kept"}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"userMessage","id":"steer-1","content":[{"type":"text","text":"continue"}]}}}`,
	)
	assert.Empty(t, a.generationBuffer.ScopeIDs())
	assert.True(t, a.generationBuffer.HasPendingWrites(), "the failed write retains the interrupted answer")
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"second","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"second","delta":"next answer"}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`,
	)
	var texts []string
	for _, row := range sink.Messages() {
		if value, ok := codexRetryText(row); ok {
			texts = append(texts, value.Text)
		}
	}
	assert.Equal(t, []string{"text the model kept", "next answer"}, texts)
}

func TestCodexGenerationRetryKeepsTheOriginalCompletion(t *testing.T) {
	t.Parallel()
	sink := &failedGenerationSink{writes: 1}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.generationBuffer.Append("first", agent.AssembledMessageKindText, "interrupted answer", providerkit.JoinVerbatim)
	a.persistDroppedCodexGeneration(&a.generationBuffer, sink, []string{"first"})
	require.Empty(t, sink.Messages())
	a.persistCodexGeneration(&a.generationBuffer, sink, agent.MessageCompletionComplete)
	require.Len(t, sink.Messages(), 1)
	_, text, completion, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
	require.True(t, ok)
	assert.Equal(t, "interrupted answer", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion)
}

type codexRetryRecordingSink struct {
	agenttest.Sink
	order       []string
	rejectRetry bool
}

func (s *codexRetryRecordingSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if s.rejectRetry {
		return false, errors.New("the retry notice write failed")
	}
	s.order = append(s.order, "retry")
	return s.Sink.PersistNotification(source, content)
}

func (s *codexRetryRecordingSink) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	changed, err := s.Sink.PersistWorkerNotification(content)
	if err == nil {
		s.order = append(s.order, "resolved")
	}
	return changed, err
}

func (s *codexRetryRecordingSink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.order = append(s.order, "resolved")
	s.Sink.PersistLeapMuxNotification(content)
}

func (s *codexRetryRecordingSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.order = append(s.order, "message")
	return s.Sink.PersistMessage(source, content, span)
}

func TestHandleCodexOutput_TheFirstItemAfterARetryResolvesTheRetryRow(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"item/started", "item/completed"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			sink := &codexRetryRecordingSink{}
			a := newCodexAgentWithSink(agent.NewProviderServices(sink))
			feedCodexLines(a, `{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"Reconnecting... 1/3"}}}`)
			feedCodexLines(a, `{"method":"`+method+`","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"answer","text":"OK"}}}`)
			require.Len(t, sink.LeapMuxNotifications(), 1)
			assert.Equal(t, "api_retry_resolved", sink.LeapMuxNotifications()[0]["type"])
			require.GreaterOrEqual(t, len(sink.order), 2)
			assert.Equal(t, []string{"retry", "resolved"}, sink.order[:2])
			feedCodexLines(a, `{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"another","text":"next"}}}`)
			assert.Len(t, sink.LeapMuxNotifications(), 1)
		})
	}
}

func TestHandleCodexOutput_AFailedTurnLeavesTheRetryRowUnresolved(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"retry"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"next-turn","text":""}}}`,
	)
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestHandleCodexOutput_AQueuedChildResolvesItsRetryAfterTheRetryRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"error","params":{"threadId":"child-1","willRetry":true,"error":{"message":"retry"}}}`,
		`{"method":"item/started","params":{"threadId":"child-1","item":{"type":"agentMessage","id":"answer","text":""}}}`,
		`{"method":"item/completed","params":{"threadId":"child-1","item":{"type":"agentMessage","id":"answer","text":"OK"}}}`,
	)
	assert.Empty(t, sink.LeapMuxNotifications())
	assert.Empty(t, sink.PersistedNotifications())
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)
	child := sink.Child("child-of-spawn-1")
	require.NotNil(t, child)
	require.Len(t, child.PersistedNotifications(), 1)
	assert.Contains(t, string(child.PersistedNotifications()[0].Content), `"willRetry":true`)
	require.Len(t, child.LeapMuxNotifications(), 1)
	assert.Equal(t, "api_retry_resolved", child.LeapMuxNotifications()[0]["type"])
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestHandleCodexOutput_AFailedRetryNoticeWriteCannotResolve(t *testing.T) {
	t.Parallel()
	sink := &codexRetryRecordingSink{rejectRetry: true}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"retry"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"answer","text":""}}}`,
	)
	assert.Empty(t, sink.LeapMuxNotifications())
}
