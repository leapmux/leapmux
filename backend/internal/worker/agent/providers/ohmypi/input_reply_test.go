package ohmypi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOMPReplyStopsCollectingAtItsNativeYield(t *testing.T) {
	r := newRig(t)
	receipt, err := r.agent.SendInput("A reader prompt.", nil)
	require.NoError(t, err)
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	assert.Equal(t, receipt.InputID, commands[0].ID)
	assert.NotContains(t, commands[0].Payload, "streamingBehavior")
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Retry context."}]}}`,
		`{"type":"agent_end","isTerminal":false,"yielded":false}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Owned final reply."}]}}`,
		`{"type":"agent_end","isTerminal":false,"yielded":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Background job result."}]}}`,
		ompResultFrame("another-command", "completed"),
	)
	assert.Empty(t, r.sink.NativeReplyTurns())
	r.emit(ompResultFrame(receipt.InputID, "completed"))
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: "omp-reply:" + receipt.TurnID + ":2"},
			{Ordinal: 3, MessageKey: "omp-reply:" + receipt.TurnID + ":3"},
		},
	}}, r.sink.NativeReplyTurns())
	assert.True(t, r.agent.PublishTurnActive().Active, "the native background run remains active")
}

func TestOMPExactInputStaysInWorkerQueueAfterNativeBusyRefusal(t *testing.T) {
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{
				Error:  "Agent is already processing. Use steer() or followUp() to queue messages, or wait for completion.",
				Before: []string{`{"type":"agent_start"}`},
			}
		}
		return nil
	})
	receipt, err := r.agent.SendInput("A reader prompt.", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, receipt)
	assert.True(t, r.agent.PublishTurnActive().Active)
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	assert.NotContains(t, commands[0].Payload, "streamingBehavior")
	r.emit(`{"type":"agent_end","isTerminal":true}`, ompResultFrame(commands[0].ID, "completed"))
	assert.Empty(t, r.sink.NativeReplyTurns(), "a refused prompt owns no reply")
}

func TestOMPReplyRequiresEvidenceOfSuccess(t *testing.T) {
	for _, tc := range []struct {
		status  string
		outcome leapmuxv1.AgentInputTurnOutcome
	}{
		{"completed", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
		{"aborted", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED},
		{"error", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED},
		{"future", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
	} {
		t.Run(tc.status, func(t *testing.T) {
			r := newRig(t)
			receipt, err := r.agent.SendInput("A reader prompt.", nil)
			require.NoError(t, err)
			r.emit(ompResultFrame(receipt.InputID, tc.status))
			assert.Empty(t, r.sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, r.sink.NativeReplyRetirements())
		})
	}
}

func TestOMPReplyFinishesAtNativePauseWhileSessionIsActive(t *testing.T) {
	r := newRig(t)
	receipt, err := r.agent.SendInput("A reader prompt.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Ready for background work."}]}}`,
		`{"type":"agent_pause","yielded":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	turns := r.sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "omp-reply:" + receipt.TurnID + ":2"}}, turns[0].Items)
	assert.True(t, r.agent.PublishTurnActive().Active)
}

func TestOMPLocalReplyCanCompleteAtItsAcknowledgement(t *testing.T) {
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
		}
		return nil
	})
	receipt, err := r.agent.SendInput("Text handled by an input hook.", nil)
	require.NoError(t, err)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
	}}, r.sink.NativeReplyTurns())
	assert.False(t, r.agent.PublishTurnActive().Active)
}

