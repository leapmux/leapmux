package zcode

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type failingZCodeNotificationSink struct {
	*agenttest.Sink
}

func (s *failingZCodeNotificationSink) PersistNotification(
	leapmuxv1.MessageSource,
	agent.MessageContent,
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
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	a.Mu.Lock()
	turnActive := a.turnActive
	a.Mu.Unlock()
	assert.True(t, turnActive, "the agent has not stopped yet, so the chat must not read idle")
	assert.Equal(t, 1, timer.armed, "the stop arms the silence fallback")
}

// Only the stopped turn's own speech restarts the window. A model stream event
// is that speech: a frame is proof the turn is alive.
func TestZCodeInterrupt_AModelStreamEventRefreshesTheFallback(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)

	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"model.streaming","payload":{"kind":"text_delta","assistantMessageId":"m1","delta":"still working"}}}}`)))

	assert.Equal(t, 2, timer.armed, "a frame of the stopped turn after the stop restarts the window")
}

// Background traffic says nothing about the stopped USER turn. A background
// task's lifecycle, a subagent's traffic and a background turn's own stream
// must not postpone the window that ends the user's turn.
func TestZCodeStoppedTurn_BackgroundTaskTrafficDoesNotRefreshTheWindow(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn-bg", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":2,"inputSource":"goal_state_change","foregroundExecutionId":"exec-u"}`))
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"task-1","status":"running"}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
		`{"agentId":"agent-1","parentToolCallId":"call-1","status":"running"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "turn-bg", contracts.ZCodeEventModelStreaming,
		`{"kind":"text_delta","assistantMessageId":"m2","delta":"background work"}`))

	assert.Equal(t, 1, timer.armed, "background traffic must not keep the stopped user turn alive")
}

// The stopped turn's tool and model events are its own speech: they restart the
// window, and the first one past the grace proves the stop was ignored.
func TestZCodeStoppedTurn_ToolAndModelEventsRefreshAndProveAnIgnoredStop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput(zcodeTurnEventLine(t, 0, "turn-user", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":1,"input":"hi"}`))
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	var ignored atomic.Int32
	require.NoError(t, a.Interrupt(agent.NewStopContext(func() { ignored.Add(1) })))
	require.Equal(t, 1, timer.armed)
	// The accepted stop is now older than the grace, exactly as a live run is
	// by the time the ignored turn's next frame arrives.
	a.Mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.Mu.Unlock()

	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn-user", contracts.ZCodeEventModelStreaming,
		`{"kind":"text_delta","assistantMessageId":"m1","delta":"still going"}`))
	assert.Equal(t, 2, timer.armed, "the model stream of the stopped turn restarts the window")

	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn-user", contracts.ZCodeEventToolUpdated,
		`{"toolCallId":"call-1","toolName":"bash"}`))
	assert.Equal(t, 3, timer.armed, "a tool update of the stopped turn restarts the window")

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "one row per accepted stop, however many frames follow")
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeStopIgnored+`"}`, string(notifications[0].Content))
	assert.Equal(t, int32(1), ignored.Load(),
		"the ignored interrupt restores the activity that makes another Interrupt available")
}

// An agent that was never stopped arms nothing, so an ordinary turn pays no cost.
func TestZCodeOutput_AnEventOfARetiredSessionDoesNotRefreshTheStopWindow(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.armStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	require.Equal(t, 1, timer.armed)
	a.HandleOutput([]byte(`{"method":"session/event","params":{"sessionId":"retired","seq":2,"type":"turn.started","payload":{}}}`))
	assert.Equal(t, 1, timer.armed)
	a.cancelStoppedZCodeTurn()
}

func TestZCodeOutput_ASessionEventWithNoStopArmsNothing(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"text.delta","payload":{"text":"working"}}}}`)))

	assert.Equal(t, 0, timer.armed)
}

// A replaced session drops every piece of per-session state, and the window is one of
// them. Left armed, it would fire into the NEW session and flush a buffer that belongs
// to nothing.
func TestZCodeClearContext_DropsTheStoppedTurnWindow(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.cancelStoppedZCodeTurn()

	a.Mu.Lock()
	armed := a.stopWindow.timer
	a.Mu.Unlock()
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
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
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
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	timer.fire()

	assert.Zero(t, sink.ResetSpanCount(), "a call still running keeps the span its update needs")
}

// A turn that reports its own end needs no row from LeapMux: the app-server's frame
// draws the divider, and the window is dropped before it can fire.
func TestZCodeStoppedTurn_WritesNoRowWhenTheTurnReportsItsOwnEnd(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"turn.completed","payload":{"resultType":"cancelled","duration":900}}}}`)))

	a.Mu.Lock()
	armed := a.stopWindow.timer
	a.Mu.Unlock()
	assert.Nil(t, armed, "the turn reported its own end, so the window is spent")
	assert.Empty(t, sink.PersistedNotifications(), "the app-server's own frame is the row")
}

