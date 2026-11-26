package qoder

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func commandFrame(sessionID, commandID, state string) []byte {
	return fmt.Appendf(nil, `{"type":"command_lifecycle","uuid":"event-id","command_uuid":%q,"session_id":%q,"state":%q}`, commandID, sessionID, state)
}

func userEchoFrame(receipt agent.NativeInputReceipt) []byte {
	return fmt.Appendf(nil, `{"type":"user","uuid":%q,"session_id":%q,"parent_tool_use_id":null,"isReplay":true,"message":{"role":"user","content":"Check the release."}}`, receipt.InputID, receipt.SessionID)
}

func TestQoderExactReplyWaitsForItsCommandCompletion(t *testing.T) {
	t.Parallel()
	a, _, stdin := newGoalAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	_, err = uuid.Parse(receipt.InputID)
	require.NoError(t, err)
	assert.Equal(t, receipt.InputID, receipt.TurnID)
	var sent struct {
		UUID      string `json:"uuid"`
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdin.String()), &sent))
	assert.Equal(t, receipt.InputID, sent.UUID)
	assert.Equal(t, receipt.SessionID, sent.SessionID)

	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, "queued"))
	a.HandleOutput(userEchoFrame(receipt))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"text","text":"An earlier reply."}]}}`))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].NativeTurnID)

	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(receipt))
	a.HandleOutput(qoderToolUseFrame("owned"))
	a.HandleOutput([]byte(`{"type":"control_request","session_id":"other-session","request_id":"foreign","request":{"tool_use_id":"owned"}}`))
	a.HandleOutput([]byte(`{"type":"control_request","session_id":"session-1","request_id":"root","request":{"tool_use_id":"owned"}}`))
	controls := sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.Empty(t, controls[0].NativeTurnID)
	assert.Equal(t, receipt.TurnID, controls[1].NativeTurnID)

	a.HandleOutput(commandFrame(receipt.SessionID, "queued-command", "queued"))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"text","text":"The release passed."}]}}`))
	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput(commandFrame(receipt.SessionID, "queued-command", commandCompleted))
	a.HandleOutput(commandFrame("other-session", receipt.InputID, commandCompleted))
	assert.True(t, a.PublishTurnActive().Active)
	assert.Empty(t, turnEndEntries(&sink.Sink))

	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))
	assert.False(t, a.PublishTurnActive().Active)
	rows = sink.Messages()
	require.Len(t, rows, 4)
	assert.Equal(t, receipt.TurnID, rows[2].NativeTurnID)
	assert.Equal(t, receipt.TurnID, rows[3].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[3].InputTurnOutcome)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestQoderExactReplyRequiresNativeOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		echo       bool
		invalidate bool
		result     string
		stop       bool
		finish     string
		outcome    leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "missing echo", result: qoderSuccessResult, finish: commandCompleted,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "competing command", echo: true, invalidate: true, result: qoderSuccessResult, finish: commandCompleted,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "native failure", echo: true, result: qoderAbortedResult, finish: commandCancelled,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "native interruption", echo: true, result: qoderAbortedResult, stop: true, finish: commandCancelled,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
		},
		{
			name: "process ended before the command completion", echo: true, result: qoderSuccessResult,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGoalAgent(t)
			receipt, err := a.SendInput("Check the release.", nil)
			require.NoError(t, err)
			a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
			if tc.echo {
				a.HandleOutput(userEchoFrame(receipt))
			}
			if tc.invalidate {
				a.HandleOutput(commandFrame(receipt.SessionID, "other-command", commandStarted))
			}
			if tc.stop {
				a.noteInterruptRequested()
			}
			a.HandleOutput([]byte(tc.result))
			if tc.finish == "" {
				a.setTurnActive(false)
			} else {
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, tc.finish))
			}
			end := onlyTurnEnd(t, sink)
			assert.False(t, a.PublishTurnActive().Active)
			if tc.outcome == leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
				assert.Equal(t, tc.outcome, sink.NativeReplyRetirements()[receipt])
				assert.Empty(t, end.NativeTurnID)
			} else {
				assert.Equal(t, tc.outcome, end.InputTurnOutcome)
				assert.Equal(t, receipt.TurnID, end.NativeTurnID)
				assert.Empty(t, sink.NativeReplyRetirements())
			}
		})
	}
}

