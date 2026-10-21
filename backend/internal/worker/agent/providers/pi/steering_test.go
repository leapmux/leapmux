package pi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type piSteerEchoSink struct {
	*agenttest.Sink
	rowsAtEcho []agenttest.Message
}

func (s *piSteerEchoSink) CaptureSteerReport(count int, outcome agent.SteerOutcome) func() error {
	report := s.Sink.CaptureSteerReport(count, outcome)
	return func() error {
		rows := s.Messages()
		err := report()
		if err == nil && outcome == agent.SteerOutcomeEchoed {
			s.rowsAtEcho = rows
		}
		return err
	}
}

func (s *piSteerEchoSink) ReportSteersEchoed(count int) {
	s.rowsAtEcho = s.Messages()
	s.Sink.ReportSteersEchoed(count)
}

func TestPiSteerDeclaresRowAtEchoAndSurvival(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agenttest.Nop())
	echo, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok)
	assert.True(t, echo.SteerRowAtEcho())
	survivor, ok := any(a).(agent.SteerSurvivor)
	require.True(t, ok)
	assert.True(t, survivor.SteerSurvivesTurnEnd())
}

func TestPiSteerEchoReportsTheRowAtTheUserMessageEnd(t *testing.T) {
	t.Parallel()
	sink := &piSteerEchoSink{Sink: &agenttest.Sink{}}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"disposition":"queued"}`), true, ""
	})
	require.NoError(t, rig.agent.SteerInput("Reply with the single word THREE.", nil))
	assert.Empty(t, sink.SteerEchoReports())
	// Trimmed user echo from .tmp/verify-c29/pi/run3-steer/frames.jsonl.
	rig.agent.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"before"}]}}`))
	rig.agent.HandleOutput([]byte(`{"type":"queue_update","steering":[],"followUp":[]}`))
	rig.agent.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Reply with the single word THREE."}]}}`))
	rig.agent.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"OK3"}]}}`))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	require.Len(t, sink.rowsAtEcho, 1)
	assert.Contains(t, string(sink.rowsAtEcho[0].Content), "before")
	assert.Len(t, sink.Messages(), 2)
}

func TestPiSteerEchoBeforeTheReplyReportsOnce(t *testing.T) {
	t.Parallel()
	for _, disposition := range []string{"queued", "handled"} {
		sink := &agenttest.Sink{}
		rig := newPiTestRigWithClock(t, agent.NewProviderServices(sink), testutil.NewQuartzMock(t))
		rig.agent.currentTurnActive = true
		release := make(chan struct{})
		rig.holdMethod, rig.holdReply = CommandPrompt, release
		rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
			return json.RawMessage(`{"disposition":"` + disposition + `"}`), true, ""
		})
		result := make(chan error, 1)
		go func() { result <- rig.agent.SteerInput("guide", nil) }()
		require.Eventually(t, func() bool { return len(rig.requests()) > 0 }, 30*time.Second, time.Millisecond)
		echo := []byte(`{"type":"message_end","message":{"role":"user","content":"guide"}}`)
		rig.agent.HandleOutput(echo)
		assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
		close(release)
		select {
		case err := <-result:
			require.NoError(t, err)
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the steer did not receive its reply")
		}
		rig.agent.HandleOutput(echo)
		assert.Len(t, sink.SteerEchoReports(), 1)
		assert.Empty(t, sink.Messages())
	}
}

func TestPiSteerHandledByAnExtensionReportsTheEchoAtOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
		return json.RawMessage(`{"disposition":"handled"}`), true, ""
	})
	require.NoError(t, rig.agent.SteerInput("/local", nil))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestPiSteerRefusalReturnsTheErrorAndReportsNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.agent.generationBuffer.Append("answer", agent.AssembledMessageKindText, "still active", providerkit.JoinVerbatim)
	rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) { return nil, false, "steer refused" })
	err := rig.agent.SteerInput("guide", nil)
	require.ErrorContains(t, err, "steer refused")
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.SteerEchoReports())
	assert.Empty(t, sink.LeapMuxNotifications())
	assert.Empty(t, sink.Messages())
	rig.agent.flushPiGeneration(agent.MessageCompletionInterrupted)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), "still active")
}

func TestPiMalformedUserEchoKeepsAnEmptyTextSteerPending(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.steers.Add("")
	for _, content := range []string{"null", "0", "{}", "[null]", "[{}]"} {
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","content":` + content + `}}`))
	}
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","content":[{"type":"image","data":"aW1hZ2U=","mimeType":"image/png"}]}}`))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, sink.Messages())
}

