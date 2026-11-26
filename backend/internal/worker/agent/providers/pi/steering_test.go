package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	require.NoError(t, agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "Reply with the single word THREE."}))
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
		go func() { result <- agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"}) }()
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
	require.NoError(t, agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "/local"}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestPiSteerRefusalReturnsTheErrorAndReportsNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.currentTurnActive = true
	rig.agent.generationBuffer.Append("answer", agent.AssembledMessageKindText, "still active", providerkit.JoinVerbatim)
	rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) { return nil, false, "steer refused" })
	err := agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"})
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
		go func() { result <- agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"}) }()
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
	go func() { result <- agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"}) }()
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
	require.NoError(t, agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"}))
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
	require.NoError(t, agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"}))
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
	require.NoError(t, agenttest.SteerInput(rig.agent, agent.SteerRequest{Content: "guide"}))
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
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, agent, agenttest.SendInput(agent, "later turn", nil))
}

func TestPiPrepareIdentifiedSteerDispositionsAndErrors(t *testing.T) {
	t.Parallel()

	const prefix = "leapmux-steer:nonce:"
	newIdentifiedRig := func(t *testing.T, clock quartz.Clock) (*piTestRig, *agenttest.Sink) {
		t.Helper()
		sink := &agenttest.Sink{}
		rig := newPiTestRigWithClock(t, agent.NewProviderServices(sink), clock)
		rig.agent.sessionFile = "session-1"
		rig.agent.currentTurnActive = true
		rig.agent.steerBridgeReady = true
		rig.agent.steerBridge = &piSteerBridge{Prefix: prefix, IDLength: 48}
		return rig, sink
	}

	for _, disposition := range []string{"queued", "started"} {
		t.Run(disposition+" keeps identifiedSteers entry and prefixes stdin message", func(t *testing.T) {
			t.Parallel()
			rig, sink := newIdentifiedRig(t, nil)
			rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
				return json.RawMessage(`{"disposition":"` + disposition + `"}`), true, ""
			})
			_, err := rig.agent.SteerInput(agent.SteerRequest{Content: "guide text", InputID: "in-1", AttemptID: "att-1"})
			require.NoError(t, err)

			reqs := rig.requests()
			require.Len(t, reqs, 1)
			msg, _ := reqs[0].Payload["message"].(string)
			require.True(t, strings.HasPrefix(msg, prefix))
			nativeID, rest, found := strings.Cut(strings.TrimPrefix(msg, prefix), "\n")
			require.True(t, found)
			assert.Len(t, nativeID, 48)
			assert.Equal(t, "guide text", rest)

			rig.agent.Mu.Lock()
			entry, ok := rig.agent.identifiedSteers[nativeID]
			rig.agent.Mu.Unlock()
			require.True(t, ok)
			assert.Equal(t, agent.SteerResolution{
				InputID: "in-1", AttemptID: "att-1",
				Receipt: agent.NativeInputReceipt{SessionID: "session-1", InputID: nativeID},
			}, entry)
			assert.Empty(t, sink.SteerResolutions())
			assert.Empty(t, sink.NativeReplyTurns())
		})
	}

	t.Run("handled publishes echoed resolution and completed NativeReplyTurn", func(t *testing.T) {
		t.Parallel()
		rig, sink := newIdentifiedRig(t, nil)
		rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
			return json.RawMessage(`{"disposition":"handled"}`), true, ""
		})
		_, err := rig.agent.SteerInput(agent.SteerRequest{Content: "handled steer", InputID: "in-h", AttemptID: "att-h"})
		require.NoError(t, err)

		reqs := rig.requests()
		require.Len(t, reqs, 1)
		msg, _ := reqs[0].Payload["message"].(string)
		nativeID, _, found := strings.Cut(strings.TrimPrefix(msg, prefix), "\n")
		require.True(t, found)

		rig.agent.Mu.Lock()
		assert.Empty(t, rig.agent.identifiedSteers)
		rig.agent.Mu.Unlock()

		wantReceipt := agent.NativeInputReceipt{SessionID: "session-1", InputID: nativeID, TurnID: nativeID}
		assert.Equal(t, []agent.SteerResolution{{
			InputID: "in-h", AttemptID: "att-h", Outcome: agent.SteerOutcomeEchoed, Receipt: wantReceipt,
		}}, sink.SteerResolutions())
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "session-1", TurnID: nativeID,
			Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:  []agent.NativeReplyBoundary{{InputID: nativeID, AfterOrdinal: 1}},
		}}, sink.NativeReplyTurns())
	})

	t.Run("refusal removes identifiedSteers entry and is not ErrDeliveryUncertain", func(t *testing.T) {
		t.Parallel()
		rig, sink := newIdentifiedRig(t, nil)
		rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
			return nil, false, "cannot steer now"
		})
		_, err := rig.agent.SteerInput(agent.SteerRequest{Content: "refused", InputID: "in-r", AttemptID: "att-r"})
		require.ErrorContains(t, err, "cannot steer now")
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		rig.agent.Mu.Lock()
		assert.Empty(t, rig.agent.identifiedSteers)
		rig.agent.Mu.Unlock()
		assert.Empty(t, sink.SteerResolutions())
	})

	t.Run("timeout returns ErrDeliveryUncertain and keeps identifiedSteers entry", func(t *testing.T) {
		t.Parallel()
		clock := testutil.NewQuartzMock(t)
		trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandPrompt)
		defer trap.Close()
		rig, _ := newIdentifiedRig(t, clock)
		rig.holdResponse(CommandPrompt, make(chan struct{}))

		result := make(chan error, 1)
		go func() {
			_, err := rig.agent.SteerInput(agent.SteerRequest{Content: "timeout", InputID: "in-t", AttemptID: "att-t"})
			result <- err
		}()
		ctx := testutil.DeadlineContext(t)
		delay := testutil.WaitForTimer(t, ctx, trap)
		clock.Advance(delay).MustWait(ctx)
		select {
		case err := <-result:
			assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		rig.agent.Mu.Lock()
		assert.Len(t, rig.agent.identifiedSteers, 1)
		rig.agent.Mu.Unlock()
	})

	t.Run("transport exit returns ErrDeliveryUncertain and keeps identifiedSteers entry", func(t *testing.T) {
		t.Parallel()
		rig, _ := newIdentifiedRig(t, nil)
		rig.holdResponse(CommandPrompt, make(chan struct{}))
		result := make(chan error, 1)
		go func() {
			_, err := rig.agent.SteerInput(agent.SteerRequest{Content: "exit", InputID: "in-e", AttemptID: "att-e"})
			result <- err
		}()
		require.Eventually(t, func() bool { return len(rig.requests()) == 1 }, 30*time.Second, time.Millisecond)
		rig.agent.SimulateExitForTest()
		select {
		case err := <-result:
			assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("timed out waiting for steer exit error")
		}
		rig.agent.Mu.Lock()
		assert.Len(t, rig.agent.identifiedSteers, 1)
		rig.agent.Mu.Unlock()
	})

	t.Run("unknown disposition returns ErrDeliveryUncertain and keeps identifiedSteers entry", func(t *testing.T) {
		t.Parallel()
		for name, resp := range map[string]string{
			"unexpected": `{"disposition":"unexpected"}`,
			"empty":      `{}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				rig, _ := newIdentifiedRig(t, nil)
				rig.setResponder(func(piRecordedRequest) (json.RawMessage, bool, string) {
					return json.RawMessage(resp), true, ""
				})
				_, err := rig.agent.SteerInput(agent.SteerRequest{Content: "unknown", InputID: "in-u", AttemptID: "att-u"})
				assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
				rig.agent.Mu.Lock()
				assert.Len(t, rig.agent.identifiedSteers, 1)
				rig.agent.Mu.Unlock()
			})
		}
	})

	t.Run("immediate beginPiCommand write errors keep entry only on ErrDeliveryUncertain", func(t *testing.T) {
		t.Parallel()

		t.Run("partial write ErrDeliveryUncertain keeps entry", func(t *testing.T) {
			t.Parallel()
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
			a.sessionFile = "session-1"
			a.currentTurnActive = true
			a.steerBridgeReady = true
			a.steerBridge = &piSteerBridge{Prefix: prefix, IDLength: 48}
			setPiInterruptWriter(a, func([]byte) (int, error) { return 4, errors.New("broken pipe") })

			_, err := a.SteerInput(agent.SteerRequest{Content: "partial", InputID: "in-p", AttemptID: "att-p"})
			assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			a.Mu.Lock()
			assert.Len(t, a.identifiedSteers, 1)
			a.Mu.Unlock()
		})

		t.Run("non-uncertain write error removes entry", func(t *testing.T) {
			t.Parallel()
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
			a.sessionFile = "session-1"
			a.currentTurnActive = true
			a.steerBridgeReady = true
			a.steerBridge = &piSteerBridge{Prefix: prefix, IDLength: 48}
			writeErr := errors.New("pipe closed")
			setPiInterruptWriter(a, func([]byte) (int, error) { return 0, writeErr })

			_, err := a.SteerInput(agent.SteerRequest{Content: "closed", InputID: "in-c", AttemptID: "att-c"})
			require.ErrorIs(t, err, writeErr)
			assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
			a.Mu.Lock()
			assert.Empty(t, a.identifiedSteers)
			a.Mu.Unlock()
		})
	})
}

func TestPiPrepareLegacySteerWriteFailureForgetsTicket(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.currentTurnActive = true
	writeErr := errors.New("pipe closed")
	setPiInterruptWriter(a, func([]byte) (int, error) { return 0, writeErr })

	_, err := a.SteerInput(agent.SteerRequest{Content: "failed legacy steer"})
	require.ErrorIs(t, err, writeErr)

	// Because forgetPiSteer removed the ticket, a matching user echo must report nothing.
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","content":"failed legacy steer"}}`))
	assert.Empty(t, sink.SteerEchoReports())
}

func TestPiHandleMessageEndEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("non-string leapmuxInputId falls through to legacy steers.EchoKey", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.steers.Add("legacy text")
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","leapmuxInputId":123,"content":"legacy text"}}`))
		assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
		assert.Empty(t, sink.Messages())
	})

	t.Run("unregistered non-empty leapmuxInputId does not fall through to legacy steers", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.steers.Add("same text")
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","leapmuxInputId":"unregistered-id","content":"same text"}}`))
		assert.Empty(t, sink.SteerEchoReports())
		assert.Empty(t, sink.SteerResolutions())
		assert.Empty(t, sink.Messages())
	})

	t.Run("user message without leapmuxInputId closes completed identified steer segment", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.sessionFile = "session"
		first := agent.SteerResolution{
			InputID: "in-1", AttemptID: "att-1",
			Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-1"},
		}
		a.identifiedSteers = map[string]agent.SteerResolution{"native-1": first}
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput(piSteerEchoFrame(t, "native-1"))
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"First reply"}]}}`))
		a.HandleOutput([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`))
		require.Empty(t, sink.NativeReplyTurns())

		// A plain user message without leapmuxInputId still closes the completed segment.
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"user","content":"unidentified follow-up"}}`))
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "session", TurnID: "native-1",
			Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:  []agent.NativeReplyBoundary{{InputID: "native-1", AfterOrdinal: 1}},
			Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:native-1:2"}},
		}}, sink.NativeReplyTurns())
		a.Mu.Lock()
		assert.Nil(t, a.replyRun)
		a.Mu.Unlock()
	})

	t.Run("non-string role and non-object message persist via default", func(t *testing.T) {
		t.Parallel()
		for name, raw := range map[string]string{
			"numeric role":   `{"type":"message_end","message":{"role":42,"content":"x"}}`,
			"string message": `{"type":"message_end","message":"not-an-object"}`,
			"null message":   `{"type":"message_end","message":null}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a := newPiAgentWithSink(agent.NewProviderServices(sink))
				a.HandleOutput([]byte(raw))
				require.Len(t, sink.Messages(), 1)
				assert.Equal(t, []byte(raw), sink.Messages()[0].Content)
			})
		}
	})
}