func TestQoderQueuedSteersKeepTheirOwnReplies(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newGoalAgent(t)
	root, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(commandFrame(root.SessionID, root.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(root))
	first := agent.SteerRequest{InputID: "first", AttemptID: "first-attempt", Content: "Check the release."}
	second := agent.SteerRequest{InputID: "second", AttemptID: "second-attempt", Content: "Check the release."}
	require.NoError(t, agenttest.SteerInput(a, first))
	require.NoError(t, agenttest.SteerInput(a, second))
	lines := strings.Split(strings.TrimSpace(stdin.String()), "\n")
	require.Len(t, lines, 3)
	firstReceipt := steerReceiptFromFrame(t, lines[1], root.SessionID)
	secondReceipt := steerReceiptFromFrame(t, lines[2], root.SessionID)
	assert.NotEqual(t, firstReceipt.InputID, secondReceipt.InputID)
	a.HandleOutput(commandFrame(root.SessionID, firstReceipt.InputID, "queued"))
	a.HandleOutput(commandFrame(root.SessionID, secondReceipt.InputID, "queued"))
	assert.Empty(t, sink.SteerResolutions())

	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput(commandFrame(root.SessionID, root.InputID, commandCompleted))
	assert.Empty(t, sink.SteerResolutions(), "the prior turn does not resolve either steer")
	for _, input := range []struct {
		request agent.SteerRequest
		receipt agent.NativeInputReceipt
	}{
		{first, firstReceipt},
		{second, secondReceipt},
	} {
		a.HandleOutput(commandFrame(root.SessionID, input.receipt.InputID, commandStarted))
		assert.True(t, a.PublishTurnActive().Active)
		a.HandleOutput(userEchoFrame(input.receipt))
		a.HandleOutput(userEchoFrame(input.receipt))
		a.HandleOutput([]byte(qoderSuccessResult))
		a.HandleOutput(commandFrame(root.SessionID, input.receipt.InputID, commandCompleted))
	}
	assert.Equal(t, []agent.SteerResolution{
		{InputID: first.InputID, AttemptID: first.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: firstReceipt},
		{InputID: second.InputID, AttemptID: second.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: secondReceipt},
	}, sink.SteerResolutions())
	ends := turnEndEntries(sink)
	require.Len(t, ends, 3)
	for i, receipt := range []agent.NativeInputReceipt{root, firstReceipt, secondReceipt} {
		assert.Equal(t, receipt.TurnID, ends[i].NativeTurnID)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, ends[i].InputTurnOutcome)
	}
}

func TestQoderUnreadSteerUsesItsNativeDisposition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		state   string
		started bool
		outcome agent.SteerOutcome
	}{
		{name: "discarded in the native queue", state: commandDiscarded, outcome: agent.SteerOutcomeDropped},
		{name: "cancelled in the native queue", state: commandCancelled, outcome: agent.SteerOutcomeDropped},
		{name: "process exit before start", outcome: agent.SteerOutcomeUncertain},
		{name: "cancelled after start without echo", state: commandCancelled, started: true, outcome: agent.SteerOutcomeUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newGoalAgent(t)
			a.setTurnActive(true)
			request := agent.SteerRequest{InputID: "input", AttemptID: "attempt", Content: "Update the release check."}
			require.NoError(t, agenttest.SteerInput(a, request))
			receipt := steerReceiptFromFrame(t, stdin.String(), a.nativeSessionID())
			if tc.started {
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
			}
			if tc.state == "" {
				a.retireQueuedSteers()
			} else {
				frame := commandFrame(receipt.SessionID, receipt.InputID, tc.state)
				a.HandleOutput(frame)
				a.HandleOutput(frame)
			}
			assert.Equal(t, []agent.SteerResolution{{
				InputID: request.InputID, AttemptID: request.AttemptID, Outcome: tc.outcome,
			}}, sink.SteerResolutions())
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

func steerReceiptFromFrame(t *testing.T, raw, sessionID string) agent.NativeInputReceipt {
	t.Helper()
	var frame UserInputMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &frame))
	_, err := uuid.Parse(frame.UUID)
	require.NoError(t, err)
	return agent.NativeInputReceipt{SessionID: sessionID, InputID: frame.UUID, TurnID: frame.UUID}
}

type qoderOrderRecordingSink struct {
	agent.ProviderServices
	mu     sync.Mutex
	events []string
}

