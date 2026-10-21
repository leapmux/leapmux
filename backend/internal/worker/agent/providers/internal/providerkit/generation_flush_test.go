package providerkit

import (
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type generationFlushSink struct {
	agent.TranscriptServices
	rows       []agent.MessageContent
	progress   []agent.ProgressUpdate
	failAt     int
	onProgress func()
	onCapture  func()
}

func (s *generationFlushSink) CaptureMessage(content agent.MessageContent, _ agent.SpanInfo) agent.MessageContent {
	if s.onCapture != nil {
		s.onCapture()
	}
	return content.Clone()
}

func (s *generationFlushSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT || span != (agent.SpanInfo{}) {
		return errors.New("invalid generation row")
	}
	if s.failAt > 0 && len(s.rows)+1 == s.failAt {
		return errors.New("write failed")
	}
	s.rows = append(s.rows, content)
	return nil
}

func (s *generationFlushSink) ReportProgress(update agent.ProgressUpdate) {
	s.progress = append(s.progress, update)
	if s.onProgress != nil {
		s.onProgress()
	}
}

func TestPersistGenerationScopesWritesInOrderAndCompletesProgress(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("first", agent.AssembledMessageKindText, "one", JoinVerbatim)
	buffer.Append("second", agent.AssembledMessageKindReasoning, "two", JoinVerbatim)
	sink := &generationFlushSink{}
	require.NoError(t, PersistGenerationScopes(&buffer, sink, []string{"second", "first", "missing"}, agent.MessageCompletionInterrupted, false))
	require.Len(t, sink.rows, 2)
	assert.Equal(t, "two", StringField(sink.rows[0].Original, "text"))
	assert.Equal(t, "reasoning", StringField(sink.rows[0].Original, "kind"))
	assert.Equal(t, "one", StringField(sink.rows[1].Original, "text"))
	assert.Equal(t, "interrupted", StringField(sink.rows[1].Original, "completion"))
	assert.Equal(t, []agent.ProgressUpdate{agent.CompleteModelProgress("second"), agent.CompleteModelProgress("first"), agent.CompleteModelProgress("missing")}, sink.progress)
	assert.Empty(t, buffer.ScopeIDs())
}

func TestPersistGenerationScopesStopsAtFirstFailedWriteAndStillCompletesProgress(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	for _, scope := range []string{"first", "second", "third"} {
		buffer.Append(scope, agent.AssembledMessageKindText, scope, JoinVerbatim)
	}
	sink := &generationFlushSink{failAt: 2}
	require.ErrorContains(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false), `scope "second"`)
	require.Len(t, sink.rows, 1)
	assert.Equal(t, "first", StringField(sink.rows[0].Original, "text"))
	assert.Empty(t, buffer.ScopeIDs())
	assert.True(t, buffer.HasPendingWrites())
	assert.Equal(t, []agent.ProgressUpdate{agent.CompleteModelProgress("first"), agent.CompleteModelProgress("second"), agent.CompleteModelProgress("third")}, sink.progress)
	sink.failAt = 0
	require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false))
	require.Len(t, sink.rows, 3)
	assert.Equal(t, "second", StringField(sink.rows[1].Original, "text"))
	assert.Equal(t, "third", StringField(sink.rows[2].Original, "text"))
}

func TestPersistGenerationScopesDiscardResetsProgressAndKeepsUnlistedScopes(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("drop", agent.AssembledMessageKindText, "discard", JoinVerbatim)
	buffer.Append("keep", agent.AssembledMessageKindText, "retain", JoinVerbatim)
	sink := &generationFlushSink{}
	require.NoError(t, PersistGenerationScopes(&buffer, sink, []string{"drop"}, agent.MessageCompletionComplete, true))
	assert.Empty(t, sink.rows)
	assert.Equal(t, []string{"keep"}, buffer.ScopeIDs())
	assert.Equal(t, []agent.ProgressUpdate{agent.ResetModelScopeProgress("drop")}, sink.progress)
}

func TestFlushGenerationDiscardKeepsASegmentAppendedDuringTheDiscard(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("old", agent.AssembledMessageKindText, "old", JoinVerbatim)
	sink := &generationFlushSink{onProgress: func() {
		buffer.Append("new", agent.AssembledMessageKindText, "new", JoinVerbatim)
	}}
	require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, true))
	assert.Equal(t, []string{"new"}, buffer.ScopeIDs())
	assert.Empty(t, sink.rows)
}

