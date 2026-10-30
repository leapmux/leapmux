package agent_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedBatchFailureSink struct {
	*agenttest.Sink
	rejectSecond bool
}

func (sink *capturedBatchFailureSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if sink.rejectSecond && bytes.Equal(content.Original, []byte(`{"text":"second"}`)) {
		return assert.AnError
	}
	return sink.Sink.PersistMessage(source, content, span)
}

func TestCapturedTranscriptBatchRetainsTypedOperationsAndOriginalOwnershipAcrossRetry(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &capturedBatchFailureSink{Sink: &agenttest.Sink{}, rejectSecond: true}
	sink.UpdateSessionID("old-session")
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	preparations := 0
	err := agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation {
		preparations++
		return []agent.CapturedTranscriptOperation{
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"text":"first"}`)}, agent.SpanInfo{SpanID: "archive-tool", SpanColor: 7}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT),
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"text":"second"}`)}, agent.SpanInfo{SpanID: "archive-tool", SpanColor: 7, Closing: true}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT),
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"type":"notice","text":"native notice"}`)}, agent.SpanInfo{}).NotificationOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT),
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"type":"agent_status","text":"worker notice"}`)}, agent.SpanInfo{}).WorkerNotificationOperation(),
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"type":"end"}`)}, agent.SpanInfo{}).TurnEndOperation(),
		}
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.LeapMuxNotifications())
	sink.UpdateSessionID("replacement")
	sink.ResetSpans()
	sink.rejectSecond = false
	require.NoError(t, queue.Flush())
	assert.Equal(t, 1, preparations, "retry must not capture against the replacement or repeat span effects")
	assert.Equal(t, 1, sink.ResetSpanCount())
	messages := sink.Messages()
	require.Len(t, messages, 3, "two ordinary rows and one divider, with no repeated first row")
	assert.JSONEq(t, `{"text":"first"}`, string(messages[0].Content))
	assert.JSONEq(t, `{"text":"second"}`, string(messages[1].Content))
	assert.Equal(t, "old-session", messages[1].AgentSessionID)
	assert.Equal(t, "archive-tool", messages[1].SpanID)
	assert.Equal(t, int32(7), messages[1].SpanColor)
	assert.True(t, messages[1].TranscriptOnly)
	assert.True(t, messages[2].TurnEnd)
	assert.Equal(t, "old-session", messages[2].AgentSessionID)
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Equal(t, "old-session", sink.PersistedNotifications()[0].AgentSessionID)
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, "worker notice", sink.LeapMuxNotifications()[0]["text"])
	assert.False(t, queue.HasPending())
}

func TestCapturedTranscriptBatchPrecedesCaptureReentryAndOwnsItsOperationSlice(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{PersistErr: assert.AnError}}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	sink.duringCapture = func() {
		err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})
		assert.True(t, agent.OutputRetained(err))
		assert.Empty(t, sink.Messages())
	}
	var operations []agent.CapturedTranscriptOperation
	err := agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation {
		operations = []agent.CapturedTranscriptOperation{
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"text":"first"}`)}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT),
			agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"text":"second"}`)}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT),
		}
		assert.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
		assert.Empty(t, sink.Messages())
		return operations
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	clear(operations)
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	messages := sink.Messages()
	require.Len(t, messages, 4, "one failed attempt and three successful rows")
	messages = messages[1:]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, messages[0].Source)
	assert.JSONEq(t, `{"text":"first"}`, string(messages[0].Content))
	assert.JSONEq(t, `{"text":"second"}`, string(messages[1].Content))
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, messages[2].Source)
}

func TestCapturedTranscriptBatchRejectsInvalidOperationsWithoutPoisoningTheQueue(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"zero operation", "unspecified source", "unknown source", "changed operation", "different queue"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var queue agent.OutputQueue
			sink := &agenttest.Sink{}
			writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
			capture := agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"text":"original"}`)}, agent.SpanInfo{})
			err := agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation {
				var operation agent.CapturedTranscriptOperation
				switch scenario {
				case "unspecified source":
					operation = capture.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED)
				case "unknown source":
					operation = capture.MessageOperation(999)
				case "changed operation":
					_ = capture.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
					operation = capture.NotificationOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
				case "different queue":
					other := agent.NewOrderedProviderServices(&agent.OutputQueue{}, agent.NewProviderServices(sink))
					operation = agent.CaptureTranscript(other, agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
				}
				return []agent.CapturedTranscriptOperation{operation}
			})
			require.Error(t, err)
			assert.False(t, agent.OutputRetained(err))
			assert.False(t, queue.HasPending())
			assert.Empty(t, sink.Messages())
			require.NoError(t, writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"healthy"}`)}, agent.SpanInfo{}))
			assert.Len(t, sink.Messages(), 1)
		})
	}
}

func TestCapturedTranscriptBatchPreparationPanicPreservesReentrantOutput(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	require.PanicsWithValue(t, "capture failed", func() {
		_ = agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation {
			err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})
			assert.True(t, agent.OutputRetained(err))
			panic("capture failed")
		})
	})
	require.NoError(t, queue.Flush())
	assert.False(t, queue.HasPending())
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{"content":"later"}`, string(sink.Messages()[0].Content))
}