func TestOMPLateAcknowledgementCannotClaimAnotherRun(t *testing.T) {
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Before: []string{`{"type":"agent_start"}`}}
		}
		return nil
	})
	receipt, err := r.agent.SendInput("A reader prompt.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_end","isTerminal":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Unrelated reply."}]}}`,
		`{"type":"agent_end","isTerminal":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	assert.Empty(t, r.sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, r.sink.NativeReplyRetirements())
}

func TestOMPQueuedSteersShareReplyAcrossToolUse(t *testing.T) {
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`)
	lead, err := r.agent.SendInput("Check the main shipment.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Looking up the shipment."}]}}`,
		`{"type":"turn_end","message":{"role":"assistant","stopReason":"toolUse"}}`,
	)
	for _, id := range []string{"first", "second"} {
		require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{
			Content: "Check the next shipment.", InputID: id, AttemptID: id + "-attempt",
		}))
	}
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 3)
	for _, command := range commands[1:] {
		assert.Equal(t, "steer-prefix:"+command.ID+"\nCheck the next shipment.", command.Payload["message"])
		r.emit(fmt.Sprintf(`{"type":"message_end","message":{"role":"user","steering":true,"leapmuxInputId":%q}}`, command.ID))
	}
	assert.Empty(t, r.sink.NativeReplyTurns())
	assert.Equal(t, []agent.SteerResolution{
		{InputID: "first", AttemptID: "first-attempt", Outcome: agent.SteerOutcomeEchoed,
			Receipt: agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: commands[1].ID, TurnID: lead.TurnID}},
		{InputID: "second", AttemptID: "second-attempt", Outcome: agent.SteerOutcomeEchoed,
			Receipt: agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: commands[2].ID, TurnID: lead.TurnID}},
	}, r.sink.SteerResolutions())
	r.emit(
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The shipment is ready."}]}}`,
		`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`,
		`{"type":"agent_end","yielded":true}`,
		ompResultFrame(commands[1].ID, "completed"),
		ompResultFrame(commands[2].ID, "completed"),
		ompResultFrame(lead.InputID, "completed"),
	)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: lead.SessionID, TurnID: lead.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{
			{InputID: lead.InputID, AfterOrdinal: 1},
			{InputID: commands[1].ID, AfterOrdinal: 3},
			{InputID: commands[2].ID, AfterOrdinal: 4},
		},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: "omp-reply:" + lead.TurnID + ":2"},
			{Ordinal: 5, MessageKey: "omp-reply:" + lead.TurnID + ":5"},
		},
	}}, r.sink.NativeReplyTurns())
}

func TestOMPSettledPromptWithoutUserEchoLeavesSteerUncertain(t *testing.T) {
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}
	r.emit(
		`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`,
		`{"type":"agent_start"}`,
	)
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{
		Content: "Check the next shipment.", InputID: "steer", AttemptID: "steer-attempt",
	}))
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	r.emit(
		fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":"completed"}`, commands[0].ID),
		ompResultFrame(commands[0].ID, "completed"),
	)
	assert.Empty(t, r.sink.SteerResolutions(), "a result without settlement proves no delivery")
	r.emit(
		`{"type":"agent_end","yielded":true}`,
		fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":"completed","agentInvoked":true,"sessionSettled":true}`, commands[0].ID),
	)
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "steer", AttemptID: "steer-attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, r.sink.SteerResolutions())
	assert.Empty(t, r.sink.NativeReplyTurns())
}

func ompResultFrame(id, status string) string {
	return fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":%q,"agentInvoked":true,"sessionSettled":false}`, id, status)
}

func TestOMPReplyTextUsesJavaScriptWhitespace(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	receipt, err := r.agent.SendInput("Check whitespace.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		// U+FEFF is blank in JS .trim() and must NOT be recorded as a reply item.
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"\uFEFF"}]}}`,
		// U+0085 is visible in JS .trim() and MUST be recorded as a reply item.
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"\u0085"}]}}`,
		`{"type":"agent_end","yielded":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	turns := r.sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "omp-reply:" + receipt.TurnID + ":2"}}, turns[0].Items)
	var matched []byte
	for _, msg := range r.sink.Messages() {
		if msg.IdempotencyKey == turns[0].Items[0].MessageKey {
			matched = msg.Content
		}
	}
	assert.Contains(t, string(matched), `\u0085`, "canonical reply item must be the U+0085 message, not U+FEFF")
}

func TestOMPBusyRefusalBeforeAgentStartKeepsTurnActive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{
				Error: "Agent is already processing. Use steer() or followUp() to queue messages, or wait for completion.",
			}
		}
		return nil
	})
	receipt, err := r.agent.SendInput("Prompt racing background start.", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, receipt)
	assert.True(t, r.agent.PublishTurnActive().Active, "busy refusal must not publish TurnState{Active: false} before agent_start arrives")

	r.agent.Mu.Lock()
	armedBy := r.agent.turn.armedBy
	r.agent.Mu.Unlock()
	assert.Empty(t, armedBy, "busy refusal must clear turn.armedBy so a later prompt_result cannot disarm the turn")

	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	r.emit(fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":"completed","agentInvoked":false}`, commands[0].ID))
	assert.True(t, r.agent.PublishTurnActive().Active, "subsequent prompt_result for refused ID must not disarm the active turn")

	r.agent.finishOutput(agent.MessageCompletionComplete)
	assert.Empty(t, r.sink.NativeReplyRetirements(), "refused prompt must not leave a reply prompt to retire on finishOutput")
}

