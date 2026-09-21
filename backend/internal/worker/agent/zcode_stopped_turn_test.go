package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type failingZCodeNotificationSink struct {
	*testSink
}

func (s *failingZCodeNotificationSink) PersistNotification(
	leapmuxv1.MessageSource,
	[]byte,
) (bool, error) {
	return false, errors.New("notification store unavailable")
}

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
	armed := a.stopWindow.timer
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
	armed := a.stopWindow.timer
	a.mu.Unlock()
	assert.Nil(t, armed, "the turn reported its own end, so the window is spent")
	assert.Empty(t, sink.PersistedNotifications(), "the app-server's own frame is the row")
}

// The window belongs to a LIVE turn. Once the agent goes down, a frame the read
// loop still delivers must not arm one: the callback would end a turn the
// tear-down already ended and write its stop row after it.
func TestZCodeStoppedTurn_ArmingRefusesWhileTheAgentGoesDown(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgentWithStdin(t, &testSink{}, &zcodeRecordedStdin{})
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	a.mu.Lock()
	a.armStoppedZCodeTurnLocked()
	a.mu.Unlock()
	require.Equal(t, 1, timer.armed)

	// The order Stop uses: mark the graceful stop, then drop the window.
	a.noteIntentionalStop()
	a.cancelStoppedZCodeTurn()
	a.mu.Lock()
	a.armStoppedZCodeTurnLocked()
	a.mu.Unlock()

	assert.Equal(t, 1, timer.armed, "an agent that goes down arms no window")
	a.mu.Lock()
	armed := a.stopWindow.timer
	a.mu.Unlock()
	assert.Nil(t, armed)
}

// time.Timer.Stop cannot stop a callback that already started, so a cancel loses
// that race whenever the window fires first. The callback then finds the turn
// over and states nothing: the app-server's own frame already drew the divider,
// and a second row would tell the reader the stop ended a turn it did not.
func TestZCodeStoppedTurn_WritesNoRowAfterTheTurnAlreadyEnded(t *testing.T) {
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

	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"turn.completed","payload":{"resultType":"cancelled","duration":900}}}}`)))
	timer.fire()

	assert.Empty(t, sink.PersistedNotifications(), "the turn ended before the window did")
}

// A frame that arrives within the grace window is a turn that may yet fall
// silent -- no ignored-stop row may exist yet.
func TestZCodeStoppedTurn_WritesNoIgnoredRowInsideTheGrace(t *testing.T) {
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

	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"text.delta","payload":{"text":"winding down"}}}}`)))

	assert.Empty(t, sink.PersistedNotifications(),
		"a turn still inside its grace owes the reader no verdict on the stop")
}

// The shipped app-server clears the turn's abort controller at admission, so a
// stop that lands after those first moments aborts nothing and the turn keeps
// SPEAKING. Events past the grace are that no-op: the first one writes the
// stop-ignored row, and only the first -- the reader is told once, not once per
// frame the ignored turn goes on producing.
func TestZCodeStoppedTurn_WritesOneIgnoredRowWhenTheAgentKeepsSpeaking(t *testing.T) {
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
	// The accepted stop is now older than the grace, exactly as a live run is
	// by the time the ignored turn's next frame arrives.
	a.mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.mu.Unlock()

	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"text.delta","payload":{"text":"still going"}}}}`)))
	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"tool.updated","payload":{"toolName":"bash"}}}}`)))

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "one row per accepted stop, however many frames follow")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, notifications[0].Source)
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeStopIgnored+`"}`, string(notifications[0].Content))
	assert.Equal(t, 1, sink.InterruptIgnoredReports(),
		"the ignored interrupt restores the activity that makes another Interrupt available")
}

func TestZCodeStoppedTurn_ReportsTheIgnoredInterruptWhenTheNotificationWriteFails(t *testing.T) {
	t.Parallel()

	sink := &failingZCodeNotificationSink{testSink: &testSink{}}
	a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})

	a.persistZCodeStopIgnoredRow()

	assert.Equal(t, 1, sink.InterruptIgnoredReports(),
		"a transcript failure must not leave the Interrupt button hidden")
	assert.Empty(t, sink.PersistedNotifications())
}

// Escalation is the SECOND press. It may not fire while the first stop is still
// inside its grace -- a double-click must retry the plain stop, not restart the
// agent -- and it may not fire once the turn has ended, because then the stop
// worked and there is nothing left to force.
func TestZCodeInterruptEscalationReady(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &testSink{}, stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()

	assert.False(t, a.InterruptEscalationReady(), "no stop has been accepted yet")

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt())
	assert.False(t, a.InterruptEscalationReady(), "a stop inside its grace has not been judged yet")

	a.mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.mu.Unlock()
	assert.True(t, a.InterruptEscalationReady(), "an accepted stop the turn outlived may be escalated")

	handleZCodeOutput(a, parseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"turn.completed","payload":{"resultType":"success","duration":1000}}}}`)))
	assert.False(t, a.InterruptEscalationReady(), "a stop the turn answered leaves nothing to escalate")
}

// The forced stop ends the turn by replacing the process, and Stop is where the
// replacement passes through. A stop that was still pending there is one the
// app-server already ignored, so the teardown owes the reader the stop row the
// app-server never drew.
func TestZCodeStop_WritesTheStopRowWhenAStopWasPending(t *testing.T) {
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

	// Stop's own final session/stop, and its wait for the process it tears down.
	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	close(a.processDone)
	a.Stop()

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeInterrupted+`"}`, string(notifications[0].Content))
}

// The window's own callback can fire while Stop runs. time.Timer.Stop cannot stop a
// callback that already began, and Stop never clears turnActive -- so the callback's
// turnActive guard did not refuse it, and the reader got TWO interrupted rows for one
// Stop press. They do not even fold into one notification thread, because the persists
// Stop makes between them break it.
//
// The generation the cancel raises is what refuses the late callback now.
func TestZCodeStop_WritesOneStopRowWhenTheWindowFiresDuringTheStop(t *testing.T) {
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
	require.Equal(t, 1, timer.armed, "the stop arms the silence window")
	fire := timer.fire
	require.NotNil(t, fire)

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionStop, `{}`)
	close(a.processDone)
	a.Stop()
	// The callback the cancel could not stop, arriving after Stop already recorded
	// the row the window earned.
	fire()

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "one Stop press earns one interrupted row")
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeInterrupted+`"}`, string(notifications[0].Content))
}