func TestCapturedTranscriptBatchRequiresAQueueAndPreparation(t *testing.T) {
	t.Parallel()
	called := false
	require.Error(t, agent.PersistCapturedTranscriptBatch(nil, func() []agent.CapturedTranscriptOperation { called = true; return nil }))
	assert.False(t, called)
	var queue agent.OutputQueue
	writer := agent.OrderedTranscript(&queue, &agenttest.Sink{})
	require.Error(t, agent.PersistCapturedTranscriptBatch(writer, nil))
	assert.False(t, queue.HasPending())
	require.NoError(t, agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation { return nil }))
	assert.False(t, queue.HasPending())
}

func TestCapturedTranscriptReservationHoldsLaterOutputUntilPreparation(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	batch, err := agent.ReserveCapturedTranscriptBatch(writer)
	require.NoError(t, err)
	assert.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
	assert.True(t, agent.OutputRetained(writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})))
	writer.SetTurnState(agent.TurnState{}, 1)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.TurnActiveCalls)
	require.NoError(t, batch.Prepare(func() []agent.CapturedTranscriptOperation {
		return []agent.CapturedTranscriptOperation{agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"native":"first"}`)}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)}
	}))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"native":"first"}`, string(rows[0].Content))
	assert.JSONEq(t, `{"content":"later"}`, string(rows[1].Content))
	assert.Equal(t, []bool{false}, sink.TurnActiveCalls)
	called := false
	require.Error(t, batch.Prepare(func() []agent.CapturedTranscriptOperation { called = true; return nil }))
	assert.False(t, called)
	assert.False(t, batch.Discard())
}

func TestCapturedTranscriptUnusedReservationCanBeDiscardedWithoutLosingOtherRows(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	batch, err := agent.ReserveCapturedTranscriptBatch(writer)
	require.NoError(t, err)
	assert.True(t, agent.OutputRetained(writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})))
	assert.True(t, batch.Discard())
	assert.False(t, batch.Discard())
	require.NoError(t, queue.Flush())
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{"content":"later"}`, string(sink.Messages()[0].Content))
	require.Error(t, batch.Prepare(func() []agent.CapturedTranscriptOperation { t.Error("discarded preparation ran"); return nil }))
	_, err = agent.ReserveCapturedTranscriptBatch(nil)
	require.Error(t, err)
}

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
	// Every retry method waits behind the failed write, so the replacement
	// skips each one. A method without an ordered override would reach the
	// sink at once.
	services.ScheduleUsageLimitRetry(time.Now().Add(time.Hour), []byte(`{"reset":1}`))
	services.ScheduleServerErrorRetry([]byte(`{"error":1}`))
	services.CancelUsageLimitRetry("the usage limit lifted")
	services.CancelServerErrorRetry("the turn ended")
	sink.UpdateSessionID("new")
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	messages := sink.Messages()
	assert.Equal(t, "old", messages[len(messages)-1].AgentSessionID)
	assert.Zero(t, sink.AutoScheduleCount())
	assert.Zero(t, sink.AutoCancelCount())
	active, _ := sink.LastTurnActive()
	assert.True(t, active)
}

// Each ordered cancellation waits behind the earlier output and then reaches
// the sink with the kind and the reason that the provider gave.
func TestOrderedProviderServicesForwardEachCancellationReason(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))

	services.CancelUsageLimitRetry("the usage limit lifted")
	require.NoError(t, queue.Flush())
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, sink.LastAutoCancel())
	assert.Equal(t, "the usage limit lifted", sink.LastAutoCancelReason())

	services.CancelServerErrorRetry("the turn ended")
	require.NoError(t, queue.Flush())
	require.Equal(t, 2, sink.AutoCancelCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
	assert.Equal(t, "the turn ended", sink.LastAutoCancelReason())
}

func TestOrderedCapturedAutoRetryWaitsForRetainedTranscript(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Error(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{}))
	writer := services.CaptureAutoRetry()
	writer.ScheduleServerErrorRetry([]byte(`{"failure":true}`))
	assert.Zero(t, sink.AutoScheduleCount(), "capturing a writer cannot bypass the transcript queue")
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.Equal(t, 1, sink.AutoScheduleCount())
}

func TestOrderedKeyedRetryEffectsWaitForRetainedTranscript(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Error(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{}))
	writer := services.CaptureAutoRetry()
	payload := []byte(`{"failure":"original"}`)
	resetAt := time.Unix(1700000000, 0)
	writer.ScheduleUsageLimitRetryWithKey("native:A", resetAt, payload)
	payload[0] = 'x'
	writer.CancelUsageLimitRetryWithKey("native:A", "native completed")
	assert.Zero(t, sink.AutoScheduleCount())
	assert.Zero(t, sink.AutoCancelCount())
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.JSONEq(t, `{"failure":"original"}`, string(sink.LastAutoSchedule().SourcePayload))
	var effects []agenttest.TimelineEntry
	for _, operation := range sink.Operations() {
		if operation.Op == "turn_end_stored" || operation.Op == "schedule_usage_limit" || operation.Op == "cancel_usage_limit" {
			effects = append(effects, operation)
		}
	}
	assert.Equal(t, []agenttest.TimelineEntry{
		{Op: "turn_end_stored"},
		{Op: "schedule_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Key: "native:A", ResetAt: resetAt},
		{Op: "cancel_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Key: "native:A", Reason: "native completed"},
	}, effects)
}

func TestOrderedRetryAfterQueuedIdleKeepsItsOriginalOwner(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Error(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{}))
	services.SetTurnState(agent.TurnState{}, 2)
	services.ScheduleServerErrorRetry([]byte(`{"failure":true}`))
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.Equal(t, 1, sink.AutoScheduleCount(), "the verdict belongs to this exact completion even when its idle was queued")
}

func TestOrderedRetryCannotFollowANewerActivePublication(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Error(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{}))
	services.ScheduleServerErrorRetry([]byte(`{"old":true}`))
	services.SetTurnState(agent.TurnState{}, 2)
	services.SetTurnState(agent.TurnState{Active: true}, 3)
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.Zero(t, sink.AutoScheduleCount(), "the next start supersedes queued work even if the preceding idle never executed")
}

func TestOrderedCapturedCompletionRejectsAStartThatOvertakesQueuedIdle(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.Error(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{}))
	services.SetTurnState(agent.TurnState{}, 2)
	writer := services.CaptureAutoRetry()
	writer.ScheduleServerErrorRetry([]byte(`{"old":true}`))
	services.SetTurnState(agent.TurnState{Active: true}, 3)
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.Zero(t, sink.AutoScheduleCount())
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

func TestPersistTranscriptBatchCapturesEveryRecordBeforePublishingTheFirst(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{}}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	captures := 0
	sink.duringCapture = func() {
		captures++
		sink.duringCapture = func() {
			captures++
			assert.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
			assert.Empty(t, sink.Messages(), "a partial capture must not publish a prefix of the batch")
		}
	}
	require.NoError(t, agent.PersistTranscriptBatch(writer, []agent.TranscriptMessage{
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: []byte(`{"text":"first"}`)}},
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: []byte(`{"text":"second"}`)}},
	}))
	assert.Equal(t, 2, captures)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"text":"first"}`, string(rows[0].Content))
	assert.JSONEq(t, `{"text":"second"}`, string(rows[1].Content))
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

