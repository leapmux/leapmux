package agent_test

import (
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitialUserMessageRetainsOriginalContentAcrossReadFailures(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	unavailable, reads := true, 0
	raw := []byte(`{"content":"original assignment"}`)
	check := func() (bool, error) {
		reads++
		if unavailable {
			return false, assert.AnError
		}
		return true, nil
	}
	err := initial.Persist(writer, agent.MessageContent{Original: raw}, check)
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	clear(raw)
	err = initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"different assignment"}`)}, func() (bool, error) {
		t.Error("a joined reservation used the replacement check")
		return false, nil
	})
	assert.True(t, agent.OutputRetained(err))
	err = writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"native":"answer"}`)}, agent.SpanInfo{})
	assert.True(t, agent.OutputRetained(err))
	assert.Empty(t, sink.Messages())
	unavailable = false
	require.NoError(t, queue.Flush())
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"original assignment"}`, string(rows[0].Content))
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.JSONEq(t, `{"native":"answer"}`, string(rows[1].Content))
	assert.GreaterOrEqual(t, reads, 2)
	assert.False(t, queue.HasPending())
}

func TestInitialUserMessageKeepsItsSuccessfulDecisionWhileTheWriteRetries(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var queue agent.OutputQueue
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	reads := 0
	err := initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"original"}`)}, func() (bool, error) {
		reads++
		return true, nil
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, agent.OutputRetained(err))
	sink.PersistErr = nil
	require.NoError(t, queue.Flush())
	assert.Equal(t, 1, reads)
	rows := sink.Messages()
	require.Len(t, rows, 2, "one refused attempt and one committed opening message")
	assert.Equal(t, rows[0].Content, rows[1].Content)
	assert.False(t, queue.HasPending())
}

func TestInitialUserMessageConcurrentRepeatsKeepTheOriginalWriterAndPosition(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("original-session")
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	entered, release := make(chan struct{}), make(chan struct{})
	releaseRead := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseRead)
	var reads atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"original assignment"}`)}, func() (bool, error) {
			reads.Add(1)
			close(entered)
			<-release
			return true, nil
		})
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the reserved emptiness check did not start")
	}
	for _, text := range []string{`{"content":"original assignment"}`, `{"content":"different assignment"}`} {
		err := initial.Persist(writer, agent.MessageContent{Original: []byte(text)}, func() (bool, error) {
			t.Error("a repeat created another emptiness check")
			return false, nil
		})
		assert.True(t, agent.OutputRetained(err))
	}
	sink.UpdateSessionID("replacement-session")
	err := writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"native":"replacement answer"}`)}, agent.SpanInfo{})
	assert.True(t, agent.OutputRetained(err))
	assert.Empty(t, sink.Messages())
	releaseRead()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the initial write did not finish")
	}
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"original assignment"}`, string(rows[0].Content))
	assert.Equal(t, "original-session", rows[0].AgentSessionID)
	assert.True(t, rows[0].TranscriptOnly)
	assert.Equal(t, "replacement-session", rows[1].AgentSessionID)
	assert.False(t, rows[1].TranscriptOnly)
	assert.Equal(t, int64(1), reads.Load())
	assert.False(t, queue.HasPending())
}

func TestInitialUserMessageCaptureReentryCannotPassTheReservation(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{}}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	reads := 0
	sink.duringCapture = func() {
		assert.ErrorIs(t, queue.Flush(), agent.ErrOutputInFlight)
		assert.Equal(t, 0, reads)
		err := initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"reentrant replacement"}`)}, func() (bool, error) {
			t.Error("capture reentry replaced the original reservation")
			return false, nil
		})
		assert.True(t, agent.OutputRetained(err))
		assert.True(t, agent.OutputRetained(writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"native":"later"}`)}, agent.SpanInfo{})))
	}
	require.NoError(t, initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"original"}`)}, func() (bool, error) { reads++; return true, nil }))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"original"}`, string(rows[0].Content))
	assert.Equal(t, 1, reads)
}

func TestInitialUserMessageRechecksStorageAfterEachCompletedReservation(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var queue agent.OutputQueue
	sink := &agenttest.Sink{}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	reads := 0
	for _, empty := range []bool{false, false, true} {
		require.NoError(t, initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"opening"}`)}, func() (bool, error) {
			reads++
			return empty, nil
		}))
	}
	assert.Equal(t, 3, reads, "stored emptiness remains authoritative after a reservation completes")
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{"content":"opening"}`, string(sink.Messages()[0].Content))
}

func TestInitialUserMessageRejectsInvalidAdmissionWithoutConsumingItsSlot(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing queue", "missing check", "absent content", "empty content", "invalid completion", "invalid metadata"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var initial agent.InitialUserMessage
			var queue agent.OutputQueue
			sink := &agenttest.Sink{}
			writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
			var target agent.TranscriptServices = writer
			content := agent.MessageContent{Original: []byte(`{"content":"valid"}`)}
			check := func() (bool, error) { t.Error("an invalid admission reached storage"); return true, nil }
			switch scenario {
			case "missing queue":
				target = nil
			case "missing check":
				check = nil
			case "absent content":
				content.Original = nil
			case "empty content":
				content.Original = []byte{}
			case "invalid completion":
				content.Completion = "unknown"
			case "invalid metadata":
				content.Metadata = []byte("{")
			}
			err := initial.Persist(target, content, check)
			require.Error(t, err)
			assert.False(t, agent.OutputRetained(err))
			assert.False(t, queue.HasPending())
			assert.Empty(t, sink.Messages())
			require.NoError(t, initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"healthy"}`)}, func() (bool, error) { return true, nil }))
			assert.Len(t, sink.Messages(), 1)
		})
	}
}