func TestOMPSteerEchoSurvivesSessionFileTransitionAndDecrementsPendingSteers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Mu.Lock()
	r.agent.sessionFile = ""
	r.agent.sessionID = "sess-lazy"
	r.agent.Mu.Unlock()
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`,
		`{"type":"agent_start"}`)
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{
		Content: "Guide mid-turn.", InputID: "steer-1", AttemptID: "att-1",
	}))
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	// Simulate lazy sessionFile population between SteerInput and message_end.
	r.agent.Mu.Lock()
	r.agent.sessionFile = "/s/sess-lazy.jsonl"
	r.agent.Mu.Unlock()
	r.emit(fmt.Sprintf(`{"type":"message_end","message":{"role":"user","steering":true,"leapmuxInputId":%q}}`, commands[0].ID))
	resolutions := r.sink.SteerResolutions()
	require.Len(t, resolutions, 1, "steer echo must match after lazy sessionFile transition")
	assert.Equal(t, agent.SteerOutcomeEchoed, resolutions[0].Outcome)
	assert.Equal(t, "/s/sess-lazy.jsonl", resolutions[0].Receipt.SessionID, "matched steer receipt must adopt the transitioned sessionFile")
	r.agent.Mu.Lock()
	pending := r.agent.turn.pendingSteers
	r.agent.Mu.Unlock()
	assert.Equal(t, 0, pending)
}

func TestOMPProcessExitDuringWrittenSteerReturnsUncertainAndPreservesIdentifiedSteer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`,
		`{"type":"agent_start"}`)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Skip: true}
		}
		return nil
	})
	errCh := make(chan error, 1)
	go func() {
		errCh <- agenttest.SteerInput(r.agent, agent.SteerRequest{
			Content: "Steer before crash.", InputID: "steer-crash", AttemptID: "att-crash",
		})
	}()
	r.waitForCommand(CommandPrompt, 1)
	require.NoError(t, r.stdinW.Close())
	err := <-errCh
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	r.agent.finishOutput(agent.MessageCompletionError)
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "steer-crash", AttemptID: "att-crash", Outcome: agent.SteerOutcomeUncertain,
	}}, r.sink.SteerResolutions())
}

func TestOMPRetiredControlsWrapErrControlRequestNotPending(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	err := r.agent.SendRawInput(agent.RawInput{Data: []byte(`{"type":"extension_ui_response","id":"missing","value":"Approve"}`)})
	require.ErrorIs(t, err, agent.ErrControlRequestNotPending)
	err = r.agent.SendRawInput(agent.RawInput{Data: []byte(`{"type":"leapmux_ask_answer","id":"missing-ask","answers":[]}`)})
	require.ErrorIs(t, err, agent.ErrControlRequestNotPending)
}

func TestOMPNoteUncommittedReplyUsesJavaScriptWhitespaceAndIgnoresThinking(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		event    string
		unproved bool
	}{
		{
			name:     "U+0085 NEL text_delta marks reply unproved",
			event:    `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"\u0085"}}`,
			unproved: true,
		},
		{
			name:     "U+FEFF BOM text_delta leaves reply proved",
			event:    `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"\uFEFF"}}`,
			unproved: false,
		},
		{
			name:     "thinking_delta leaves reply proved",
			event:    `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"reasoning text"}}`,
			unproved: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			receipt, err := r.agent.SendInput("Check uncommitted.", nil)
			require.NoError(t, err)
			r.emit(
				`{"type":"agent_start"}`,
				tc.event,
				`{"type":"agent_pause","yielded":true}`,
				ompResultFrame(receipt.InputID, "completed"),
			)
			if tc.unproved {
				assert.Empty(t, r.sink.NativeReplyTurns())
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
					receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
				}, r.sink.NativeReplyRetirements())
			} else {
				turns := r.sink.NativeReplyTurns()
				require.Len(t, turns, 1)
				assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
				assert.Empty(t, r.sink.NativeReplyRetirements())
			}
		})
	}
}