func TestPiUserMessageTextPreservesTextAndRejectsMalformedContent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		content, want string
		valid         bool
	}{
		{`""`, "", true},
		{`"  text\n"`, "  text\n", true},
		{`[{"type":"text","text":"one "},{"type":"text","text":"two"}]`, "one two", true},
		{`[]`, "", true},
		{`null`, "", false},
		{`12`, "", false},
		{`[{"type":"text","text":12}]`, "", false},
	} {
		text, valid := piUserMessageText([]byte(`{"message":{"content":` + tc.content + `}}`))
		assert.Equal(t, tc.valid, valid)
		assert.Equal(t, tc.want, text)
	}
}

func TestPiSteerLostReplyIsDeliveryUncertain(t *testing.T) {
	t.Parallel()
	for _, malformed := range []bool{false, true} {
		sink := &agenttest.Sink{}
		rig := newPiTestRig(t, agent.NewProviderServices(sink))
		rig.agent.currentTurnActive = true
		rig.holdMethod, rig.holdReply = CommandPrompt, make(chan struct{})
		result := make(chan error, 1)
		go func() { result <- rig.agent.SteerInput("guide", nil) }()
		require.Eventually(t, func() bool { return len(rig.requests()) > 0 }, 30*time.Second, time.Millisecond)
		if malformed {
			request := rig.requests()[0]
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"response","id":%q,"command":"prompt","data":{}}`, request.ID)))
		} else {
			rig.agent.SimulateExitForTest()
		}
		select {
		case err := <-result:
			assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the steer did not report its delivery outcome")
		}
		assert.Empty(t, sink.SteerEchoReports())
	}
}

func TestPiSteerTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandPrompt)
	defer trap.Close()
	sink := &agenttest.Sink{}
	rig := newPiTestRigWithClock(t, agent.NewProviderServices(sink), clock)
	rig.agent.currentTurnActive = true
	rig.holdMethod, rig.holdReply = CommandPrompt, make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- rig.agent.SteerInput("guide", nil) }()
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, 2*time.Second, delay)
	clock.Advance(delay).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assert.Empty(t, sink.SteerEchoReports())
}

func TestPiClearContextDropsPendingSteers(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		switch req.Type {
		case CommandPrompt:
			return json.RawMessage(`{"disposition":"queued"}`), true, ""
		case CommandNewSession:
			return json.RawMessage(`{"cancelled":false}`), true, ""
		case CommandGetState:
			return json.RawMessage(`{"sessionId":"fresh","sessionFile":"/tmp/pi-fresh.jsonl"}`), true, ""
		}
		return nil, true, ""
	})
	require.NoError(t, rig.agent.SteerInput("guide", nil))
	_, err := rig.agent.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestPiClearContextKeepsSteersWhenCancelled(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		if req.Type == CommandNewSession {
			return json.RawMessage(`{"cancelled":true}`), true, ""
		}
		return json.RawMessage(`{"disposition":"queued"}`), true, ""
	})
	require.NoError(t, rig.agent.SteerInput("guide", nil))
	_, err := rig.agent.ClearContext()
	require.ErrorIs(t, err, agent.ErrContextClearCancelled)
	assert.Empty(t, sink.SteerEchoReports())
	rig.agent.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","content":"guide"}}`))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestPiClearContextDropsSteersEvenWhenTheNewStateReadFails(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		switch req.Type {
		case CommandNewSession:
			return json.RawMessage(`{"cancelled":false}`), true, ""
		case CommandGetState:
			return nil, false, "state unavailable"
		default:
			return json.RawMessage(`{"disposition":"queued"}`), true, ""
		}
	})
	require.NoError(t, rig.agent.SteerInput("guide", nil))
	_, err := rig.agent.ClearContext()
	require.ErrorContains(t, err, "state unavailable")
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestPiDropsUserToolResultAndSystemMessageEnds(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	for _, role := range []string{"user", "toolResult", "system", "bashExecution"} {
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"` + role + `","content":[{"type":"text","text":"native internal content"}]}}`))
	}
	assert.Empty(t, sink.Messages())
}

func TestPiUnknownMessageRolePersists(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"message_end","message":{"role":"futureRole","content":"future content"}}`)
	a.HandleOutput(raw)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, raw, sink.Messages()[0].Content)
}

func TestPiSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := &Agent{
		Process:           providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"}),
		currentTurnActive: true,
		sink:              agent.NewProviderServices(sink),
	}
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, agent, agent.SendInput("later turn", nil))
}