func TestFlushGenerationEmptyBufferWritesNothing(t *testing.T) {
	t.Parallel()
	for _, discarding := range []bool{false, true} {
		var buffer GenerationBuffer
		sink := &generationFlushSink{}
		require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, discarding))
		assert.Empty(t, sink.rows)
		assert.Empty(t, sink.progress)
	}
}

func TestFlushGenerationKeepsTheOriginalSessionAcrossAFailedWrite(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	sink.UpdateSessionID("old-session")
	buffer.Append("a", agent.AssembledMessageKindText, "earlier", JoinVerbatim)
	buffer.Append("b", agent.AssembledMessageKindText, "also earlier", JoinVerbatim)
	require.ErrorIs(t, FlushGeneration(&buffer, sink, agent.MessageCompletionInterrupted, false), assert.AnError)
	sink.UpdateSessionID("new-session")
	sink.PersistErr = nil
	require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false))
	messages := sink.Messages()
	require.Len(t, messages, 3)
	for _, message := range messages {
		assert.Equal(t, "old-session", message.AgentSessionID)
	}
}

func TestFlushGenerationReservesTheBatchBeforeAReentrantNativeCapture(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("a", agent.AssembledMessageKindText, "earlier", JoinVerbatim)
	buffer.Append("b", agent.AssembledMessageKindText, "second", JoinVerbatim)
	sink := &generationFlushSink{}
	sink.onCapture = func() {
		sink.onCapture = nil
		writer := agent.OrderedTranscript(buffer.OutputQueue(), sink)
		err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"later"}`)}, agent.SpanInfo{})
		assert.True(t, agent.OutputRetained(err))
	}
	require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false))
	require.Len(t, sink.rows, 3)
	assert.Equal(t, "earlier", StringField(sink.rows[0].Original, "text"))
	assert.Equal(t, "second", StringField(sink.rows[1].Original, "text"))
	assert.Equal(t, "later", StringField(sink.rows[2].Original, "text"))
}

func TestFlushGenerationSubmitsEveryScopeBeforeLaterNativeRows(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	var queue agent.OutputQueue
	raw := &agenttest.Sink{PersistErr: assert.AnError}
	sink := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(raw))
	buffer.Append("first", agent.AssembledMessageKindText, "first", JoinVerbatim)
	buffer.Append("second", agent.AssembledMessageKindText, "second", JoinVerbatim)
	require.Error(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false))
	raw.PersistErr = nil
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"native"}`)}, agent.SpanInfo{}))
	require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false))
	messages := raw.Messages()
	require.Len(t, messages, 4)
	assert.Equal(t, "first", StringField(messages[1].Content, "text"))
	assert.Equal(t, "second", StringField(messages[2].Content, "text"))
	assert.Equal(t, "native", StringField(messages[3].Content, "text"))
}

type forwardingGenerationSink struct {
	agent.ProviderServices
	writes int
}

func (sink *forwardingGenerationSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	sink.writes++
	return sink.ProviderServices.PersistMessage(source, content, span)
}

func TestFlushGenerationKeepsAnOuterDecoratorWithoutEnqueueingAgain(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	raw := &agenttest.Sink{}
	sink := &forwardingGenerationSink{ProviderServices: agent.NewModelProgressResetSink(agent.NewProviderServices(raw))}
	buffer.Append("a", agent.AssembledMessageKindText, "first", JoinVerbatim)
	buffer.Append("b", agent.AssembledMessageKindText, "second", JoinVerbatim)
	require.NoError(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false))
	require.Len(t, raw.Messages(), 2)
	assert.Equal(t, 2, sink.writes)
	assert.False(t, buffer.OutputQueue().HasPending())
}

type generationSessionReplacingSink struct{ *agenttest.Sink }

func (sink generationSessionReplacingSink) PersistMessage(leapmuxv1.MessageSource, agent.MessageContent, agent.SpanInfo) error {
	sink.UpdateSessionID("new")
	sink.ReportProgress(agent.ModelTextProgress("same-scope", "new session text"))
	return assert.AnError
}

func TestFlushGenerationDoesNotCompleteAReplacementSessionsScope(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	raw := &agenttest.Sink{}
	raw.UpdateSessionID("old")
	sink := generationSessionReplacingSink{Sink: raw}
	buffer.Append("same-scope", agent.AssembledMessageKindText, "old session text", JoinVerbatim)
	require.ErrorIs(t, FlushGeneration(&buffer, sink, agent.MessageCompletionComplete, false), assert.AnError)
	assert.Equal(t, []agent.ProgressUpdate{agent.ModelTextProgress("same-scope", "new session text")}, raw.ProgressUpdates())
}
