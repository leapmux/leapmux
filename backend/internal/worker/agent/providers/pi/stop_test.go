package pi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Close records the fake process exit after the pipe closes.
type piExitOnCloseWriter struct {
	io.WriteCloser
	exit func()
	once sync.Once
	err  error
}

func (writer *piExitOnCloseWriter) Close() error {
	writer.once.Do(func() {
		writer.err = writer.WriteCloser.Close()
		writer.exit()
	})
	return writer.err
}

// The real pipe unblocks a pending write when Stop closes stdin.
type piShutdownWriter struct {
	entered   chan struct{}
	closed    chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
	onClose   func()
}

func (writer *piShutdownWriter) Write([]byte) (int, error) {
	writer.enterOnce.Do(func() { close(writer.entered) })
	<-writer.closed
	return 0, errors.New("the shutdown pipe is closed")
}

func (writer *piShutdownWriter) Close() error {
	writer.closeOnce.Do(func() {
		close(writer.closed)
		writer.onClose()
	})
	return nil
}

func piBlockedShutdownProcess(t *testing.T, a *Agent, clock *quartz.Mock) *piShutdownWriter {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	writer := &piShutdownWriter{entered: make(chan struct{}), closed: make(chan struct{})}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "pi-shutdown-test", ProviderName: "pi", Ctx: ctx, Cancel: cancel,
		Clock: clock, Stdin: writer,
	})
	writer.onClose = a.SimulateExitForTest
	a.Mu.Lock()
	a.currentTurnActive = true
	a.Mu.Unlock()
	return writer
}

func TestPiStopLimitsAnAbortWriteBeforeClosingStdin(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	writer := piBlockedShutdownProcess(t, a, clock)
	trap := clock.Trap().NewTimer("pi-stop-abort-delivery")
	done := make(chan struct{})
	go func() { a.Stop(); close(done) }()
	t.Cleanup(func() { trap.Close(); _ = writer.Close(); awaitPiWrite(t, done) })
	call, err := trap.Wait(testutil.DeadlineContext(t))
	require.NoError(t, err, "Stop must start its delivery deadline before the stdin write can block")
	assert.Equal(t, time.Second, call.Duration)
	call.MustRelease(testutil.DeadlineContext(t))
	trap.Close()
	awaitPiWrite(t, writer.entered)
	clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
	awaitPiWrite(t, writer.closed)
	awaitPiWrite(t, done)
}

