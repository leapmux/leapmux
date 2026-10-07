package pi

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piOutputQueueState(q *piInterruptOutput) (frames, retained, waiters int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.frames), q.bytes, q.waiters
}

// The Cond calls this locker when Wait releases Mu. That signal proves admission
// waits before the test releases its handler, without a scheduling delay.
type piOutputWaitLocker struct {
	mu       *sync.Mutex
	unlocked chan struct{}
	once     sync.Once
}

func (locker *piOutputWaitLocker) Lock() { locker.mu.Lock() }
func (locker *piOutputWaitLocker) Unlock() {
	locker.mu.Unlock()
	locker.once.Do(func() { close(locker.unlocked) })
}

func TestPiWaitingStopStartsBeforeTheNextQueuedHandler(t *testing.T) {
	t.Parallel()
	queue := &piInterruptOutput{}
	waitUnlocked := make(chan struct{})
	queue.ready = sync.NewCond(&piOutputWaitLocker{mu: &queue.mu, unlocked: waitUnlocked})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	events := make(chan string, 2)
	dispatch := func(line *providerkit.ParsedLine, _ time.Time) {
		if bytes.Equal(line.Raw, []byte(`{"n":1}`)) {
			close(entered)
			<-release
		} else {
			events <- "second handler"
		}
	}
	outputDone := make(chan struct{})
	go func() {
		queue.enqueue(providerkit.ParseLine([]byte(`{"n":1}`)), time.Time{}, dispatch)
		close(outputDone)
	}()
	awaitPiWrite(t, entered)
	queue.enqueue(providerkit.ParseLine([]byte(`{"n":2}`)), time.Time{}, dispatch)
	beginDone := make(chan struct{})
	var admitted atomic.Bool
	go func() {
		admitted.Store(queue.begin(func() bool {
			events <- "stop"
			return true
		}))
		close(beginDone)
	}()
	t.Cleanup(func() {
		queue.dispose(func() {}, dispatch)
		releaseOnce.Do(func() { close(release) })
		awaitPiWrite(t, beginDone)
		awaitPiWrite(t, outputDone)
	})
	awaitPiWrite(t, waitUnlocked)
	releaseOnce.Do(func() { close(release) })
	select {
	case first := <-events:
		require.Equal(t, "stop", first, "a waiting stop takes the handler boundary before the next frame")
	case <-time.After(30 * time.Second):
		t.Fatal("the handler boundary admitted no stop or frame")
	}
	awaitPiWrite(t, beginDone)
	require.True(t, admitted.Load())
	awaitPiWrite(t, outputDone)
	frames, _, _ := piOutputQueueState(queue)
	assert.Equal(t, 1, frames, "the admitted write retains the second frame until its outcome")
	queue.finish(func() {}, dispatch)
	assert.Equal(t, "second handler", <-events)
}

func TestPiDisposalWakesAStopThatWaitsForAnActiveHandler(t *testing.T) {
	t.Parallel()
	queue := &piInterruptOutput{}
	waitUnlocked := make(chan struct{})
	queue.ready = sync.NewCond(&piOutputWaitLocker{mu: &queue.mu, unlocked: waitUnlocked})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	dispatch := func(*providerkit.ParsedLine, time.Time) { close(entered); <-release }
	outputDone := make(chan struct{})
	go func() {
		queue.enqueue(providerkit.ParseLine([]byte(`{"n":1}`)), time.Time{}, dispatch)
		close(outputDone)
	}()
	awaitPiWrite(t, entered)
	beginDone := make(chan struct{})
	var admitted, recorded atomic.Bool
	go func() {
		admitted.Store(queue.begin(func() bool { recorded.Store(true); return true }))
		close(beginDone)
	}()
	t.Cleanup(func() {
		queue.dispose(func() {}, dispatch)
		releaseOnce.Do(func() { close(release) })
		awaitPiWrite(t, beginDone)
		awaitPiWrite(t, outputDone)
	})
	awaitPiWrite(t, waitUnlocked)
	queue.dispose(func() {}, dispatch)
	awaitPiWrite(t, beginDone)
	assert.False(t, admitted.Load(), "shutdown wakes and rejects the waiting stop")
	assert.False(t, recorded.Load(), "a rejected stop records no intent")
	releaseOnce.Do(func() { close(release) })
	awaitPiWrite(t, outputDone)
}