type preparedMessageInterceptor struct {
	agent.TranscriptServices
	queue  *agent.OutputQueue
	calls  int
	before func()
}

func (writer *preparedMessageInterceptor) OutputQueue() *agent.OutputQueue { return writer.queue }

func (writer *preparedMessageInterceptor) PersistPreparedMessage(source leapmuxv1.MessageSource, template agent.MessageContent, span agent.SpanInfo, prepare func() []byte) error {
	writer.calls++
	if writer.before != nil {
		writer.before()
	}
	return agent.PersistPreparedMessage(writer.TranscriptServices, source, template, span, prepare)
}

func TestPreparedTranscriptOverrideOwnsAdmissionBeforeTheGlobalQueue(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("original")
	writer := &preparedMessageInterceptor{TranscriptServices: agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink)), queue: &queue}
	writer.before = func() { assert.False(t, queue.HasPending(), "the concrete writer owns prepared admission") }
	preparations := 0
	require.NoError(t, agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{}, agent.SpanInfo{}, func() []byte {
		preparations++
		sink.UpdateSessionID("replacement")
		return []byte(`{"text":"selected"}`)
	}))
	assert.Equal(t, 1, writer.calls)
	assert.Equal(t, 1, preparations)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"text":"selected"}`, string(rows[0].Content))
	assert.Equal(t, "original", rows[0].AgentSessionID)
	assert.True(t, rows[0].TranscriptOnly)
	assert.False(t, queue.HasPending())
}

func TestPreparedTranscriptOverrideCannotAcceptInvalidAdmission(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	writer := &preparedMessageInterceptor{TranscriptServices: agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(&agenttest.Sink{})), queue: &queue}
	prepare := func() []byte { t.Error("invalid admission reached preparation"); return nil }
	for _, source := range []leapmuxv1.MessageSource{leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED, 999} {
		require.Error(t, agent.PersistPreparedMessage(writer, source, agent.MessageContent{}, agent.SpanInfo{}, prepare))
	}
	require.Error(t, agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{}, agent.SpanInfo{}, nil))
	assert.Zero(t, writer.calls)
	assert.False(t, queue.HasPending())
}

func TestPreparedMessagePreparationPanicReleasesOnlyItsOwnReservation(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	require.PanicsWithValue(t, "preparation failed", func() {
		_ = agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{}, agent.SpanInfo{}, func() []byte {
			err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})
			assert.True(t, agent.OutputRetained(err))
			panic("preparation failed")
		})
	})
	require.NoError(t, queue.Flush())
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.JSONEq(t, `{"content":"later"}`, string(rows[0].Content))
	assert.False(t, queue.HasPending())
}

func TestPreparedMessageCapturePanicLeavesTheQueueUsable(t *testing.T) {
	t.Parallel()
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{}, duringCapture: func() { panic("capture failed") }}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	require.PanicsWithValue(t, "capture failed", func() {
		_ = agent.PersistPreparedMessage(writer, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{}, agent.SpanInfo{}, func() []byte {
			t.Error("failed capture reached preparation")
			return nil
		})
	})
	assert.False(t, queue.HasPending())
	require.NoError(t, writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"healthy"}`)}, agent.SpanInfo{}))
	require.Len(t, sink.Messages(), 1)
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

