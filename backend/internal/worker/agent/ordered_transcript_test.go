package agent_test

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderedTranscriptPreservesPayloadSourceAndDestination(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	first, second := &agenttest.Sink{PersistErr: assert.AnError}, &agenttest.Sink{}
	writer := agent.OrderedTranscript(&queue, first)
	raw := []byte(`{"text":"original"}`)
	err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, AgentSessionID: "old"}, agent.SpanInfo{SpanID: "tool"})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	clear(raw)
	next := agent.OrderedTranscript(&queue, second)
	err = next.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})
	require.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, second.Messages())
	first.PersistErr = nil
	require.NoError(t, queue.Flush())
	messages := first.Messages()
	assert.JSONEq(t, `{"text":"original"}`, string(messages[len(messages)-1].Content))
	assert.Equal(t, "old", messages[len(messages)-1].AgentSessionID)
	assert.Equal(t, "tool", messages[len(messages)-1].SpanID)
	require.Len(t, second.Messages(), 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, second.Messages()[0].Source)
}

func TestOrderedTranscriptDelaysTurnReleaseAndSteerEcho(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.ErrorIs(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"end"}`)}, agent.SpanInfo{}), assert.AnError)
	services.ReportSteersEchoed(1)
	services.SetTurnState(agent.TurnState{}, 2)
	assert.Empty(t, sink.SteerEchoReports())
	active, _ := sink.LastTurnActive()
	assert.True(t, active)
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	active, _ = sink.LastTurnActive()
	assert.False(t, active)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	lifecycle := sink.TurnLifecycle()
	assert.Equal(t, []string{"turn_end", "steer-echoed:1", "turn_active:false"}, lifecycle[len(lifecycle)-3:])
}

func TestOrderedTranscriptRetryOfACapturedWriteDoesNotQueueAnotherCopy(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	record := agent.CaptureTranscript(agent.OrderedTranscript(&queue, sink), agent.MessageContent{Original: []byte(`{"text":"once"}`)}, agent.SpanInfo{})
	require.ErrorIs(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT), assert.AnError)
	require.Len(t, sink.Messages(), 1)
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	require.NoError(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	require.Len(t, sink.Messages(), 2, "one failed attempt and one successful write")
}

func TestOrderedTranscriptKeepsTheOldRowAndSkipsAnEffectAfterSessionReplacement(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	sink.UpdateSessionID("old")
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Error(t, services.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"old"}`)}, agent.SpanInfo{}))
	services.ScheduleAutoContinue(agent.AutoContinueSchedule{Reason: agent.AutoContinueReasonAPIError})
	sink.UpdateSessionID("new")
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	messages := sink.Messages()
	assert.Equal(t, "old", messages[len(messages)-1].AgentSessionID)
	assert.Zero(t, sink.AutoScheduleCount())
	active, _ := sink.LastTurnActive()
	assert.True(t, active)
}

type batchMutationSink struct {
	*agenttest.Sink
	duringCapture func()
}

func (sink *batchMutationSink) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	content = sink.Sink.CaptureMessage(content, span)
	if callback := sink.duringCapture; callback != nil {
		sink.duringCapture = nil
		callback()
	}
	return content
}

func TestPersistTranscriptBatchCopiesEveryPayloadBeforeCapture(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{}}
	second := []byte(`{"text":"second"}`)
	sink.duringCapture = func() { clear(second) }
	ordered := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	require.NoError(t, agent.PersistTranscriptBatch(ordered, []agent.TranscriptMessage{
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: []byte(`{"text":"first"}`)}},
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: second}},
	}))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.JSONEq(t, `{"text":"second"}`, string(messages[1].Content))
}

func TestPersistPreparedMessageKeepsTheCapturedSessionAndSelectedBytesOnRetry(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	sink.UpdateSessionID("old")
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	raw := []byte(`{"text":"selected"}`)
	var preparations int
	err := agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"text":"before selection"}`)}, agent.SpanInfo{}, func() []byte {
			preparations++
			sink.UpdateSessionID("new")
			return raw
		})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	clear(raw)
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.Equal(t, 1, preparations)
	require.Len(t, sink.Messages(), 2)
	for _, message := range sink.Messages() {
		assert.Equal(t, "old", message.AgentSessionID)
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, message.Source)
		assert.JSONEq(t, `{"text":"selected"}`, string(message.Content))
	}
}

func TestPersistPreparedMessageCanOmitItsRowWithoutBlockingTheNextRow(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	require.NoError(t, agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"text":"summary"}`)}, agent.SpanInfo{}, func() []byte {
			err := writer.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, agent.SpanInfo{})
			assert.True(t, agent.OutputRetained(err))
			assert.Empty(t, sink.Messages())
			return nil
		}))
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
	assert.False(t, queue.HasPending())
}