func (s *qoderOrderRecordingSink) record(event string) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *qoderOrderRecordingSink) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *qoderOrderRecordingSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.record("turn_end")
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *qoderOrderRecordingSink) CloseSpan(spanID string) {
	s.record("close_span:" + spanID)
	s.ProviderServices.CloseSpan(spanID)
}

func (s *qoderOrderRecordingSink) ResetSpans() {
	s.record("reset_spans")
	s.ProviderServices.ResetSpans()
}

func (s *qoderOrderRecordingSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.record("retire_reply")
	return s.ProviderServices.RetireNativeInputTurn(receipt, outcome)
}

func (s *qoderOrderRecordingSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	fn := s.ProviderServices.CaptureSteerResolution(resolution)
	return func() error {
		s.record("steer:" + resolution.InputID)
		return fn()
	}
}

type partialFailStdin struct {
	written int
	err     error
}

func (w partialFailStdin) Write([]byte) (int, error) { return w.written, w.err }
func (partialFailStdin) Close() error                { return nil }

func TestQoderFinishStreamFlushesHeldResultBeforeTeardownWithoutUsageRefresh(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	recorder := &qoderOrderRecordingSink{ProviderServices: a.sink}
	a.sink = recorder
	a.usageRefresh = make(chan struct{}, 1)

	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(receipt))
	a.HandleOutput(qoderToolUseFrame("open-tool"))
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID: "queued-steer", AttemptID: "attempt-1", Content: "Follow up.",
	}))
	a.HandleOutput([]byte(qoderSuccessResult))

	// Process exits before command_lifecycle arrives.
	a.finishStream()

	events := recorder.recorded()
	turnEndIdx := -1
	closeSpanIdx := -1
	resetSpansIdx := -1
	steerIdx := -1
	for i, event := range events {
		switch event {
		case "turn_end":
			turnEndIdx = i
		case "close_span:open-tool":
			closeSpanIdx = i
		case "reset_spans":
			resetSpansIdx = i
		case "steer:queued-steer":
			steerIdx = i
		}
	}
	require.NotEqual(t, -1, turnEndIdx, "held result must be persisted on exit: %v", events)
	require.NotEqual(t, -1, closeSpanIdx, "open tool must close on exit: %v", events)
	require.NotEqual(t, -1, resetSpansIdx, "spans must reset on exit: %v", events)
	require.NotEqual(t, -1, steerIdx, "queued steer must retire on exit: %v", events)
	assert.Less(t, turnEndIdx, closeSpanIdx, "held result must persist before closing open tools: %v", events)
	assert.Less(t, turnEndIdx, resetSpansIdx, "held result must persist before resetting spans: %v", events)
	assert.Less(t, turnEndIdx, steerIdx, "held result must persist before retiring queued steers: %v", events)

	select {
	case <-a.usageRefresh:
		t.Fatal("finishStream must not request a post-exit usage refresh")
	default:
	}

	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "queued-steer", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions())
	assert.False(t, a.PublishTurnActive().Active)
}

func TestQoderFinishStreamWithNilSinkDoesNotPanic(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(receipt))
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{
		InputID: "queued-steer", AttemptID: "attempt-1", Content: "Follow up.",
	}))
	a.HandleOutput([]byte(qoderSuccessResult))

	a.sink = nil
	assert.NotPanics(t, func() {
		a.finishStream()
	})
}

func TestQoderForeignCommandStartedWithHeldResultKeepsTurnActive(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	receipt, err := a.SendInput("Check the release.", nil)
	require.NoError(t, err)
	a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
	a.HandleOutput(userEchoFrame(receipt))
	a.HandleOutput([]byte(qoderSuccessResult))

	// A foreign command starts while the previous command's result is still held.
	// A stale stop request after the previous result must not leak into the foreign turn.
	a.noteInterruptRequested()
	a.HandleOutput(commandFrame(receipt.SessionID, "foreign-command", commandStarted))

	end := onlyTurnEnd(t, sink)
	assert.Empty(t, end.NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	assert.True(t, a.PublishTurnActive().Active, "a foreign command starting must keep Qoder active")
	a.mu.Lock()
	assert.Nil(t, a.inputReply, "the finished previous reply must be cleared")
	assert.False(t, a.interruptRequested, "a stale interrupt flag must not leak to the foreign command")
	a.mu.Unlock()
}

func TestQoderSteerInputFailedSendRemovesDefiniteAndKeepsUncertain(t *testing.T) {
	t.Parallel()

	t.Run("definite write error deletes queuedSteers and does not report uncertain", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.setTurnActive(true)
		a.SetStdinForTest(agenttest.FailingStdin{})

		_, err := a.SteerInput(agent.SteerRequest{
			InputID: "definite-steer", AttemptID: "attempt-1", Content: "Update.",
		})
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

		a.retireQueuedSteers()
		assert.Empty(t, sink.SteerResolutions(), "a steer that definitely failed to send must not retire as uncertain")
	})

	t.Run("partial write error reports uncertain and retains queuedSteers for retirement", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.setTurnActive(true)
		a.SetStdinForTest(partialFailStdin{written: 3, err: errors.New("broken pipe")})

		_, err := a.SteerInput(agent.SteerRequest{
			InputID: "uncertain-steer", AttemptID: "attempt-1", Content: "Update.",
		})
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)

		a.retireQueuedSteers()
		assert.Equal(t, []agent.SteerResolution{{
			InputID: "uncertain-steer", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeUncertain,
		}}, sink.SteerResolutions())
	})
}

