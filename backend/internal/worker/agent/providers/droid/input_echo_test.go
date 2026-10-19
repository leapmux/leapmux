package droid

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func newDroidInputHarness(t *testing.T) (*Agent, *agenttest.Sink, <-chan droidEnvelope, *quartz.Mock) {
	t.Helper()
	writer := &droidRequestWriter{requests: make(chan droidEnvelope, 10)}
	sink := &agenttest.Sink{}
	clock := testutil.NewQuartzMock(t)
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "input-test", Stdin: writer, Ctx: t.Context(), ProcessDone: make(chan struct{}),
			Clock: clock, APITimeout: time.Minute,
		}),
		sink: agent.NewProviderServices(sink), sessionID: "main-session",
	}
	return a, sink, writer.requests, clock
}

func answerDroidInput(t *testing.T, a *Agent, id string, refused bool) {
	t.Helper()
	response := newDroidEnvelope(droidTypeResponse)
	response.ID = id
	if refused {
		response.Error = &droidRPCError{Code: -32602, Message: "input refused"}
	} else {
		response.Result = json.RawMessage(`{}`)
	}
	raw, err := response.Marshal()
	require.NoError(t, err)
	a.HandleOutput(raw)
}

func receiveDroidInput(t *testing.T, requests <-chan droidEnvelope) droidEnvelope {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-t.Context().Done():
		t.Fatal("the input request did not arrive")
		return droidEnvelope{}
	}
}

func TestAddUserMessageErrorReleasesTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	done := make(chan error, 1)
	go func() { done <- a.SendInput("refused", nil) }()
	request := receiveDroidInput(t, requests)
	answerDroidInput(t, a, request.ID, true)
	require.ErrorContains(t, <-done, "input refused")
	assert.False(t, a.PublishTurnActive().Active)
	assert.Empty(t, sink.SteerEchoReports())
}

func TestQueuedMessagesDiscardedReportsPendingSteers(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	a.startTurn()
	for _, text := range []string{"first", "second"} {
		done := make(chan error, 1)
		go func() { done <- a.SteerInput(text, nil) }()
		request := receiveDroidInput(t, requests)
		answerDroidInput(t, a, request.ID, false)
		require.NoError(t, <-done)
	}
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"queued_messages_discarded","text":"first\nsecond"}`)))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 2}}, sink.SteerEchoReports())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
}

func TestDroidSteerEchoPrecedesTheReplyAndSurvivesTheTurnEnd(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	a.startTurn()
	done := make(chan error, 1)
	go func() { done <- a.SteerInput("steer", nil) }()
	request := receiveDroidInput(t, requests)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	assert.Empty(t, sink.SteerEchoReports())
	var params addUserMessageParams
	require.NoError(t, json.Unmarshal(request.Params, &params))
	require.NotEmpty(t, params.MessageID)
	echo := []byte(fmt.Sprintf(`{"type":"create_message","message":{"id":%q,"role":"user","content":[{"type":"text","text":"steer"}]}}`, params.MessageID))
	a.HandleOutput(droidAuditNotification(t, "", echo))
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false", "turn_active:true", "steer-echoed:1"}, sink.TurnLifecycle())
	assert.True(t, a.PublishTurnActive().Active, "the echoed input starts work before the queue releases another input")
	answerDroidInput(t, a, request.ID, true)
	require.NoError(t, <-done, "the native echo proves delivery despite a later error")
	a.HandleOutput(droidAuditNotification(t, "", echo))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	require.Len(t, sink.Messages(), 1, "only the earlier turn end is a native row")
}

func TestDroidSteerRefusalDoesNotReportADrop(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	a.startTurn()
	done := make(chan error, 1)
	go func() { done <- a.SteerInput("refused", nil) }()
	request := receiveDroidInput(t, requests)
	answerDroidInput(t, a, request.ID, true)
	require.ErrorContains(t, <-done, "input refused")
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"queued_messages_discarded","text":"external input"}`)))
	assert.Empty(t, sink.SteerEchoReports())
	require.Len(t, sink.PersistedNotifications(), 1)
}

func droidInputEcho(t *testing.T, request droidEnvelope) []byte {
	t.Helper()
	var params addUserMessageParams
	require.NoError(t, json.Unmarshal(request.Params, &params))
	require.NotEmpty(t, params.MessageID)
	return []byte(fmt.Sprintf(`{"type":"create_message","message":{"id":%q,"role":"user","content":[{"type":"text","text":%q}]}}`, params.MessageID, params.Text))
}