// The window belongs to a LIVE turn. Once the agent goes down, a frame the read
// loop still delivers must not arm one: the callback would end a turn the
// tear-down already ended and write its stop row after it.
func TestZCodeStoppedTurn_ArmingRefusesWhileTheAgentGoesDown(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), &zcodeRecordedStdin{})
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	a.Mu.Lock()
	a.armStoppedZCodeTurnLocked()
	a.Mu.Unlock()
	require.Equal(t, 1, timer.armed)

	// The order Stop uses: mark the graceful stop, then drop the window.
	a.NoteIntentionalStop()
	a.cancelStoppedZCodeTurn()
	a.Mu.Lock()
	a.armStoppedZCodeTurnLocked()
	a.Mu.Unlock()

	assert.Equal(t, 1, timer.armed, "an agent that goes down arms no window")
	a.Mu.Lock()
	armed := a.stopWindow.timer
	a.Mu.Unlock()
	assert.Nil(t, armed)
}

// time.Timer.Stop cannot stop a callback that already started, so a cancel loses
// that race whenever the window fires first. The callback then finds the turn
// over and states nothing: the app-server's own frame already drew the divider,
// and a second row would tell the reader the stop ended a turn it did not.
func TestZCodeStoppedTurn_WritesNoRowAfterTheTurnAlreadyEnded(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)

	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"turn.completed","payload":{"resultType":"cancelled","duration":900}}}}`)))
	timer.fire()

	assert.Empty(t, sink.PersistedNotifications(), "the turn ended before the window did")
}

// A frame that arrives within the grace window is a turn that may yet fall
// silent -- no ignored-stop row may exist yet.
func TestZCodeStoppedTurn_WritesNoIgnoredRowInsideTheGrace(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"model.streaming","payload":{"kind":"text_delta","assistantMessageId":"m1","delta":"winding down"}}}}`)))

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
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	var ignored atomic.Int32
	require.NoError(t, a.Interrupt(agent.NewStopContext(func() { ignored.Add(1) })))
	// The accepted stop is now older than the grace, exactly as a live run is
	// by the time the ignored turn's next frame arrives.
	a.Mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.Mu.Unlock()

	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"model.streaming","payload":{"kind":"text_delta","assistantMessageId":"m1","delta":"still going"}}}}`)))
	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"tool.updated","payload":{"toolName":"bash"}}}}`)))

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "one row per accepted stop, however many frames follow")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, notifications[0].Source)
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeStopIgnored+`"}`, string(notifications[0].Content))
	assert.Equal(t, int32(1), ignored.Load(),
		"the ignored interrupt restores the activity that makes another Interrupt available")
}

func TestZCodeStoppedTurn_ReportsTheIgnoredInterruptWhenTheNotificationWriteFails(t *testing.T) {
	t.Parallel()

	sink := &failingZCodeNotificationSink{Sink: &agenttest.Sink{}}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})

	var ignored atomic.Int32
	a.persistZCodeStopIgnoredRow([]agent.StopContext{agent.NewStopContext(func() { ignored.Add(1) })})

	assert.Equal(t, int32(1), ignored.Load(),
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
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	assert.False(t, a.InterruptEscalationReady(), "no stop has been accepted yet")

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.False(t, a.InterruptEscalationReady(), "a stop inside its grace has not been judged yet")

	a.Mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.Mu.Unlock()
	assert.True(t, a.InterruptEscalationReady(), "an accepted stop the turn outlived may be escalated")

	handleZCodeOutput(a, providerkit.ParseLine([]byte(`{"method":"session/event","params":{"sessionId":"sess-1","event":{"type":"turn.completed","payload":{"resultType":"success","duration":1000}}}}`)))
	assert.False(t, a.InterruptEscalationReady(), "a stop the turn answered leaves nothing to escalate")
}