func TestQoderSendInputFailedSendDoesNotRetireUnreturnedReceipt(t *testing.T) {
	t.Parallel()

	t.Run("definite write error clears inputReply without retiring unreturned receipt", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.SetStdinForTest(agenttest.FailingStdin{})

		receipt, err := a.SendInput("Check the release.", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.Empty(t, sink.NativeReplyRetirements(), "an unreturned receipt must never be retired")
		assert.False(t, a.PublishTurnActive().Active)
	})

	t.Run("partial write error reports uncertain without retiring unreturned receipt", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.SetStdinForTest(partialFailStdin{written: 3, err: errors.New("broken pipe")})

		receipt, err := a.SendInput("Check the release.", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.Empty(t, sink.NativeReplyRetirements(), "an unreturned receipt must never be retired")
		assert.False(t, a.PublishTurnActive().Active)

		a.finishStream()
		assert.Empty(t, sink.NativeReplyRetirements(), "exit teardown must not retire an unreturned receipt either")
	})
}

func TestQoderRetireQueuedSteersUsesDeterministicSortedOrder(t *testing.T) {
	t.Parallel()
	want := []string{"in-a", "in-b", "in-c", "in-d", "in-e"}
	for range 25 {
		a, sink, _ := newGoalAgent(t)
		a.queuedSteers = map[string]*inputReply{
			"cmd-5": {steer: &agent.SteerResolution{InputID: "in-e", AttemptID: "att-e"}},
			"cmd-2": {steer: &agent.SteerResolution{InputID: "in-b", AttemptID: "att-b"}},
			"cmd-4": {steer: &agent.SteerResolution{InputID: "in-d", AttemptID: "att-d"}},
			"cmd-1": {steer: &agent.SteerResolution{InputID: "in-a", AttemptID: "att-a"}},
			"cmd-3": {steer: &agent.SteerResolution{InputID: "in-c", AttemptID: "att-c"}},
		}
		a.retireQueuedSteers()
		resolutions := sink.SteerResolutions()
		require.Len(t, resolutions, len(want))
		got := make([]string, len(resolutions))
		for i, res := range resolutions {
			got[i] = res.InputID
		}
		require.Equal(t, want, got)
	}
}

func TestQoderSendAndSteerEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("SendInput without sessionID resets active on definite failure without panicking", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.mu.Lock()
		a.sessionID = ""
		a.mu.Unlock()
		a.SetStdinForTest(agenttest.FailingStdin{})

		receipt, err := a.SendInput("Hello.", nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)
		assert.False(t, a.PublishTurnActive().Active)
		assert.Empty(t, sink.NativeReplyRetirements())
	})

	t.Run("SteerInput without AttemptID preserves definite vs uncertain write error", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newGoalAgent(t)
		a.setTurnActive(true)
		a.SetStdinForTest(agenttest.FailingStdin{})
		_, err := a.SteerInput(agent.SteerRequest{Content: "Definite."})
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

		a.SetStdinForTest(partialFailStdin{written: 2, err: errors.New("broken pipe")})
		_, err = a.SteerInput(agent.SteerRequest{Content: "Uncertain."})
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	})

	t.Run("aborted text assistant preserves interrupted completion and NativeTurnID", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		receipt, err := a.SendInput("Check.", nil)
		require.NoError(t, err)
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
		a.HandleOutput(userEchoFrame(receipt))
		a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","aborted":true,"message":{"role":"assistant","content":[{"type":"text","text":"Partial"}]}}`))

		msgs := sink.Messages()
		require.Len(t, msgs, 1)
		assert.Equal(t, agent.MessageCompletionInterrupted, msgs[0].Completion)
		assert.Equal(t, receipt.TurnID, msgs[0].NativeTurnID)
	})

	t.Run("assistant frame with multiple tool_use blocks scopes root controls and counts all uses", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newGoalAgent(t)
		sink := &agenttest.ControlSink{}
		a.sink = agent.NewProviderServices(sink)
		receipt, err := a.SendInput("Run two tools.", nil)
		require.NoError(t, err)
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
		a.HandleOutput(userEchoFrame(receipt))
		a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"Bash","input":{"command":"echo a"}},{"type":"tool_use","id":"call-b","name":"Read","input":{"file_path":"a.txt"}}]}}`))

		a.HandleOutput([]byte(`{"type":"control_request","session_id":"session-1","request_id":"req-a","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"call-a","input":{"command":"echo a"}}}`))
		a.HandleOutput([]byte(`{"type":"control_request","session_id":"session-1","request_id":"req-b","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"call-b","input":{"file_path":"a.txt"}}}`))
		a.HandleOutput([]byte(`{"type":"control_request","session_id":"session-1","request_id":"req-child","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"child-call","agent_id":"child-native","input":{"command":"echo child"}}}`))
		a.HandleOutput([]byte(`{"type":"control_request","session_id":"session-1","request_id":"req-bad-agent","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"call-a","agent_id":42}}`))

		controls := sink.PublishedControls()
		require.Len(t, controls, 4)
		assert.Equal(t, receipt.SessionID, controls[0].AgentSessionID)
		assert.Equal(t, receipt.TurnID, controls[0].NativeTurnID)
		assert.Equal(t, receipt.SessionID, controls[1].AgentSessionID)
		assert.Equal(t, receipt.TurnID, controls[1].NativeTurnID)
		assert.Empty(t, controls[2].AgentSessionID, "subagent control with agent_id must not scope to root turn")
		assert.Empty(t, controls[2].NativeTurnID, "subagent control with agent_id must not scope to root turn")
		assert.Empty(t, controls[3].AgentSessionID, "non-string agent_id must not scope control")
		assert.Empty(t, controls[3].NativeTurnID, "non-string agent_id must not scope control")

		// Tool-result user frames without isReplay do not invalidate ownership.
		a.HandleOutput(qoderToolResultFrame("call-a"))
		a.HandleOutput(qoderToolResultFrame("call-b"))
		a.HandleOutput([]byte(qoderSuccessResult))
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))

		end := onlyTurnEnd(t, &sink.Sink)
		assert.Equal(t, receipt.TurnID, end.NativeTurnID)
		count, ok := resolvedTurnEndToolUses(end)
		assert.True(t, ok)
		assert.Equal(t, int32(2), count)
	})

	t.Run("observeInputEcho marks synthetic or mismatched replay incomplete during started turn", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			frame string
		}{
			{
				name:  "synthetic replay",
				frame: `{"type":"user","uuid":"%s","session_id":"session-1","isReplay":true,"isSynthetic":true,"message":{"role":"user","content":"Synthetic."}}`,
			},
			{
				name:  "mismatched uuid replay",
				frame: `{"type":"user","uuid":"other-uuid","session_id":"session-1","isReplay":true,"message":{"role":"user","content":"Other."}}`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				a, sink, _ := newGoalAgent(t)
				receipt, err := a.SendInput("Check.", nil)
				require.NoError(t, err)
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
				a.HandleOutput(userEchoFrame(receipt))
				raw := tc.frame
				if strings.Contains(raw, "%s") {
					raw = fmt.Sprintf(raw, receipt.InputID)
				}
				a.HandleOutput([]byte(raw))
				a.HandleOutput([]byte(qoderSuccessResult))
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))

				end := onlyTurnEnd(t, sink)
				assert.Empty(t, end.NativeTurnID)
				assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
			})
		}
	})

	t.Run("assistant frame with malformed aborted field still captures session_id", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.mu.Lock()
		a.sessionID = ""
		a.mu.Unlock()
		a.HandleOutput([]byte(`{"type":"assistant","session_id":"recovered-session","aborted":"not-a-bool","message":{"role":"assistant","content":"plain"}}`))

		assert.Equal(t, "recovered-session", a.nativeSessionID())
		msgs := sink.Messages()
		require.Len(t, msgs, 1)
		assert.Equal(t, "recovered-session", msgs[0].AgentSessionID)
		assert.Empty(t, msgs[0].Completion)
	})
}

