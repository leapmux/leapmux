package cline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestSendInputStartsATurnOnceClineConfirmsIt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	assert.True(t, r.turnActive())
	command := r.hub.commandsNamed(commandSessionSendInput)[0]
	assert.Equal(t, r.sessionID(), command.str("sessionId"))
	assert.Equal(t, "Hello.", command.str("prompt"))
	assert.Equal(t, sessionModeAct, command.str("mode"))
	assert.Empty(t, command.str("delivery"), "a plain send starts a turn")
	last, ok := r.sink.LastTurnActive()
	require.True(t, ok)
	assert.True(t, last, "the turn is published")

	r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
	last, _ = r.sink.LastTurnActive()
	assert.False(t, last, "the run's end clears the turn")
}

func TestSendInputRefusesWhileATurnRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "First.")
	err := r.agent.SendInput("Second.", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, &r.sink.Sink, r.agent, err)
	var busy *agent.AgentBusyError
	require.ErrorAs(t, err, &busy)
	assert.True(t, busy.ActiveTurnSteerable, "a turn that LeapMux started takes a steer")
	assert.Len(t, r.hub.commandsNamed(commandSessionSendInput), 1, "the refused message never leaves")
}

func TestAgentPublishesRisingTurnTokens(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.noSession = true })
	agenttest.AssertRisingTurnTokens(t, &r.sink.Sink, r.agent)
}

func TestSendInputForSessionRejectsMissingAndReplacedSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, r.agent)
	assert.Empty(t, r.hub.commandsNamed(commandSessionSendInput))
	require.NoError(t, r.agent.SendInputForSession(r.sessionID(), "Current.", nil))
}

func TestSendInputReleasesTheTurnWhenClineRefusesTheMessage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.handle(commandSessionSendInput, func(fakeCommand) fakeReply {
		return fakeReply{Code: "invalid_session_input", Message: "session input requires a prompt or attachment"}
	})
	err := r.agent.SendInput("Hello.", nil)
	var refused *HubCommandError
	require.ErrorAs(t, err, &refused)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "a refusal is certain")
	assert.False(t, r.turnActive(), "a refused message holds no turn")
	last, _ := r.sink.LastTurnActive()
	assert.False(t, last)
}

func TestSendInputReportsAnUnconfirmedDelivery(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.sendWait = 50 * time.Millisecond
	// The hub reads the command and never starts the run.
	r.hub.handle(commandSessionSendInput, func(fakeCommand) fakeReply { return fakeReply{Hold: true} })
	err := r.agent.SendInput("Hello.", nil)
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.True(t, r.turnActive(), "an uncertain delivery keeps the turn: the run may have started")
}

func TestSendInputReportsALostConnectionAsUncertain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.handle(commandSessionSendInput, func(fakeCommand) fakeReply {
		go r.hub.drop()
		return fakeReply{Hold: true}
	})
	err := r.agent.SendInput("Hello.", nil)
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.True(t, r.turnActive())
}

func TestSendInputRefusesAMessageWithNothingInIt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.Error(t, r.agent.SendInput("  ", nil))
	assert.False(t, r.turnActive())
	assert.Empty(t, r.hub.commandsNamed(commandSessionSendInput))
}

func TestSendInputRefusesAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Stop()
	require.ErrorIs(t, r.agent.SendInput("Hello.", nil), errAgentStopped)
}

func TestATurnReplyThatFailsAfterTheStartEndsTheTurnAsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.hub.reply(requestID, fakeReply{Code: "runtime_error", Message: "the runtime crashed"})
	waitFor(t, func() bool { return !r.turnActive() }, "the failed reply ends the turn")
	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.True(t, last.TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, last.Completion)
	assert.Equal(t, contracts.ClineEventRunFailed, decode(t, last.Content)["event"])
	assert.Contains(t, payloadOf(t, last)["error"], "the runtime crashed")
}

func TestSteerInputJoinsTheRunningTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.ErrorIs(t, r.agent.SteerInput("Early.", nil), agent.ErrNoActiveTurn)
	r.startTurn(t, "Hello.")
	require.True(t, r.agent.SupportsSteering())
	require.NoError(t, r.agent.SteerInput("Also this.", nil))
	commands := r.hub.commandsNamed(commandSessionSendInput)
	require.Len(t, commands, 2)
	assert.Equal(t, deliverySteer, commands[1].str("delivery"))
	assert.Equal(t, "Also this.", commands[1].str("prompt"))
}

func TestSteerInputRefusesATurnClineStartedByItself(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.ensureTurn()
	err := r.agent.SteerInput("Hello.", nil)
	var busy *agent.AgentBusyError
	require.ErrorAs(t, err, &busy)
	assert.False(t, busy.ActiveTurnSteerable)
	assert.Empty(t, r.hub.commandsNamed(commandSessionSendInput))
}

func TestSteerInputReportsARefusal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	r.hub.handle(commandSessionSendInput, func(fakeCommand) fakeReply {
		return fakeReply{Code: "session_not_running", Message: "no run"}
	})
	err := r.agent.SteerInput("Late.", nil)
	var refused *HubCommandError
	require.ErrorAs(t, err, &refused)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
}

func TestARunThatClineStartsByItselfTakesATurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(eventSessionUpdated, map[string]any{"sessionId": r.sessionID(), "snapshot": map[string]any{"status": "running"}})
	waitFor(t, r.turnActive, "a running session holds a turn")
	r.agent.Mu.Lock()
	steerable := r.agent.turn.steerable
	r.agent.Mu.Unlock()
	assert.False(t, steerable, "a run Cline started by itself takes no steer")

	r.emit(contracts.ClineEventRunCompleted, map[string]any{"reason": "completed"})
	waitFor(t, func() bool { return !r.turnActive() }, "its end ends the turn")
}

func TestAnEventOfAnotherSessionIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.HandleOutput(eventEnvelope(t, "other-session", contracts.ClineEventAssistantFinished, map[string]any{"text": "Not mine."}))
	r.agent.HandleOutput(eventEnvelope(t, "other-session", eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": "running"}}))
	assert.Zero(t, r.sink.MessageCount())
	assert.False(t, r.turnActive())
}

func TestHandleOutputDropsAnUnreadableEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.HandleOutput([]byte(`not json`))
	r.agent.HandleOutput([]byte(`{"payload":{}}`))
	assert.Zero(t, r.sink.MessageCount())
}

func TestTurnFramesMoveOnlyNamedSignals(t *testing.T) {
	t.Parallel()
	cases := []agenttest.TurnFrameCase{
		{Name: "a running session", Line: `{"event":"session.updated","payload":{"snapshot":{"status":"running"}}}`, Moves: true},
		{Name: "a run of another client", Line: `{"event":"run.started","payload":{"requestId":"other_1","clientId":"other"}}`, Moves: true},
		{Name: "an iteration of a run Cline started", Line: `{"event":"iteration.started","payload":{"iteration":1}}`, Moves: true},
		{Name: "text of a run Cline started", Line: `{"event":"assistant.delta","payload":{"text":"a"}}`, Moves: true},
		{Name: "an idle session", Line: `{"event":"session.updated","payload":{"snapshot":{"status":"idle"}}}`},
		{Name: "a pending session", Line: `{"event":"session.updated","payload":{"snapshot":{"status":"pending"}}}`},
		{Name: "a run's end with no turn", Line: `{"event":"run.completed","payload":{"reason":"completed"}}`},
		{Name: "a heartbeat", Line: `{"event":"run.heartbeat","payload":{}}`},
		{Name: "usage", Line: `{"event":"usage.updated","payload":{"delta":{"inputTokens":1},"agent":{"kind":"lead"}}}`},
		{Name: "a hook's tool copy", Line: `{"event":"tool.started","payload":{"toolName":"read_files"}}`},
		{Name: "a tool finish with no start", Line: `{"event":"tool.finished","payload":{"toolCallId":"x","toolName":"read_files"}}`},
		{Name: "an approval resolution", Line: `{"event":"approval.resolved","payload":{"approvalId":"a"}}`},
		{Name: "a future event", Line: `{"event":"session.something_new","payload":{}}`},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.ControlSink{}
		r := newRig(t, func(c *rigConfig) { c.sink = sink })
		before := len(sink.TurnActives())
		var frame map[string]any
		require.NoError(t, json.Unmarshal([]byte(tc.Line), &frame))
		r.feed(t, frame["event"].(string), frame["payload"])
		return sink.TurnActives()[before:]
	})
}

func TestAttachmentsReachClineAsImagesAndFiles(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	attachments := []*leapmuxv1.Attachment{
		{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}},
		{Filename: "notes.md", MimeType: "text/markdown", Data: []byte("# Notes\n")},
	}
	require.NoError(t, r.agent.SendInput("Look.", attachments))
	command, ok := r.hub.waitCommand(commandSessionSendInput)
	require.True(t, ok)
	var files struct {
		UserImages []string `json:"userImages"`
		UserFiles  []string `json:"userFiles"`
	}
	require.True(t, command.field("attachments", &files))
	require.Len(t, files.UserImages, 1)
	assert.Contains(t, files.UserImages[0], "data:image/png;base64,")
	require.Len(t, files.UserFiles, 1)
	assert.FileExists(t, files.UserFiles[0])
}