// The forced stop ends the turn by replacing the process, and Stop is where the
// replacement passes through. A stop that was still pending there is one the
// app-server already ignored, so the teardown owes the reader the stop row the
// app-server never drew.
func TestZCodeStop_WritesTheStopRowWhenAStopWasPending(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	// Stop's own final session/stop, and its wait for the process it tears down.
	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	a.SimulateExitForTest()
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
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.turnActive = true
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed, "the stop arms the silence window")
	fire := timer.fire
	require.NotNil(t, fire)

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	a.SimulateExitForTest()
	a.Stop()
	// The callback the cancel could not stop, arriving after Stop already recorded
	// the row the window earned.
	fire()

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "one Stop press earns one interrupted row")
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeInterrupted+`"}`, string(notifications[0].Content))
}

// A stopped foreground turn's tool call keeps its running card (the abort reaches
// the model stream, not a launched command), so its late result must still route
// to the stopped owner while another turn runs -- and must leave that other
// turn's output and activity intact.
func TestZCodeStoppedTurn_LateToolResultRoutesToTheStoppedOwner(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc

	a.HandleOutput(zcodeTurnEventLine(t, 1, "u-1", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":1,"input":"hi"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "u-1", contracts.ZCodeEventToolUpdated,
		`{"kind":"scheduled","toolCallId":"t-1","toolName":"Bash","input":{"command":"sleep 90"}}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "bg-1", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":2,"inputSource":"background_task"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "bg-1", contracts.ZCodeEventModelStreaming,
		`{"assistantMessageId":"m-bg","kind":"text_delta","delta":"bg out"}`))

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NotNil(t, timer.fire, "the stop arms the silence window")
	timer.fire()

	a.HandleOutput(zcodeTurnEventLine(t, 5, "u-1", contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"t-1","result":{"content":"done"}}`))

	var closing *agenttest.Message
	for i, row := range sink.Messages() {
		if row.Closing && row.SpanID == "t-1" {
			closing = &sink.Messages()[i]
		}
	}
	require.NotNil(t, closing, "the late result still routes to the stopped owner and closes its call")
	assert.True(t, a.PublishTurnActive().Active,
		"the background turn is still running, so the chat must not read idle")
	assert.Equal(t, len("bg out"), a.generationBuffer.RetainedTextLenForTest(zcodeTextScope("m-bg")),
		"the stop settles the stopped owner only; the background turn's output stays buffered")
}

// Only the watched turn's own traffic extends the silence window. An ID-less
// session.updated extends it only when its kind reports the running turn's own
// work: the model request of the main query, or the main reply. Side queries,
// side replies and compaction progress are the session's own machinery and must
// not postpone the stopped turn's end.
func TestZCodeStoppedTurn_OnlyTheMainQueriesAndRepliesExtendTheWindow(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)

	extend := func(payload string) {
		t.Helper()
		a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, Payload: json.RawMessage(payload)})
	}

	extend(`{"type":"model_request_started","requestId":"q1"}`)
	assert.Equal(t, 2, timer.armed, "a network status with no query source is the main query")

	extend(`{"type":"model_request_completed","requestId":"q1","querySource":"main_turn"}`)
	assert.Equal(t, 3, timer.armed, "the main query's network status is the turn speaking")

	extend(`{"type":"model_request_started","requestId":"q2","querySource":"compact"}`)
	assert.Equal(t, 3, timer.armed, "a side query is not the turn's speech")

	extend(`{"content":"answer","stopReason":"stop"}`)
	assert.Equal(t, 4, timer.armed, "the main reply is the turn speaking")

	extend(`{"content":"internal","stopReason":"tool_internal"}`)
	assert.Equal(t, 4, timer.armed, "a tool-internal reply is a side reply")

	extend(`{"content":"title","querySource":"goal_summary_title","stopReason":"stop"}`)
	assert.Equal(t, 4, timer.armed, "a reply of another query source is a side reply")

	extend(`{"operationId":"c1","trigger":"auto","status":"running"}`)
	assert.Equal(t, 4, timer.armed, "compaction progress is not the stopped turn's speech")
}

// Compaction progress is the compaction's own speech: it extends a stop window
// that watches the compaction, and only such a window.
func TestZCodeStoppedTurn_CompactionProgressExtendsACompactionWindow(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.compactionSessionID = "sess-1"
	a.Mu.Unlock()
	a.PublishTurnActive()
	t.Cleanup(a.cancelStoppedZCodeTurn)

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)

	a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated,
		Payload: json.RawMessage(`{"operationId":"c1","trigger":"manual","status":"running"}`)})
	assert.Equal(t, 2, timer.armed, "the compaction's own progress keeps the compaction window alive")

	a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated,
		Payload: json.RawMessage(`{"strategy":"local_tool_result_clear"}`)})
	assert.Equal(t, 3, timer.armed, "a microcompact boundary is the compaction's own progress")
}
