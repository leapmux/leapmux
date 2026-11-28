package acp

import (
	"bytes"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlushBufferedOutputKeepsTheRequestedCompletionAndOrder(t *testing.T) {
	var stdin bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&stdin))
	b.appendAssistant("answer", "message")
	b.appendThought("thought", "reasoning")
	var ids []string
	b.hooks.SegmentText = func(_ agent.AssembledMessageKind, id, text string) string { ids = append(ids, id); return text }
	b.FlushBufferedOutput(agent.MessageCompletionInterrupted)
	assert.Equal(t, []string{"reasoning", "message"}, ids)
	assert.Equal(t, []string{"thought:thought", "text:answer"}, assembledTexts(t, sink.Messages()))
	for _, message := range sink.Messages() {
		_, _, completion, ok := agenttest.DecodeAssembledMessage(message.Content)
		require.True(t, ok)
		assert.Equal(t, agent.MessageCompletionInterrupted, completion)
	}
	b.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Len(t, sink.Messages(), 2)
	assert.Len(t, sink.ProgressUpdates(), 2)
}

func TestFlushBufferedOutputPreservesTheNativeAssistantKey(t *testing.T) {
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
	var assigned []string
	b.hooks.AssistantTextKey = func(messageID string) string {
		assigned = append(assigned, messageID)
		return "reply:" + messageID
	}
	b.appendThought("reasoning", "native-thought")
	b.appendAssistant("the verified answer", "native-assistant")
	b.FlushBufferedOutput(agent.MessageCompletionComplete)
	b.appendAssistant("an unidentified fragment", "")
	b.FlushBufferedOutput(agent.MessageCompletionComplete)

	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"native-assistant"}, assigned)
	assert.Empty(t, rows[0].IdempotencyKey)
	assert.Equal(t, "reply:native-assistant", rows[1].IdempotencyKey)
	assert.Empty(t, rows[2].IdempotencyKey)
	assert.Equal(t, []string{"thought:reasoning", "text:the verified answer", "text:an unidentified fragment"}, assembledTexts(t, rows))
}

func TestFlushNativeReplyOutputWaitsForBothBuffersToBelongToTheReply(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
	b.appendThought("reasoning", "native-thought")
	b.appendAssistant("answer", "native-answer")
	owned := map[string]bool{"native-thought": true, "native-answer": true}

	b.FlushNativeReplyOutput("old-session", owned)
	b.FlushNativeReplyOutput("session-1", map[string]bool{"native-thought": true})
	b.FlushNativeReplyOutput("session-1", map[string]bool{"native-answer": true})
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ProgressUpdates())

	b.FlushNativeReplyOutput("session-1", owned)
	b.FlushNativeReplyOutput("session-1", owned)
	assert.Equal(t, []string{"thought:reasoning", "text:answer"}, assembledTexts(t, sink.Messages()))
	assert.Len(t, sink.ProgressUpdates(), 2)
}

func TestDiscardBufferedOutputResetsEachSelectedScope(t *testing.T) {
	for _, reasoningOnly := range []bool{false, true} {
		b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
		b.appendAssistant("answer", "message")
		b.appendThought("thought", "reasoning")
		if reasoningOnly {
			b.DiscardBufferedOutput(agent.AssembledMessageKindReasoning, agent.AssembledMessageKindReasoning)
		} else {
			b.DiscardBufferedOutput()
		}
		assert.Empty(t, sink.Messages())
		want := []agent.ProgressUpdate{agent.ResetModelScopeProgress("acp:agent_thought_chunk")}
		if !reasoningOnly {
			want = append(want, agent.ResetModelScopeProgress("acp:agent_message_chunk"))
		}
		assert.Equal(t, want, sink.ProgressUpdates())
		b.FlushBufferedOutput(agent.MessageCompletionComplete)
		if reasoningOnly {
			assert.Equal(t, []string{"text:answer"}, assembledTexts(t, sink.Messages()))
		} else {
			assert.Empty(t, sink.Messages())
		}
	}
}

func TestDiscardBufferedOutputIgnoresKindsWithoutAnACPBuffer(t *testing.T) {
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
	b.appendAssistant("answer", "message")
	b.DiscardBufferedOutput(agent.AssembledMessageKindPlan, agent.AssembledMessageKind("future"))
	assert.Empty(t, sink.ProgressUpdates())
	b.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Equal(t, []string{"text:answer"}, assembledTexts(t, sink.Messages()))
}

func TestFlushBufferedOutputHonorsDiscardMode(t *testing.T) {
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
	b.appendAssistant("discarded", "message")
	b.DiscardOutput()
	b.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Empty(t, sink.Messages())
	assert.Equal(t, []agent.ProgressUpdate{agent.ResetModelScopeProgress("acp:agent_message_chunk")}, sink.ProgressUpdates())
}

func TestFlushBufferedOutputOffReaderHoldsTheDispatchMutex(t *testing.T) {
	assertACPFlushWaitsForDispatch(t, func(b *Base) { b.FlushBufferedOutputOffReader(agent.MessageCompletionComplete) })
}

func assertACPFlushWaitsForDispatch(t *testing.T, flush func(*Base)) {
	t.Helper()
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&bytes.Buffer{}))
	b.appendAssistant("answer", "message")
	inspect := make(chan struct{})
	b.hooks.SegmentText = func(_ agent.AssembledMessageKind, _ string, text string) string {
		<-inspect
		if b.sessionUpdates.mu.TryLock() {
			b.sessionUpdates.mu.Unlock()
			t.Error("the off-reader flush did not hold the session-update mutex")
		}
		return text
	}
	b.sessionUpdates.mu.Lock()
	done := make(chan struct{})
	go func() { flush(b); close(done) }()
	assert.Empty(t, sink.Messages())
	b.sessionUpdates.mu.Unlock()
	close(inspect)
	select {
	case <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the off-reader flush did not finish")
	}
	assert.Equal(t, []string{"text:answer"}, assembledTexts(t, sink.Messages()))
}
