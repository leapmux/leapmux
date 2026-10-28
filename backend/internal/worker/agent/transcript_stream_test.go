package agent_test

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturedTranscriptStreamRetriesCaptureAtItsOriginalPosition(t *testing.T) {
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	batch, err := agent.ReserveCapturedTranscriptBatch(writer)
	require.NoError(t, err)
	err = writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"later"}`)}, agent.SpanInfo{})
	require.True(t, agent.OutputRetained(err))
	writer.SetTurnState(agent.TurnState{}, 1)
	index, captures, cleanups := 0, 0, 0
	fail := true
	payloads := []string{`{"text":"first"}`, `{"text":"second"}`}
	err = batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
		if index == len(payloads) {
			cleanups++
			assert.Len(t, sink.Messages(), 2)
			return agent.CapturedTranscriptOperation{}, true, nil
		}
		if index == 1 && fail {
			return agent.CapturedTranscriptOperation{}, false, assert.AnError
		}
		captures++
		operation := agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(payloads[index])}, agent.SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		index++
		return operation, false, nil
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	assert.Equal(t, 1, captures)
	assert.Equal(t, 1, index)
	assert.Zero(t, cleanups)
	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, sink.TurnActiveCalls)
	fail = false
	require.NoError(t, queue.Flush())
	assert.Equal(t, 2, captures)
	assert.Equal(t, 1, cleanups)
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.JSONEq(t, payloads[0], string(rows[0].Content))
	assert.JSONEq(t, payloads[1], string(rows[1].Content))
	assert.JSONEq(t, `{"content":"later"}`, string(rows[2].Content))
	assert.Equal(t, []bool{false}, sink.TurnActiveCalls)
	assert.False(t, queue.HasPending())
}

func TestCapturedTranscriptStreamKeepsOneCaptureAcrossStorageFailure(t *testing.T) {
	var queue agent.OutputQueue
	sink := &capturedBatchFailureSink{Sink: &agenttest.Sink{}, rejectSecond: true}
	sink.UpdateSessionID("original")
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	batch, err := agent.ReserveCapturedTranscriptBatch(writer)
	require.NoError(t, err)
	index, captures, cleanups := 0, 0, 0
	var retained agent.CapturedTranscriptOperation
	payloads := []string{`{"text":"first"}`, `{"text":"second"}`}
	err = batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
		if index == len(payloads) {
			cleanups++
			return agent.CapturedTranscriptOperation{}, true, nil
		}
		captures++
		operation := agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(payloads[index])}, agent.SpanInfo{SpanID: "native-tool", SpanColor: 5}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		if index == 1 {
			retained = operation
		}
		index++
		return operation, false, nil
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 2, captures)
	assert.Zero(t, cleanups)
	require.Len(t, sink.Messages(), 1)
	_, err = retained.WithSupplementalContent([]byte(`{"changed":true}`))
	require.ErrorIs(t, err, agent.ErrCapturedTranscriptImmutable)
	sink.UpdateSessionID("replacement")
	sink.ResetSpans()
	sink.rejectSecond = false
	require.NoError(t, queue.Flush())
	assert.Equal(t, 2, captures)
	assert.Equal(t, 1, cleanups)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, "original", rows[1].AgentSessionID)
	assert.Equal(t, int32(5), rows[1].SpanColor)
	assert.True(t, rows[1].TranscriptOnly)
}

func TestCapturedTranscriptStreamKeepsAnInvalidYieldInsteadOfSkippingIt(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid source", true: "foreign queue"}[foreign], func(t *testing.T) {
			var queue, other agent.OutputQueue
			sink := &agenttest.Sink{}
			writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
			batch, err := agent.ReserveCapturedTranscriptBatch(writer)
			require.NoError(t, err)
			calls := 0
			err = batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
				calls++
				captureWriter := agent.TranscriptServices(writer)
				source := leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED
				if foreign {
					captureWriter = agent.OrderedTranscript(&other, sink)
					source = leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
				}
				return agent.CaptureTranscript(captureWriter, agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{}).MessageOperation(source), false, nil
			})
			require.Error(t, err)
			require.True(t, agent.OutputRetained(err))
			require.Error(t, queue.Flush())
			assert.Equal(t, 1, calls)
			assert.Empty(t, sink.Messages())
		})
	}
}

func TestCapturedTranscriptStreamOwnsCleanupAndTypedOperations(t *testing.T) {
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	batch, err := agent.ReserveCapturedTranscriptBatch(writer)
	require.NoError(t, err)
	index, cleanups := 0, 0
	failCleanup := true
	err = batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
		if index == 3 {
			cleanups++
			if failCleanup {
				return agent.CapturedTranscriptOperation{}, false, assert.AnError
			}
			return agent.CapturedTranscriptOperation{}, true, nil
		}
		capture := agent.CaptureTranscript(writer, agent.MessageContent{Original: []byte(`{"type":"agent_status","text":"native"}`)}, agent.SpanInfo{})
		index++
		switch index {
		case 1:
			return capture.NotificationOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT), false, nil
		case 2:
			return capture.WorkerNotificationOperation(), false, nil
		default:
			return capture.TurnEndOperation(), false, nil
		}
	})
	require.ErrorIs(t, err, assert.AnError)
	require.Len(t, sink.PersistedNotifications(), 1)
	require.Len(t, sink.LeapMuxNotifications(), 1)
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
	failCleanup = false
	require.NoError(t, queue.Flush())
	assert.Equal(t, 2, cleanups)
	assert.Equal(t, 3, index)
	assert.False(t, queue.HasPending())
}

func TestCapturedTranscriptStreamEmptyAndNilPreparation(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "nil"}[missing], func(t *testing.T) {
			var queue agent.OutputQueue
			sink := &agenttest.Sink{}
			writer := agent.OrderedTranscript(&queue, sink)
			batch, err := agent.ReserveCapturedTranscriptBatch(writer)
			require.NoError(t, err)
			if missing {
				require.Error(t, batch.PrepareStream(nil))
			} else {
				require.NoError(t, batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
					return agent.CapturedTranscriptOperation{}, true, nil
				}))
			}
			require.NoError(t, queue.Flush())
			assert.False(t, queue.HasPending())
			called := false
			require.Error(t, batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
				called = true
				return agent.CapturedTranscriptOperation{}, true, nil
			}))
			assert.False(t, called)
		})
	}
}