type ompFailingReplySink struct {
	agent.ProviderServices
	persistErr error
	publishErr error
}

func (s *ompFailingReplySink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.persistErr != nil {
		return s.persistErr
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func (s *ompFailingReplySink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	if s.publishErr != nil {
		return s.publishErr
	}
	return s.ProviderServices.PublishNativeReplyTurn(turn)
}

func TestOMPPersistReplyTextNonRetainedErrorMarksUnproved(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.sink = &ompFailingReplySink{ProviderServices: r.agent.sink, persistErr: errors.New("disk full")}
	receipt, err := r.agent.SendInput("Persist error check.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Visible reply."}]}}`,
		`{"type":"agent_end","yielded":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	assert.Empty(t, r.sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, r.sink.NativeReplyRetirements())
}

func TestOMPPublishReplyToNonRetainedErrorRetiresInputsAsUnproved(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.sink = &ompFailingReplySink{ProviderServices: r.agent.sink, publishErr: errors.New("publish failed")}
	receipt, err := r.agent.SendInput("Publish error check.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Visible reply."}]}}`,
		`{"type":"agent_end","yielded":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	assert.Empty(t, r.sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, r.sink.NativeReplyRetirements())
}

type ompRetirementOrderSink struct {
	agent.ProviderServices
	mu      sync.Mutex
	events  []string
	retires []agent.NativeInputReceipt
	steers  []agent.SteerResolution
}

func (s *ompRetirementOrderSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.mu.Lock()
	s.events = append(s.events, "retire:"+receipt.InputID)
	s.retires = append(s.retires, receipt)
	s.mu.Unlock()
	return s.ProviderServices.RetireNativeInputTurn(receipt, outcome)
}

func (s *ompRetirementOrderSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	write := s.ProviderServices.CaptureSteerResolution(resolution)
	return func() error {
		s.mu.Lock()
		s.events = append(s.events, "steer:"+resolution.InputID)
		s.steers = append(s.steers, resolution)
		s.mu.Unlock()
		return write()
	}
}

func TestOMPRetireOpenRepliesDeterministicOrderAndIdempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var recorder *ompRetirementOrderSink
	r.agent.sink = agent.DecorateProviderServices(r.agent.sink, func(base agent.ProviderServices) agent.ProviderServices {
		recorder = &ompRetirementOrderSink{ProviderServices: base}
		return recorder
	})

	for _, id := range []string{"reply-c", "reply-a", "reply-b"} {
		r.agent.registerReplyPrompt(agent.NativeInputReceipt{SessionID: "/s/s.jsonl", InputID: id, TurnID: id})
	}
	for _, pair := range []struct{ key, inputID string }{
		{"k-z", "steer-z"},
		{"k-a", "steer-a"},
		{"k-m", "steer-m"},
	} {
		r.agent.registerOMPSteer(agent.SteerResolution{
			InputID:   pair.inputID,
			AttemptID: pair.inputID + "-att",
			Receipt:   agent.NativeInputReceipt{SessionID: "/s/s.jsonl", InputID: pair.key, TurnID: pair.key},
		})
	}

	r.agent.retireOpenReplies(agent.SteerOutcomeUncertain)

	assert.Equal(t, []string{
		"retire:reply-a", "retire:reply-b", "retire:reply-c",
		"steer:steer-a", "steer:steer-m", "steer:steer-z",
	}, recorder.events)
	for _, steer := range recorder.steers {
		assert.Equal(t, agent.SteerOutcomeUncertain, steer.Outcome)
		assert.Empty(t, steer.Receipt, "unread steer resolution must clear receipt")
	}

	// Second call must be a no-op.
	r.agent.retireOpenReplies(agent.SteerOutcomeDropped)
	assert.Len(t, recorder.events, 6, "second retireOpenReplies must be idempotent")
}