func TestPiStopDoesNotWaitForAQueuedDialogBeforeClosingStdin(t *testing.T) {
	t.Parallel()
	for _, freshSettings := range []bool{false, true} {
		t.Run(map[bool]string{false: "custom input", true: "fresh settings"}[freshSettings], func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			var a *Agent
			var sink *agenttest.ControlSink
			var queued []byte
			if freshSettings {
				a, sink, _ = piFreshImplementationFixture()
				a.notePiPlanFreshApproval()
				queued = []byte(piFreshSettingsDialog)
			} else {
				a, sink, _ = piQuestionResponseFixture()
				require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"select","value":"My custom answer"}`), agent.StopContext{}))
				queued = []byte(`{"type":"extension_ui_request","id":"input","method":"input","title":"Choose\n\nType your answer:","placeholder":""}`)
			}
			writer := piBlockedShutdownProcess(t, a, clock)
			scope, active, stopped := a.beginPiInterrupt()
			require.True(t, active)
			require.False(t, stopped)
			oldWrite := make(chan error, 1)
			go func() { oldWrite <- a.WriteStdin([]byte(`{"type":"abort","id":"held-old-abort"}`)) }()
			awaitPiWrite(t, writer.entered)
			a.HandleOutput(queued)
			trap := clock.Trap().NewTimer("pi-stop-abort-delivery")
			done := make(chan struct{})
			go func() { a.Stop(); close(done) }()
			t.Cleanup(func() {
				trap.Close()
				_ = writer.Close()
				awaitPiWrite(t, done)
			})
			call, err := trap.Wait(testutil.DeadlineContext(t))
			require.NoError(t, err, "queued dialog replay must not prevent Stop from reaching its delivery deadline")
			assert.Equal(t, time.Second, call.Duration)
			call.MustRelease(testutil.DeadlineContext(t))
			trap.Close()
			clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
			awaitPiWrite(t, writer.closed)
			awaitPiWrite(t, done)
			require.Error(t, awaitPiInterrupt(t, oldWrite))
			a.finishPiInterruptWrite(scope, errors.New("the old write failed during shutdown"))
			controls := sink.PublishedControls()
			require.NotEmpty(t, controls)
			assert.Equal(t, queued, controls[len(controls)-1].Payload, "shutdown keeps the queued native dialog")
		})
	}
}

func TestPiIdleInterruptAndStopWaitClearContextResetSpansAndProgress(t *testing.T) {
	t.Parallel()

	t.Run("idle interrupt closes open tool and resets spans and progress", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-idle","toolName":"bash","args":{"command":"sleep 30"}}`))
		a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`))
		a.HandleOutput([]byte(`{"type":"agent_settled"}`))
		require.Empty(t, sink.ClosedSpans())

		require.NoError(t, a.Interrupt(agent.StopContext{}))
		assert.Contains(t, sink.ClosedSpans(), "tc-idle")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
	})

	t.Run("Stop resets spans and progress", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-stop","toolName":"bash","args":{"command":"sleep 30"}}`))
		a.SimulateExitForTest()
		a.Stop()
		assert.Contains(t, sink.ClosedSpans(), "tc-stop")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
	})

	t.Run("Wait resets spans and progress", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-wait","toolName":"bash","args":{"command":"sleep 30"}}`))
		a.SimulateExitForTest()
		_ = a.Wait()
		assert.Contains(t, sink.ClosedSpans(), "tc-wait")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
	})

	t.Run("ClearContext closes open tools resets spans progress and closedSpans", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		rig := newPiTestRig(t, agent.NewProviderServices(sink))
		rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
			switch req.Type {
			case "new_session":
				return json.RawMessage(`{"cancelled":false}`), true, ""
			case "get_state":
				return json.RawMessage(`{
					"model":{"id":"gpt-5.5","provider":"openai-codex"},
					"thinkingLevel":"medium",
					"sessionId":"new-sess",
					"sessionFile":"/tmp/pi-clear.jsonl"
				}`), true, ""
			}
			return nil, true, ""
		})

		rig.agent.HandleOutput([]byte(`{"type":"agent_start"}`))
		rig.agent.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-clear","toolName":"bash","args":{"command":"sleep 30"}}`))
		rig.agent.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`))
		rig.agent.HandleOutput([]byte(`{"type":"agent_settled"}`))
		require.Empty(t, sink.ClosedSpans())

		id, err := rig.agent.ClearContext()
		require.NoError(t, err)
		assert.Equal(t, "/tmp/pi-clear.jsonl", id)
		assert.Contains(t, sink.ClosedSpans(), "tc-clear")
		assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
		assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())

		rig.agent.Mu.Lock()
		closedAfterClear := rig.agent.closedSpans.WasClosed("tc-clear")
		rig.agent.Mu.Unlock()
		assert.False(t, closedAfterClear, "ClearContext must reset closedSpans for the new session")

		// A reused tool ID in the replacement session is accepted rather than suppressed.
		rowsBefore := sink.MessageCount()
		rig.agent.HandleOutput([]byte(`{"type":"tool_execution_end","toolCallId":"tc-clear","toolName":"bash","result":{"content":[{"type":"text","text":"new session"}]}}`))
		assert.Equal(t, rowsBefore+1, sink.MessageCount())
	})
}

type piBlockingCloseSink struct {
	agent.ProviderServices
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
	inCloseOld atomic.Bool
	overlapped atomic.Bool
}

func (s *piBlockingCloseSink) CloseSpan(spanID string) {
	if spanID == "tc-old" {
		s.inCloseOld.Store(true)
		defer s.inCloseOld.Store(false)
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	s.ProviderServices.CloseSpan(spanID)
}

func (s *piBlockingCloseSink) OpenSpan(spanID, parentSpanID string) {
	if spanID == "tc-new" && s.inCloseOld.Load() {
		s.overlapped.Store(true)
	}
	s.ProviderServices.OpenSpan(spanID, parentSpanID)
}

func TestPiIdleInterruptSerializedWithHandleOutput(t *testing.T) {
	t.Parallel()

	t.Run("idle interrupt cleanup cannot wipe a newly arriving turn", func(t *testing.T) {
		t.Parallel()
		for range 5 {
			base := &agenttest.ControlSink{}
			gate := &piBlockingCloseSink{
				ProviderServices: agent.NewProviderServices(base),
				entered:          make(chan struct{}),
				release:          make(chan struct{}),
			}
			a := newPiAgentWithSink(gate)
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-old","toolName":"bash","args":{"command":"sleep 30"}}`))
			a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`))
			a.HandleOutput([]byte(`{"type":"agent_settled"}`))

			interruptDone := make(chan error, 1)
			go func() { interruptDone <- a.Interrupt(agent.StopContext{}) }()
			awaitPiWrite(t, gate.entered)

			handled := make(chan struct{})
			go func() {
				defer close(handled)
				a.HandleOutput([]byte(`{"type":"agent_start"}`))
				a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-new","toolName":"bash","args":{"command":"sleep 10"}}`))
			}()

			// Wait until HandleOutput has entered dispatch (blocking on output serialization)
			// or finished without waiting (the unserialized bug).
			ctx := testutil.DeadlineContext(t)
			for {
				select {
				case <-handled:
					goto releaseGate
				case <-ctx.Done():
					t.Fatal("timed out waiting for HandleOutput to reach dispatch")
				default:
				}
				a.interruptOutput.mu.Lock()
				handling := a.interruptOutput.handling
				a.interruptOutput.mu.Unlock()
				if handling {
					for range 64 {
						runtime.Gosched()
					}
					break
				}
				runtime.Gosched()
			}

		releaseGate:
			close(gate.release)
			require.NoError(t, awaitPiInterrupt(t, interruptDone))
			awaitPiWrite(t, handled)

			assert.False(t, gate.overlapped.Load(), "HandleOutput must not open tc-new while idle interrupt cleanup is running")
			assert.Contains(t, base.ClosedSpans(), "tc-old")
			assert.NotContains(t, base.ClosedSpans(), "tc-new", "idle interrupt must not close a tool from the next turn")
			assert.Equal(t, "bash", base.GetSpanType("tc-new"), "idle interrupt ResetSpansAndProgress must not wipe the next turn's live span tracker")
		}
	})

	t.Run("concurrent idle interrupt handle output and stop have no race or deadlock", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.ControlSink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 25 {
				a.HandleOutput([]byte(`{"type":"agent_start"}`))
				a.HandleOutput([]byte(`{"type":"tool_execution_start","toolCallId":"tc-race","toolName":"bash","args":{"command":"echo hi"}}`))
				a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`))
				a.HandleOutput([]byte(`{"type":"agent_settled"}`))
			}
		}()
		go func() {
			defer wg.Done()
			for range 25 {
				_ = a.Interrupt(agent.StopContext{})
			}
		}()
		wg.Wait()
		a.SimulateExitForTest()
		a.Stop()
	})
}