func TestPersistPreparedMessageReservesBeforeCaptureReentry(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{}}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	sink.duringCapture = func() {
		err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})
		assert.True(t, agent.OutputRetained(err))
		assert.Empty(t, sink.Messages())
	}
	require.NoError(t, agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{}, agent.SpanInfo{}, func() []byte { return []byte(`{"text":"first"}`) }))
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, sink.Messages()[0].Source)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, sink.Messages()[1].Source)
}

func TestPersistPreparedMessageRejectsInvalidAdmission(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	writer := agent.OrderedTranscript(&queue, &agenttest.Sink{})
	prepare := func() []byte { t.Error("an invalid message reached preparation"); return nil }
	for _, source := range []leapmuxv1.MessageSource{leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED, 999} {
		require.Error(t, agent.PersistPreparedMessage(writer, source, agent.MessageContent{}, agent.SpanInfo{}, prepare))
	}
	require.Error(t, agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{}, agent.SpanInfo{}, nil))
	require.Error(t, agent.PersistPreparedMessage(nil, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{}, agent.SpanInfo{}, prepare))
	assert.False(t, queue.HasPending())
}

func TestOrderedChildFailureDoesNotBlockAnotherTranscript(t *testing.T) {
	t.Parallel()
	root := &agenttest.Sink{}
	ordered := agent.NewModelProgressResetSink(agent.NewProviderServices(root))
	child := ordered.ChildSink("one")
	rawChild := root.Child("one")
	rawChild.PersistErr = assert.AnError
	require.ErrorIs(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"child"}`)}, agent.SpanInfo{}), assert.AnError)
	require.NoError(t, ordered.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"root"}`)}, agent.SpanInfo{}))
	require.NoError(t, ordered.ChildSink("two").PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"other child"}`)}, agent.SpanInfo{}))
	assert.Len(t, root.Messages(), 1)
	assert.Len(t, root.Child("two").Messages(), 1)
	rawChild.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(ordered.ChildSink("one")))
	assert.Len(t, rawChild.Messages(), 2, "one failed attempt and one successful write")
}

func TestPersistTranscriptBatchRejectsInvalidMetadataBeforeItAdmitsAnyRow(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{}
	ordered := agent.NewModelProgressResetSink(agent.NewProviderServices(raw))
	err := agent.PersistTranscriptBatch(ordered, []agent.TranscriptMessage{
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: []byte(`{}`)}},
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: []byte(`{}`), Completion: "invalid"}},
	})
	require.ErrorContains(t, err, "unknown completion")
	assert.False(t, agent.OutputRetained(err))
	assert.Empty(t, raw.Messages())
	require.NoError(t, ordered.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"valid"}`)}, agent.SpanInfo{}))
	assert.Len(t, raw.Messages(), 1)
}

func TestOrderedSteerReportsIgnoreNonpositiveCountsWithoutRetryingOutput(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{PersistErr: assert.AnError}
	sink := agent.NewModelProgressResetSink(agent.NewProviderServices(raw))
	require.Error(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{}))
	raw.PersistErr = nil
	sink.ReportSteersEchoed(0)
	sink.ReportSteersDropped(-1)
	sink.ReportSteersUncertain(0)
	sink.ReportSteersUncertain(-1)
	assert.Empty(t, raw.SteerEchoReports())
	assert.Len(t, raw.Messages(), 1, "a nonpositive report must not start an unrelated retry")
	require.NoError(t, agent.FlushPendingOutput(sink))
	assert.Len(t, raw.Messages(), 2)
}

func TestOrderedTranscriptRetriesUncertaintyBeforeLaterRows(t *testing.T) {
	t.Parallel()
	raw := &agenttest.Sink{PersistErr: assert.AnError}
	sink := agent.NewModelProgressResetSink(agent.NewProviderServices(raw))
	sink.ReportSteersUncertain(2)
	require.ErrorIs(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{}), assert.AnError)
	assert.Empty(t, raw.SteerEchoReports())
	assert.Empty(t, raw.Messages())
	raw.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(sink))
	require.NoError(t, agent.FlushPendingOutput(sink))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 2}}, raw.SteerEchoReports())
	assert.Equal(t, []string{"steer-uncertain:2", "turn_end"}, raw.TurnLifecycle())
}