func TestQoderControlRequestScoping(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		afterResult bool
		frame       string
		wantScoped  bool
	}{
		{
			name:       "root MCP elicitation without tool_use_id or agent_id",
			frame:      `{"type":"control_request","session_id":"session-1","request_id":"elicit-1","request":{"subtype":"elicitation","mcp_server_name":"forms","message":"Enter your token."}}`,
			wantScoped: true,
		},
		{
			name:       "root MCP elicitation without top-level session_id",
			frame:      `{"type":"control_request","request_id":"elicit-2","request":{"subtype":"elicitation","mcp_server_name":"forms","message":"Enter your token."}}`,
			wantScoped: true,
		},
		{
			name:       "pre-assistant root can_use_tool before any assistant frame",
			frame:      `{"type":"control_request","session_id":"session-1","request_id":"perm-early","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"early-call","input":{"command":"ls"}}}`,
			wantScoped: true,
		},
		{
			name:       "root control_request with empty agent_id",
			frame:      `{"type":"control_request","session_id":"session-1","request_id":"perm-empty-agent","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"early-call","agent_id":"","input":{"command":"ls"}}}`,
			wantScoped: true,
		},
		{
			name:       "root control_request with null agent_id",
			frame:      `{"type":"control_request","session_id":"session-1","request_id":"perm-null-agent","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"early-call","agent_id":null,"input":{"command":"ls"}}}`,
			wantScoped: true,
		},
		{
			name:       "subagent can_use_tool with request.agent_id without parent_tool_use_id",
			frame:      `{"type":"control_request","session_id":"session-1","request_id":"child-perm","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"child-call","agent_id":"child-native","input":{"command":"ls"}}}`,
			wantScoped: false,
		},
		{
			name:       "foreign session_id control_request",
			frame:      `{"type":"control_request","session_id":"other-session","request_id":"foreign-perm","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"call-1","input":{"command":"ls"}}}`,
			wantScoped: false,
		},
		{
			name:       "null request object",
			frame:      `{"type":"control_request","session_id":"session-1","request_id":"null-req","request":null}`,
			wantScoped: false,
		},
		{
			name:        "control_request arriving after result before command completion",
			afterResult: true,
			frame:       `{"type":"control_request","session_id":"session-1","request_id":"late-perm","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"late-call","input":{"command":"ls"}}}`,
			wantScoped:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newGoalAgent(t)
			sink := &agenttest.ControlSink{}
			a.sink = agent.NewProviderServices(sink)
			receipt, err := a.SendInput("Check the release.", nil)
			require.NoError(t, err)
			a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
			a.HandleOutput(userEchoFrame(receipt))
			if tc.afterResult {
				a.HandleOutput([]byte(qoderSuccessResult))
			}

			// Send control_request with no preceding assistant frame.
			a.HandleOutput([]byte(tc.frame))

			controls := sink.PublishedControls()
			require.Len(t, controls, 1)
			if tc.wantScoped {
				assert.Equal(t, receipt.SessionID, controls[0].AgentSessionID)
				assert.Equal(t, receipt.TurnID, controls[0].NativeTurnID)
			} else {
				assert.Empty(t, controls[0].AgentSessionID)
				assert.Empty(t, controls[0].NativeTurnID)
			}
		})
	}
}

type failEchoedSteerSink struct {
	agent.ProviderServices
	err error
}

func (*failEchoedSteerSink) OutputQueue() *agent.OutputQueue { return nil }

func (s *failEchoedSteerSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	if resolution.Outcome == agent.SteerOutcomeEchoed {
		return func() error { return s.err }
	}
	return s.ProviderServices.CaptureSteerResolution(resolution)
}

type callbackFailStdin struct {
	onWrite func(data []byte)
	written int
	err     error
}

func (w callbackFailStdin) Write(data []byte) (int, error) {
	if w.onWrite != nil {
		w.onWrite(data)
	}
	return w.written, w.err
}

func (callbackFailStdin) Close() error { return nil }

func TestQoderAbandonInputReplyEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("SendInput write failure after held result flushes result without retiring unreturned receipt", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.SetStdinForTest(callbackFailStdin{
			written: 3,
			err:     errors.New("broken pipe"),
			onWrite: func(data []byte) {
				var sent UserInputMessage
				require.NoError(t, json.Unmarshal(data, &sent))
				receipt := agent.NativeInputReceipt{
					SessionID: a.nativeSessionID(),
					InputID:   sent.UUID,
					TurnID:    sent.UUID,
				}
				a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
				a.HandleOutput(userEchoFrame(receipt))
				a.HandleOutput([]byte(qoderSuccessResult))
				a.noteInterruptRequested()
			},
		})

		receipt, err := a.SendInput("Check the release.", nil)
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
		assert.Equal(t, agent.NativeInputReceipt{}, receipt)

		end := onlyTurnEnd(t, sink)
		assert.Empty(t, end.NativeTurnID, "flushed result from an abandoned input must stay unproved")
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED, end.InputTurnOutcome)
		assert.Empty(t, sink.NativeReplyRetirements(), "an unreturned receipt must never be retired")
		assert.False(t, a.PublishTurnActive().Active)
		a.mu.Lock()
		assert.Nil(t, a.inputReply)
		assert.False(t, a.interruptRequested)
		a.mu.Unlock()
	})

	t.Run("abandonInputReply ignores stale or nil reply when a newer reply owns the turn", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		current := &inputReply{receipt: agent.NativeInputReceipt{SessionID: "session-1", InputID: "new-cmd", TurnID: "new-cmd"}}
		stale := &inputReply{
			receipt: agent.NativeInputReceipt{SessionID: "session-1", InputID: "old-cmd", TurnID: "old-cmd"},
			result:  &inputResult{raw: []byte(qoderSuccessResult)},
		}
		a.mu.Lock()
		a.active = true
		a.inputReply = current
		a.interruptRequested = true
		a.mu.Unlock()

		a.abandonInputReply(stale)
		a.abandonInputReply(nil)

		assert.Empty(t, turnEndEntries(sink), "a stale reply must not flush its result into another turn")
		assert.True(t, a.PublishTurnActive().Active)
		a.mu.Lock()
		assert.Same(t, current, a.inputReply)
		assert.True(t, a.interruptRequested)
		a.mu.Unlock()
	})
}

func TestQoderQueuedSteerEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("SteerInput with AttemptID rejects uninitialized session without writing", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newGoalAgent(t)
		a.mu.Lock()
		a.sessionID = ""
		a.active = true
		a.mu.Unlock()

		_, err := a.SteerInput(agent.SteerRequest{
			InputID: "steer-no-session", AttemptID: "attempt-1", Content: "Update.",
		})
		require.ErrorContains(t, err, "session is not initialized")
		assert.Empty(t, stdin.String())
	})

	t.Run("forgetQueuedSteer ignores empty ID and preserves a different entry", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newGoalAgent(t)
		kept := &inputReply{steer: &agent.SteerResolution{InputID: "kept", AttemptID: "att-1"}}
		other := &inputReply{steer: &agent.SteerResolution{InputID: "other", AttemptID: "att-2"}}
		a.mu.Lock()
		a.queuedSteers = map[string]*inputReply{"cmd-1": kept}
		a.mu.Unlock()

		a.forgetQueuedSteer("", kept)
		a.forgetQueuedSteer("cmd-1", other)

		a.mu.Lock()
		assert.Same(t, kept, a.queuedSteers["cmd-1"])
		a.mu.Unlock()

		a.forgetQueuedSteer("cmd-1", kept)
		a.mu.Lock()
		assert.Empty(t, a.queuedSteers)
		a.mu.Unlock()
	})

	t.Run("started steer falls back to uncertain and incomplete when echo resolution fails", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newGoalAgent(t)
		a.sink = &failEchoedSteerSink{
			ProviderServices: agent.NewProviderServices(sink),
			err:              errors.New("steer resolution write failed"),
		}
		a.setTurnActive(true)

		request := agent.SteerRequest{InputID: "steer-fail-echo", AttemptID: "attempt-1", Content: "Follow up."}
		require.NoError(t, agenttest.SteerInput(a, request))
		receipt := steerReceiptFromFrame(t, stdin.String(), a.nativeSessionID())

		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
		a.HandleOutput(userEchoFrame(receipt))

		assert.Equal(t, []agent.SteerResolution{{
			InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeUncertain,
		}}, sink.SteerResolutions())

		a.HandleOutput([]byte(qoderSuccessResult))
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))
		end := onlyTurnEnd(t, sink)
		assert.Empty(t, end.NativeTurnID, "an incomplete steer reply must settle unproved")
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})
}