func TestPiInterruptOutputAppliesBothLimitsAndPreservesOrder(t *testing.T) {
	t.Parallel()
	for name, queue := range map[string]*piInterruptOutput{
		"frame count": {maxFrames: 2, maxBytes: 1000},
		"byte count":  {maxFrames: 4, maxBytes: 14},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var received []string
			dispatch := func(line *providerkit.ParsedLine, _ time.Time) {
				mu.Lock()
				received = append(received, string(line.Raw))
				mu.Unlock()
			}
			require.True(t, queue.begin(func() bool { return true }))
			queue.enqueue(providerkit.ParseLine([]byte(`{"n":1}`)), time.Time{}, dispatch)
			queue.enqueue(providerkit.ParseLine([]byte(`{"n":2}`)), time.Time{}, dispatch)
			frames, retained, _ := piOutputQueueState(queue)
			assert.Equal(t, 2, frames)
			assert.Equal(t, 14, retained)
			done := make(chan struct{})
			go func() {
				queue.enqueue(providerkit.ParseLine([]byte(`{"n":3}`)), time.Time{}, dispatch)
				close(done)
			}()
			require.Eventually(t, func() bool { _, _, waiting := piOutputQueueState(queue); return waiting == 1 },
				30*time.Second, time.Millisecond, "the producer must wait at the configured limit")
			queue.finish(func() {}, dispatch)
			awaitPiWrite(t, done)
			mu.Lock()
			assert.Equal(t, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, received)
			mu.Unlock()
			frames, retained, waiters := piOutputQueueState(queue)
			assert.Zero(t, frames)
			assert.Zero(t, retained)
			assert.Zero(t, waiters)
		})
	}
}

func TestPiInterruptOutputAcceptsOneLargeFrameAndClonesItsBytes(t *testing.T) {
	t.Parallel()
	queue := &piInterruptOutput{maxFrames: 2, maxBytes: 4}
	var received []string
	dispatch := func(line *providerkit.ParsedLine, _ time.Time) { received = append(received, string(line.Raw)) }
	require.True(t, queue.begin(func() bool { return true }))
	raw := []byte(`{"n":1}`)
	queue.enqueue(providerkit.ParseLine(raw), time.Time{}, dispatch)
	copy(raw, []byte(`{"n":9}`))
	frames, retained, _ := piOutputQueueState(queue)
	assert.Equal(t, 1, frames)
	assert.Equal(t, 7, retained, "the single frame can exceed a changed byte limit")
	queue.finish(func() {}, dispatch)
	assert.Equal(t, []string{`{"n":1}`}, received)
}

func TestPiInterruptOutputDisposalReplaysRetainedFramesAndWakesAProducer(t *testing.T) {
	t.Parallel()
	queue := &piInterruptOutput{maxFrames: 1, maxBytes: 1000}
	var received []string
	dispatch := func(line *providerkit.ParsedLine, _ time.Time) { received = append(received, string(line.Raw)) }
	require.True(t, queue.begin(func() bool { return true }))
	queue.enqueue(providerkit.ParseLine([]byte(`{"n":1}`)), time.Time{}, dispatch)
	done := make(chan struct{})
	go func() {
		queue.enqueue(providerkit.ParseLine([]byte(`{"n":2}`)), time.Time{}, dispatch)
		close(done)
	}()
	require.Eventually(t, func() bool { _, _, waiting := piOutputQueueState(queue); return waiting == 1 },
		30*time.Second, time.Millisecond)
	queue.dispose(func() {}, dispatch)
	awaitPiWrite(t, done)
	queue.waitForDrain()
	assert.Equal(t, []string{`{"n":1}`, `{"n":2}`}, received, "disposal preserves retained and blocked native frames")
	assert.False(t, queue.begin(func() bool { return true }), "an ended stream accepts no new write")
	queue.finish(func() {}, dispatch)
}