func TestPiUserMessageTextFromContentEmptyAndMalformed(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]json.RawMessage{
		"nil":            nil,
		"empty bytes":    json.RawMessage(""),
		"invalid json":   json.RawMessage("{bad"),
		"null":           json.RawMessage("null"),
		"number":         json.RawMessage("99"),
		"object":         json.RawMessage("{}"),
		"null block":     json.RawMessage("[null]"),
		"missing type":   json.RawMessage(`[{}]`),
		"non-string txt": json.RawMessage(`[{"type":"text","text":123}]`),
	} {
		text, ok := piUserMessageTextFromContent(content)
		assert.False(t, ok, name)
		assert.Empty(t, text, name)
	}
}

func TestPiObserveSteerEchoTransitions(t *testing.T) {
	t.Parallel()

	for _, stopReason := range []string{"stop", "length"} {
		t.Run("closes previous segment on "+stopReason+" before new piReplyRun", func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.sessionFile = "session"
			a.identifiedSteers = map[string]agent.SteerResolution{
				"native-1": {InputID: "in-1", AttemptID: "att-1", Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-1"}},
				"native-2": {InputID: "in-2", AttemptID: "att-2", Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-2"}},
			}
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			assert.True(t, a.observePiSteerEcho("native-1"))
			a.HandleOutput([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"` + stopReason + `"}}`))

			assert.True(t, a.observePiSteerEcho("native-2"))
			assert.Equal(t, []agent.NativeReplyTurn{{
				SessionID: "session", TurnID: "native-1",
				Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs:  []agent.NativeReplyBoundary{{InputID: "native-1", AfterOrdinal: 1}},
			}}, sink.NativeReplyTurns())
			a.Mu.Lock()
			require.NotNil(t, a.replyRun)
			assert.Equal(t, "native-2", a.replyRun.receipt.TurnID)
			a.Mu.Unlock()
			assert.Equal(t, []agent.SteerResolution{
				{InputID: "in-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed, Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-1", TurnID: "native-1"}},
				{InputID: "in-2", AttemptID: "att-2", Outcome: agent.SteerOutcomeEchoed, Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: "native-2", TurnID: "native-2"}},
			}, sink.SteerResolutions())
		})
	}

	t.Run("session mismatch on replyRun retires UNPROVED and on pending steer produces no resolution", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.sessionFile = "session-new"
		oldReceipt := agent.NativeInputReceipt{SessionID: "session-old", InputID: "old-in", TurnID: "old-in"}
		a.replyRun = newPiReplyRun(oldReceipt, true)
		a.identifiedSteers = map[string]agent.SteerResolution{
			"native-stale": {InputID: "in-stale", AttemptID: "att-stale", Receipt: agent.NativeInputReceipt{SessionID: "session-old", InputID: "native-stale"}},
		}

		assert.True(t, a.observePiSteerEcho("native-stale"))
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			oldReceipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, sink.NativeReplyRetirements())
		assert.Empty(t, sink.SteerResolutions(), "mismatched session steer must not resolve")
		a.Mu.Lock()
		assert.Nil(t, a.replyRun)
		assert.Contains(t, a.identifiedSteers, "native-stale")
		a.Mu.Unlock()
	})

	t.Run("unknown non-empty inputID returns true without resolution and empty returns false", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.sessionFile = "session"
		assert.True(t, a.observePiSteerEcho("unknown-id"))
		assert.False(t, a.observePiSteerEcho(""))
		assert.Empty(t, sink.SteerResolutions())
		assert.Empty(t, sink.NativeReplyTurns())
	})
}

func TestPiUpdateAssistantBoundary(t *testing.T) {
	t.Parallel()

	t.Run("finished=false clears reply.stop and non-assistant/malformed are no-ops", func(t *testing.T) {
		t.Parallel()
		a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
		a.sessionFile = "session"
		a.replyRun = newPiReplyRun(agent.NativeInputReceipt{SessionID: "session", InputID: "in-1", TurnID: "in-1"}, true)

		a.updatePiAssistantBoundary([]byte(`{"message":{"role":"assistant","stopReason":"stop"}}`), true)
		assert.Equal(t, "stop", a.replyRun.stop)

		// Non-assistant role and malformed JSON must not clear reply.stop.
		a.updatePiAssistantBoundary([]byte(`{"message":{"role":"user","stopReason":"other"}}`), false)
		a.updatePiAssistantBoundary([]byte(`{bad json`), false)
		assert.Equal(t, "stop", a.replyRun.stop)

		// Assistant message_start (finished=false) clears reply.stop.
		a.updatePiAssistantBoundary([]byte(`{"message":{"role":"assistant"}}`), false)
		assert.Empty(t, a.replyRun.stop)
	})

	t.Run("no active replyRun is a no-op", func(t *testing.T) {
		t.Parallel()
		a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
		a.sessionFile = "session"
		a.updatePiAssistantBoundary([]byte(`{"message":{"role":"assistant","stopReason":"stop"}}`), true)

		a.replyRun = newPiReplyRun(agent.NativeInputReceipt{SessionID: "session", InputID: "in-1", TurnID: "in-1"}, false)
		a.updatePiAssistantBoundary([]byte(`{"message":{"role":"assistant","stopReason":"stop"}}`), true)
		assert.Empty(t, a.replyRun.stop)
	})
}

func TestPiResolveAndDropIdentifiedSteersOrderAndIdempotency(t *testing.T) {
	t.Parallel()

	t.Run("sorted order by native inputID and idempotent second call", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newPiAgentWithSink(agent.NewProviderServices(sink))
		a.identifiedSteers = map[string]agent.SteerResolution{
			"native-c": {InputID: "in-c", AttemptID: "att-c", Receipt: agent.NativeInputReceipt{SessionID: "s", InputID: "native-c"}},
			"native-a": {InputID: "in-a", AttemptID: "att-a", Receipt: agent.NativeInputReceipt{SessionID: "s", InputID: "native-a"}},
			"native-b": {InputID: "in-b", AttemptID: "att-b", Receipt: agent.NativeInputReceipt{SessionID: "s", InputID: "native-b"}},
		}
		a.resolveIdentifiedPiSteers(agent.SteerOutcomeUncertain)
		a.resolveIdentifiedPiSteers(agent.SteerOutcomeDropped)

		assert.Equal(t, []agent.SteerResolution{
			{InputID: "in-a", AttemptID: "att-a", Outcome: agent.SteerOutcomeUncertain},
			{InputID: "in-b", AttemptID: "att-b", Outcome: agent.SteerOutcomeUncertain},
			{InputID: "in-c", AttemptID: "att-c", Outcome: agent.SteerOutcomeUncertain},
		}, sink.SteerResolutions())
	})

	t.Run("ClearContext drops pending identifiedSteers with empty Receipt", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		rig := newPiTestRig(t, agent.NewProviderServices(sink))
		rig.agent.identifiedSteers = map[string]agent.SteerResolution{
			"native-2": {InputID: "in-2", AttemptID: "att-2", Receipt: agent.NativeInputReceipt{SessionID: "s", InputID: "native-2"}},
			"native-1": {InputID: "in-1", AttemptID: "att-1", Receipt: agent.NativeInputReceipt{SessionID: "s", InputID: "native-1"}},
		}
		rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
			switch req.Type {
			case CommandNewSession:
				return json.RawMessage(`{"cancelled":false}`), true, ""
			case CommandGetState:
				return json.RawMessage(`{"sessionId":"fresh","sessionFile":"/tmp/pi-fresh.jsonl"}`), true, ""
			}
			return nil, true, ""
		})
		_, err := rig.agent.ClearContext()
		require.NoError(t, err)
		assert.Equal(t, []agent.SteerResolution{
			{InputID: "in-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeDropped},
			{InputID: "in-2", AttemptID: "att-2", Outcome: agent.SteerOutcomeDropped},
		}, sink.SteerResolutions())
	})
}

func TestPiSteerMessageInputID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", piSteerMessageInputID(map[string]json.RawMessage{}))
	assert.Equal(t, "", piSteerMessageInputID(map[string]json.RawMessage{piSteerMessageIDField: json.RawMessage(`123`)}))
	assert.Equal(t, "", piSteerMessageInputID(map[string]json.RawMessage{piSteerMessageIDField: json.RawMessage(`true`)}))
	assert.Equal(t, "", piSteerMessageInputID(map[string]json.RawMessage{piSteerMessageIDField: json.RawMessage(`null`)}))
	assert.Equal(t, "", piSteerMessageInputID(map[string]json.RawMessage{piSteerMessageIDField: json.RawMessage(`""`)}))
	assert.Equal(t, "abc123", piSteerMessageInputID(map[string]json.RawMessage{piSteerMessageIDField: json.RawMessage(`"abc123"`)}))
}