func TestDroidSteerDiscardBeforeReplyReportsOnce(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	a.startTurn()
	done := make(chan error, 1)
	go func() { done <- a.SteerInput("discard", nil) }()
	request := receiveDroidInput(t, requests)
	a.HandleOutput(droidAuditNotification(t, "", []byte(fmt.Sprintf(`{"type":"queued_messages_discarded","requestId":%q,"text":"discard"}`, request.ID))))
	answerDroidInput(t, a, request.ID, true)
	require.NoError(t, <-done)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestDroidSteerEchoWaitsBehindAFailedWrite(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	recorder := &droidSteerCaptureSink{Sink: sink}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(recorder))
	a.startTurn()
	done := make(chan error, 1)
	go func() { done <- a.SteerInput("steer", nil) }()
	request := receiveDroidInput(t, requests)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"before","textDelta":"before the steer"}`)))
	sink.PersistErr = assert.AnError
	a.HandleOutput(droidAuditNotification(t, "", droidInputEcho(t, request)))
	assert.Empty(t, sink.SteerEchoReports())
	answerDroidInput(t, a, request.ID, true)
	require.NoError(t, <-done, "a retained echo still proves delivery")
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"create_message","message":{"id":"after","role":"assistant","content":[{"type":"text","text":"after the steer"}]}}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	failed := len(sink.Messages())
	sink.PersistErr = nil
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"droid_working_state_changed","newState":"idle"}`)))
	rows := sink.Messages()[failed:]
	require.Len(t, rows, 4)
	assert.Contains(t, string(rows[0].Content), "before the steer")
	assert.Contains(t, string(rows[1].Content), "after the steer")
	assert.Equal(t, failed+1, recorder.rowsAtEcho, "the echo follows the first row and precedes the second")
	assert.True(t, recorder.activeAtEcho, "the later turn end cannot release the queue before the retained echo")
	active, _ := sink.LastTurnActive()
	assert.False(t, active, "the queue releases after the retained turn end")
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

type droidSteerCaptureSink struct {
	*agenttest.Sink
	rowsAtEcho   int
	activeAtEcho bool
}

func (sink *droidSteerCaptureSink) CaptureSteerReport(count int, outcome agent.SteerOutcome) func() error {
	report := sink.Sink.CaptureSteerReport(count, outcome)
	return func() error {
		if err := report(); err != nil {
			return err
		}
		sink.rowsAtEcho = len(sink.Messages())
		sink.activeAtEcho, _ = sink.LastTurnActive()
		return nil
	}
}

type droidFailedInputWriter struct{ written int }

func (writer droidFailedInputWriter) Write([]byte) (int, error) {
	return writer.written, io.ErrClosedPipe
}

func (droidFailedInputWriter) Close() error { return nil }

func TestDroidInputWriteFailurePreservesDeliveryCertainty(t *testing.T) {
	t.Parallel()
	for _, written := range []int{0, 2} {
		t.Run(fmt.Sprint(written), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{
				Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
					AgentID: "write-test", Stdin: droidFailedInputWriter{written: written}, Ctx: t.Context(), ProcessDone: make(chan struct{}),
				}),
				sink: agent.NewProviderServices(sink), sessionID: "main-session",
			}
			a.startTurn()
			err := a.SteerInput("write failure", nil)
			require.ErrorIs(t, err, io.ErrClosedPipe)
			if written == 0 {
				assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
			} else {
				assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			assert.Empty(t, a.steers.Pending())
			assert.Empty(t, sink.SteerEchoReports())
		})
	}
}

func TestDroidInputExitBeforeReplyIsUncertain(t *testing.T) {
	t.Parallel()
	processDone := make(chan struct{})
	a, sink, writer := newSteerAgentWithProcessDone(t, processDone)
	writer.respond = nil
	a.startTurn()
	done := make(chan error, 1)
	go func() { done <- a.SteerInput("input", nil) }()
	<-writer.written
	close(processDone)
	require.ErrorIs(t, <-done, agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.SteerEchoReports())
}

func TestDroidSteerMatchesIdentityInsteadOfEqualText(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	a.startTurn()
	var sent []droidEnvelope
	for range 2 {
		done := make(chan error, 1)
		go func() { done <- a.SteerInput("same", nil) }()
		request := receiveDroidInput(t, requests)
		sent = append(sent, request)
		answerDroidInput(t, a, request.ID, false)
		require.NoError(t, <-done)
	}
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"create_message","message":{"id":"ordinary","role":"user","content":[{"type":"text","text":"same"}]}}`)))
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(droidAuditNotification(t, "", droidInputEcho(t, sent[1])))
	assert.Empty(t, sink.SteerEchoReports(), "a later echo waits for the first steer")
	a.HandleOutput(droidAuditNotification(t, "", droidInputEcho(t, sent[0])))
	a.HandleOutput(droidAuditNotification(t, "", droidInputEcho(t, sent[1])))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 2}}, sink.SteerEchoReports())
	assert.Empty(t, sink.Messages(), "the queue already owns user transcript rows")
}

func TestDroidSteerTimeoutKeepsDeliveryUncertain(t *testing.T) {
	t.Parallel()
	a, sink, requests, clock := newDroidInputHarness(t)
	a.startTurn()
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, droidMethodAddUserMessage)
	t.Cleanup(trap.Close)
	done := make(chan error, 1)
	go func() { done <- a.SteerInput("uncertain", nil) }()
	receiveDroidInput(t, requests)
	testutil.WaitForTimer(t, testutil.DeadlineContext(t), trap)
	clock.Advance(time.Minute).MustWait(testutil.DeadlineContext(t))
	require.ErrorIs(t, <-done, agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.SteerEchoReports())
	assert.Empty(t, a.steers.Pending())
}