func TestPiOutputDisposalPreservesANativeEndDuringShutdown(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	scope, active, stopped := a.beginPiInterrupt()
	require.True(t, active)
	require.False(t, stopped)
	a.disposePiInterruptOutput()
	a.finishPiInterruptWrite(scope, nil)
	a.Mu.Lock()
	assert.Empty(t, a.interruptRequests, "a late write cannot restore the disposed pending attempt")
	a.Mu.Unlock()
	ended := []byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`)
	a.HandleOutput(ended)
	rows := sink.Messages()
	require.Len(t, rows, 1, "shutdown must preserve the native divider")
	assert.Equal(t, ended, rows[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
}

func TestPiInterruptOutputAllowsAHandlerToEnqueueDuringReplay(t *testing.T) {
	t.Parallel()
	queue := &piInterruptOutput{maxFrames: 3, maxBytes: 1000}
	var received []string
	var dispatch func(*providerkit.ParsedLine, time.Time)
	dispatch = func(line *providerkit.ParsedLine, _ time.Time) {
		received = append(received, string(line.Raw))
		if bytes.Equal(line.Raw, []byte(`{"n":1}`)) {
			queue.enqueue(providerkit.ParseLine([]byte(`{"n":3}`)), time.Time{}, dispatch)
		}
	}
	require.True(t, queue.begin(func() bool { return true }))
	queue.enqueue(providerkit.ParseLine([]byte(`{"n":1}`)), time.Time{}, dispatch)
	queue.enqueue(providerkit.ParseLine([]byte(`{"n":2}`)), time.Time{}, dispatch)
	queue.finish(func() {}, dispatch)
	assert.Equal(t, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, received)
}

func TestPiStopWakesOutputBeforeItWaitsForProcessCompletion(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a := rig.agent
	a.interruptOutput.maxFrames = 1
	require.True(t, a.interruptOutput.begin(func() bool { return true }))
	a.HandleOutput([]byte(`{"type":"unknown","sequence":1}`))
	producerDone := make(chan struct{})
	go func() {
		a.HandleOutput([]byte(`{"type":"unknown","sequence":2}`))
		// ReadLines records process completion only after its handler returns.
		a.SimulateExitForTest()
		close(producerDone)
	}()
	require.Eventually(t, func() bool {
		_, _, waiting := piOutputQueueState(&a.interruptOutput)
		return waiting == 1
	}, 30*time.Second, time.Millisecond)
	stopDone := make(chan struct{})
	go func() { a.Stop(); close(stopDone) }()
	// Dispose during cleanup also releases a broken implementation's producer.
	t.Cleanup(func() {
		a.disposePiInterruptOutput()
		awaitPiWrite(t, producerDone)
		awaitPiWrite(t, stopDone)
	})
	require.Eventually(t, a.IsStopped, 30*time.Second, time.Millisecond)
	a.interruptOutput.mu.Lock()
	closed := a.interruptOutput.stopsClosed
	a.interruptOutput.mu.Unlock()
	assert.True(t, closed, "Stop must release output before Process.Stop waits for ReadLines")
}

func TestPiQueuedTurnEventsKeepTheirObservationTimes(t *testing.T) {
	t.Parallel()
	for _, negative := range []bool{false, true} {
		t.Run(map[bool]string{false: "old and replacement turns", true: "negative clock"}[negative], func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			now := time.Unix(100, 0)
			a.nowFn = func() time.Time { return now }
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			scope, active, stopped := a.beginPiInterrupt()
			require.True(t, active)
			require.False(t, stopped)
			if negative {
				now = time.Unix(90, 0)
			} else {
				now = time.Unix(103, 0)
			}
			a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}]}`))
			if !negative {
				now = time.Unix(110, 0)
				a.HandleOutput([]byte(`{"type":"agent_start"}`))
				now = time.Unix(114, 0)
				a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}]}`))
			}
			now = time.Unix(200, 0)
			a.finishPiInterruptWrite(scope, nil)
			rows := sink.Messages()
			if negative {
				require.Len(t, rows, 1)
			} else {
				require.Len(t, rows, 2)
			}
			for index := range rows {
				data := piPersistedAgentEnd(t, sink, index)
				if negative {
					assert.NotContains(t, data, "duration_ms", "a negative observed duration is absent")
				} else {
					assert.Equal(t, []float64{3000, 4000}[index], data["duration_ms"])
				}
			}
		})
	}
}

func TestPiMalformedAgentEndKeepsStateUntilAValidEnd(t *testing.T) {
	t.Parallel()
	for _, malformed := range []string{
		`{"type":"agent_end","willRetry":"invalid","messages":[]}`,
		`{"type":"agent_end","willRetry":null,"messages":[]}`,
		`{"type":"agent_end","messages":"invalid"}`,
	} {
		t.Run(malformed, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			a.Mu.Lock()
			started := a.turnStartedAt
			a.Mu.Unlock()
			a.HandleOutput([]byte(malformed))
			a.Mu.Lock()
			assert.True(t, a.currentTurnActive)
			assert.Equal(t, started, a.turnStartedAt)
			a.Mu.Unlock()
			assert.Zero(t, sink.MessageCount(), "a malformed end must persist no divider")
			assert.Zero(t, sink.AutoCancelCount(), "a malformed end changes no continuation")
			a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[]}`))
			a.Mu.Lock()
			assert.False(t, a.currentTurnActive)
			a.Mu.Unlock()
			assert.Equal(t, 1, sink.MessageCount(), "a later valid end still persists its divider")
		})
	}
}

func TestPiNullMessagesKeepsTheActiveQuestionAndContinuation(t *testing.T) {
	t.Parallel()
	a, sink, output := piQuestionResponseFixture()
	setPiInterruptWriter(a, func(data []byte) (int, error) { return output.Write(data) })
	a.Mu.Lock()
	a.currentTurnActive = true
	a.turnStartedAt = time.Unix(100, 0)
	a.Mu.Unlock()
	a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":null}`))
	a.Mu.Lock()
	assert.True(t, a.currentTurnActive)
	assert.Equal(t, time.Unix(100, 0), a.turnStartedAt)
	assert.Contains(t, a.questionDialogs, "select", "a malformed end must retain native answer correlation")
	assert.Contains(t, a.openDialogs, "select")
	a.Mu.Unlock()
	assert.Equal(t, 1, sink.MessageCount(), "only the original question tool row remains")
	assert.Zero(t, sink.AutoCancelCount())
	assert.Zero(t, sink.AutoScheduleCount())
	assert.Empty(t, sink.CanceledControls())
	a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[]}`))
	a.Mu.Lock()
	assert.False(t, a.currentTurnActive)
	assert.Empty(t, a.questionDialogs)
	a.Mu.Unlock()
	assert.Equal(t, 3, sink.MessageCount(), "the valid end keeps the unfinished tool and its native divider")
}

func TestPiOmittedMessagesStillEndsTheObservedTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"agent_end"}`))
	a.Mu.Lock()
	assert.False(t, a.currentTurnActive)
	a.Mu.Unlock()
	assert.Equal(t, 1, sink.MessageCount())
}
