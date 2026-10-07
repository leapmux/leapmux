package pi

import (
	"context"
	"errors"
	"io"
	"sync"
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