type pausedReceiptCaptureSink struct {
	*agenttest.Sink
	entered chan struct{}
	release chan struct{}
}

func (sink *pausedReceiptCaptureSink) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	captured := sink.Sink.CaptureMessage(content, span)
	close(sink.entered)
	<-sink.release
	return captured
}

func TestTranscriptWriteReceiptPublishesItsOriginalAuthorityAfterCapture(t *testing.T) {
	t.Parallel()
	var absent *agent.TranscriptWriteReceipt
	publication, session, known := absent.CapturedPublication()
	assert.Nil(t, publication)
	assert.Empty(t, session)
	assert.False(t, known)
	assert.False(t, absent.OrderedWriteComplete())
	receipt := agent.NewTranscriptWriteReceipt()
	_, _, known = receipt.CapturedPublication()
	assert.False(t, known)
	sink := &pausedReceiptCaptureSink{Sink: &agenttest.Sink{}, entered: make(chan struct{}), release: make(chan struct{})}
	sink.UpdateSessionID("original")
	sink.PersistErr = assert.AnError
	var queue agent.OutputQueue
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(sink.release) }) })
	done := make(chan error, 1)
	go func() {
		done <- writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"native"}`), WriteReceipt: receipt}, agent.SpanInfo{SpanID: "tool"})
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-sink.entered:
	case <-ctx.Done():
		t.Fatal("the ordered capture did not start")
	}
	_, _, known = receipt.CapturedPublication()
	assert.False(t, known, "an unfinished capture supplies no authority")
	sink.UpdateSessionID("replacement")
	release.Do(func() { close(sink.release) })
	require.ErrorIs(t, <-done, assert.AnError)
	publication, session, known = receipt.CapturedPublication()
	require.True(t, known)
	require.NotNil(t, publication)
	assert.Equal(t, "original", session)
	assert.False(t, receipt.OrderedWriteComplete())
	assert.False(t, publication.Owner().IsCurrent())
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.True(t, receipt.OrderedWriteComplete())
	after, afterSession, known := receipt.CapturedPublication()
	require.True(t, known)
	assert.Same(t, publication.Owner(), after.Owner())
	assert.Equal(t, session, afterSession)
	require.Len(t, sink.Messages(), 2)
	assert.True(t, sink.Messages()[1].TranscriptOnly)
}

func TestCapturedAutoRetryOperationRunsAtItsReservedPosition(t *testing.T) {
	t.Parallel()
	var queue, nested agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	services := agent.NewOrderedProviderServices(&queue, agent.NewOrderedProviderServices(&nested, agent.NewProviderServices(sink)))
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	require.True(t, agent.OutputRetained(services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"end":true}`)}, agent.SpanInfo{})))
	calls := 0
	operation := services.CaptureAutoRetryOperation(2, func(writer agent.AutoRetryWriter) { calls++; writer.CancelServerErrorRetry("manual completion") })
	require.NotNil(t, operation)
	queue.Append(agent.OutputWrite{Label: "manual retry operation", Run: operation})
	services.SetTurnState(agent.TurnState{}, 2)
	services.ScheduleServerErrorRetry([]byte(`{"later":true}`))
	require.NoError(t, nested.Flush(), "capturing the operation must not append work to the nested queue")
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	require.NoError(t, nested.Flush())
	require.Equal(t, 1, calls)
	assert.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.Equal(t, []string{"turn_end", "cancel_server_error", "schedule_server_error"}, func() []string {
		var operations []string
		for _, entry := range sink.Operations() {
			switch entry.Op {
			case "turn_end_stored":
				operations = append(operations, "turn_end")
			case "cancel_server_error", "schedule_server_error":
				operations = append(operations, entry.Op)
			}
		}
		return operations
	}())
	assert.Nil(t, services.CaptureAutoRetryOperation(0, nil))
}