func TestSendInputRefusesAnAttachmentClineCannotRead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	err := r.agent.SendInput("Read.", []*leapmuxv1.Attachment{{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PDF")
	assert.False(t, r.turnActive())
	assert.Empty(t, r.hub.commandsNamed(commandSessionSendInput))
}

// Output that arrives with no turn opens one that Cline started. Cline ends such
// a run with no run event when the output was a stray one, so the session's own
// idle status ends the turn, or the input queue would wait forever.
func TestAStrayTurnEndsWhenTheSessionIsIdle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, eventAssistantDelta, map[string]any{"text": "late"})
	require.True(t, r.turnActive(), "stray output opens a turn")
	r.feed(t, eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": "idle"}})
	assert.False(t, r.turnActive(), "the idle session ends it")
}

// A turn that a LeapMux message started ends with its run, never with a status.
func TestASentTurnOutlivesAnIdleStatus(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	r.feed(t, eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": "idle"}})
	assert.True(t, r.turnActive())
}

// Cline publishes the session's `idle` status just before the end event of a
// run (Cline 3.0.64 publishes both in the same millisecond). A turn whose run
// the worker saw start ends with that end event and its row, not with the
// status.
func TestATurnThatClineStartedEndsWithItsRunAfterTheIdleStatus(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.feed(t, eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": sessionStatusRunning}})
	require.True(t, r.turnActive(), "the running status opens a turn")
	r.feed(t, eventAssistantDelta, map[string]any{"text": "Team result received."})
	r.feed(t, eventSessionUpdated, map[string]any{"snapshot": map[string]any{"status": sessionStatusIdle}})
	require.True(t, r.turnActive(), "the idle status does not end a turn with a run")
	r.feed(t, contracts.ClineEventRunCompleted, map[string]any{"reason": contracts.ClineRunReasonCompleted, "result": map[string]any{"text": "Team result received."}})
	assert.False(t, r.turnActive())
	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.Equal(t, agent.MessageCompletionComplete, last.Completion)
	assert.Equal(t, contracts.ClineEventRunCompleted, decode(t, last.Content)["event"], "the row is Cline's own end event")
}

// A message that cannot leave never reached Cline, so the send fails as certain
// and the turn goes: whether the client closed, or its connection failed after
// the send recorded its request id.
func TestSendInputReleasesTheTurnWhenTheMessageCannotLeave(t *testing.T) {
	t.Parallel()
	t.Run("a closed client", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.hub.close()
		err := r.agent.SendInput("Hello.", nil)
		require.ErrorIs(t, err, errHubClosed)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.False(t, r.turnActive())
		last, _ := r.sink.LastTurnActive()
		assert.False(t, last)
	})
	t.Run("a connection that failed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.hub.setRefuseUpgrades(true)
		r.agent.hub.mu.Lock()
		conn := r.agent.hub.conn
		r.agent.hub.mu.Unlock()
		require.NoError(t, conn.CloseNow())
		err := r.agent.SendInput("Hello.", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "the frame never left")
		assert.Contains(t, err.Error(), "deliver the message to Cline")
		assert.False(t, r.turnActive())
		r.agent.Mu.Lock()
		waiting := len(r.agent.deliveries)
		r.agent.Mu.Unlock()
		assert.Zero(t, waiting, "no delivery waits for a run that cannot start")
		assert.Empty(t, r.hub.commandsNamed(commandSessionSendInput))
	})
}

// A run can start after the send gave up waiting for it. The run is the one
// that the send started, so its turn stays the steerable turn of the message,
// the interrupt aborts the run at once, and the run's end ends the turn.
func TestARunThatStartsAfterTheSenderGaveUpKeepsItsTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.sendWait = 50 * time.Millisecond
	sent := make(chan string, 1)
	r.hub.handle(commandSessionSendInput, func(c fakeCommand) fakeReply {
		sent <- c.RequestID
		return fakeReply{Hold: true}
	})
	require.ErrorIs(t, r.agent.SendInput("Hello.", nil), agent.ErrDeliveryUncertain)
	requestID := <-sent

	r.emit(eventRunStarted, map[string]any{"requestId": requestID, "clientId": r.agent.clientID})
	waitFor(t, func() bool {
		r.agent.Mu.Lock()
		defer r.agent.Mu.Unlock()
		return r.agent.turn.runStarted
	}, "the run's start reaches the turn")
	r.agent.Mu.Lock()
	turn := r.agent.turn
	r.agent.Mu.Unlock()
	assert.True(t, turn.steerable, "the message's turn takes a steer")
	assert.Equal(t, requestID, turn.requestID)

	require.NoError(t, r.agent.Interrupt())
	_, ok := r.hub.waitCommand(commandRunAbort)
	require.True(t, ok, "a turn whose run started aborts it at once")
	r.endRun(t, requestID, contracts.ClineRunReasonAborted)
	messages := r.sink.Messages()
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
}

func TestSteerInputReportsALostConnectionAsUncertain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	r.hub.handle(commandSessionSendInput, func(fakeCommand) fakeReply {
		go r.hub.drop()
		return fakeReply{Hold: true}
	})
	err := r.agent.SteerInput("Also this.", nil)
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.True(t, r.turnActive(), "the steer changes nothing about the turn")
}

func TestSteerInputRefusesWhatCannotJoinTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.startTurn(t, "Hello.")
	err := r.agent.SteerInput("Read.", []*leapmuxv1.Attachment{{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PDF")
	require.Error(t, r.agent.SteerInput(" ", nil), "a steer with nothing in it")
	assert.Len(t, r.hub.commandsNamed(commandSessionSendInput), 1, "no refused steer leaves")

	r.agent.Stop()
	require.ErrorIs(t, r.agent.SteerInput("Late.", nil), errAgentStopped)
}