func TestQoderObserveInputEchoForeignSessionAndTurnEndPersistFailure(t *testing.T) {
	t.Parallel()

	t.Run("foreign-session replay echo during started turn does not invalidate ownership", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		receipt, err := a.SendInput("Check the release.", nil)
		require.NoError(t, err)
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
		a.HandleOutput(userEchoFrame(receipt))

		// Foreign session replay echo must be ignored, not mark the current reply incomplete.
		a.HandleOutput([]byte(`{"type":"user","uuid":"other-uuid","session_id":"other-session","isReplay":true,"message":{"role":"user","content":"Other session."}}`))
		a.HandleOutput([]byte(qoderSuccessResult))
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))

		end := onlyTurnEnd(t, sink)
		assert.Equal(t, receipt.TurnID, end.NativeTurnID)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, end.InputTurnOutcome)
		assert.Empty(t, sink.NativeReplyRetirements())
	})

	t.Run("PersistTurnEnd failure on proved command completion retires the input reply", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.sink = agent.NewProviderServices(sink)
		receipt, err := a.SendInput("Check the release.", nil)
		require.NoError(t, err)
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
		a.HandleOutput(userEchoFrame(receipt))
		a.HandleOutput([]byte(qoderSuccessResult))

		sink.PersistErr = errors.New("turn end store failed")
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandCompleted))

		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		assert.False(t, a.PublishTurnActive().Active)
	})
}

func TestQoderHandleAssistantAndMessageContentEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("malformed assistant JSON persists with current session and arms turn", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		raw := []byte(`{"type":"assistant","message":`)
		a.handleAssistant(raw)

		assert.True(t, a.PublishTurnActive().Active)
		msgs := sink.Messages()
		require.Len(t, msgs, 1)
		assert.Equal(t, "session-1", msgs[0].AgentSessionID)
		assert.Equal(t, raw, msgs[0].Content)
	})

	t.Run("assistant frame with non-string parent_tool_use_id is not treated as root turn output", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		receipt, err := a.SendInput("Check.", nil)
		require.NoError(t, err)
		a.HandleOutput(commandFrame(receipt.SessionID, receipt.InputID, commandStarted))
		a.HandleOutput(userEchoFrame(receipt))

		a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","parent_tool_use_id":123,"message":{"role":"assistant","content":[{"type":"text","text":"Non-root."}]}}`))
		msgs := sink.Messages()
		require.Len(t, msgs, 1)
		assert.Empty(t, msgs[0].NativeTurnID)
	})

	t.Run("assistant frame with malformed content block array still persists raw message", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		raw := []byte(`{"type":"assistant","session_id":"session-1","message":{"role":"assistant","content":[{"type":123}]}}`)
		a.HandleOutput(raw)

		msgs := sink.Messages()
		require.Len(t, msgs, 1)
		assert.JSONEq(t, string(raw), string(msgs[0].Content))
	})

	t.Run("aborted tool_use assistant frame preserves interrupted completion on tool open", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newGoalAgent(t)
		a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","aborted":true,"message":{"role":"assistant","content":[{"type":"tool_use","id":"call-aborted","name":"Bash","input":{"command":"ls"}}]}}`))

		msgs := sink.Messages()
		require.Len(t, msgs, 1)
		assert.Equal(t, agent.MessageCompletionInterrupted, msgs[0].Completion)
		assert.Equal(t, "call-aborted", msgs[0].SpanID)
	})

	t.Run("qoderMessageContent matches aborted and session_id case-sensitively", func(t *testing.T) {
		t.Parallel()
		upperAborted := qoderMessageContent([]byte(`{"type":"assistant","ABORTED":true}`), "fallback-session")
		assert.Empty(t, upperAborted.Completion)
		assert.Equal(t, "fallback-session", upperAborted.AgentSessionID)

		userAborted := qoderMessageContent([]byte(`{"type":"user","session_id":"explicit-session","aborted":true}`), "fallback-session")
		assert.Empty(t, userAborted.Completion)
		assert.Equal(t, "explicit-session", userAborted.AgentSessionID)
	})
}