func TestInitialUserMessageCapturePanicLeavesTheSlotAvailable(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var queue agent.OutputQueue
	sink := &batchMutationSink{Sink: &agenttest.Sink{}, duringCapture: func() { panic("capture failed") }}
	writer := agent.NewOrderedProviderServices(&queue, agent.NewProviderServices(sink))
	require.PanicsWithValue(t, "capture failed", func() {
		_ = initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"aborted"}`)}, func() (bool, error) { t.Error("capture failure reached storage"); return true, nil })
	})
	assert.False(t, queue.HasPending())
	require.NoError(t, initial.Persist(writer, agent.MessageContent{Original: []byte(`{"content":"healthy"}`)}, func() (bool, error) { return true, nil }))
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{"content":"healthy"}`, string(sink.Messages()[0].Content))
}

func TestInitialUserMessageRejectsAnotherQueueWithoutReplacingTheOriginalReservation(t *testing.T) {
	t.Parallel()
	var initial agent.InitialUserMessage
	var originalQueue, otherQueue agent.OutputQueue
	originalSink, otherSink := &agenttest.Sink{}, &agenttest.Sink{}
	originalWriter := agent.NewOrderedProviderServices(&originalQueue, agent.NewProviderServices(originalSink))
	otherWriter := agent.NewOrderedProviderServices(&otherQueue, agent.NewProviderServices(otherSink))
	unavailable := true
	err := initial.Persist(originalWriter, agent.MessageContent{Original: []byte(`{"content":"original"}`)}, func() (bool, error) {
		if unavailable {
			return false, assert.AnError
		}
		return true, nil
	})
	require.True(t, agent.OutputRetained(err))
	err = initial.Persist(otherWriter, agent.MessageContent{Original: []byte(`{"content":"wrong queue"}`)}, func() (bool, error) {
		t.Error("a refused queue reached its emptiness check")
		return true, nil
	})
	require.ErrorContains(t, err, "another output queue")
	assert.False(t, agent.OutputRetained(err))
	assert.False(t, otherQueue.HasPending())
	assert.Empty(t, otherSink.Messages())
	unavailable = false
	require.NoError(t, originalQueue.Flush())
	require.Len(t, originalSink.Messages(), 1)
	assert.JSONEq(t, `{"content":"original"}`, string(originalSink.Messages()[0].Content))
	require.NoError(t, initial.Persist(otherWriter, agent.MessageContent{Original: []byte(`{"content":"new reservation"}`)}, func() (bool, error) { return true, nil }))
	require.Len(t, otherSink.Messages(), 1)
	assert.JSONEq(t, `{"content":"new reservation"}`, string(otherSink.Messages()[0].Content))
}
