package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// zcodeCapturedTimer replaces time.AfterFunc so a test fires the fallback itself.
type zcodeCapturedTimer struct {
	fire  func()
	delay time.Duration
	armed int
}

func (c *zcodeCapturedTimer) afterFunc(delay time.Duration, fire func()) *time.Timer {
	c.delay = delay
	c.fire = fire
	c.armed++
	// A real timer that never fires on its own: the test calls `fire` directly.
	return time.NewTimer(time.Hour)
}

// A stop the app-server ACCEPTS does not always cut the turn. In a live census the
// agent went on making tool calls for two more minutes and then reported a normal
// end, while LeapMux had already cleared the thinking indicator -- the "live agent
// behind an idle chat" the Interrupt comment says must not happen.
//
// Captured from `.tmp/provider-parity/interrupt6` (RL-016).
func TestZCodeInterrupt_KeepsTheTurnAliveWhileTheAgentStillSpeaks(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())

	a.mu.Lock()
	turnActive := a.turnActive
	a.mu.Unlock()
	assert.True(t, turnActive, "the agent has not stopped yet, so the chat must not read idle")
	assert.Equal(t, 1, timer.armed, "the stop arms the silence fallback")
}

// Every session event the agent sends after the stop restarts the window, because a
// frame is proof the turn is alive.
func TestZCodeInterrupt_ASessionEventRefreshesTheFallback(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())
	require.Equal(t, 1, timer.armed)

	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"text.delta","payload":{"text":"still working"}}}}`)))

	assert.Equal(t, 2, timer.armed, "a frame after the stop restarts the window")
}

// An agent that was never stopped arms nothing, so an ordinary turn pays no cost.
func TestZCodeOutput_ASessionEventWithNoStopArmsNothing(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"text.delta","payload":{"text":"working"}}}}`)))

	assert.Equal(t, 0, timer.armed)
}

// A replaced session drops every piece of per-session state, and the window is one of
// them. Left armed, it would fire into the NEW session and flush a buffer that belongs
// to nothing.
func TestZCodeClearContext_DropsTheStoppedTurnWindow(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())
	a.cancelStoppedZCodeTurn()

	a.mu.Lock()
	armed := a.stoppedTurnTimer
	a.mu.Unlock()
	assert.Nil(t, armed, "the window goes with the session it watched")
}

// A stop the reader asked for has to leave a ROW, even when the app-server says
// nothing about it.
//
// The census pressed Stop on `sleep 45` for ten providers. Nine drew a turn-end
// divider and eight also drew a result row. ZCode drew NEITHER: the app-server sent
// no frame for the abort, so the transcript held a running command card and no
// statement at all that the reader had stopped the turn.
//
// The row this writes is LeapMux's own, in LeapMux's own vocabulary. It states what
// LeapMux DID -- it asked for the stop, the app-server accepted it, and the window of
// silence then expired -- and it invents nothing the provider did not send.
func TestZCodeStoppedTurn_WritesARowForTheStop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())
	require.Equal(t, 1, timer.armed)

	// The app-server stays silent, so the window expires.
	timer.fire()

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "the reader's stop ended the turn, so one row states it")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, notifications[0].Source,
		"LeapMux wrote this row, so it carries LeapMux's source rather than the agent's")
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeInterrupted+`"}`, string(notifications[0].Content))
}

// The spans stay OPEN, because the abort reaches the model stream and not a command
// the runtime already launched. A reset would orphan the update that call still
// sends, which is the same reason endStoppedZCodeTurn closes no tool call.
func TestZCodeStoppedTurn_KeepsTheSpansOfWorkStillRunning(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())
	timer.fire()

	assert.Zero(t, sink.ResetSpanCount(), "a call still running keeps the span its update needs")
}

// A turn that reports its own end needs no row from LeapMux: the app-server's frame
// draws the divider, and the window is dropped before it can fire.
func TestZCodeStoppedTurn_WritesNoRowWhenTheTurnReportsItsOwnEnd(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())
	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"turn.completed","payload":{"resultType":"cancelled","duration":900}}}}`)))

	a.mu.Lock()
	armed := a.stoppedTurnTimer
	a.mu.Unlock()
	assert.Nil(t, armed, "the turn reported its own end, so the window is spent")
	assert.Empty(t, sink.PersistedNotifications(), "the app-server's own frame is the row")
}
