package ohmypi

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOMPIdleChildReplyBelongsToTheNativeSession(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
		frameTaskStart, frameStarted, strings.ReplaceAll(frameStarted, "Probe", "Sibling"))
	child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
	sibling := r.sink.Child(subagentRow(t, r, "Sibling").ChildAgentID)
	key := subagentRowKey(r.agent.sessionID, "Probe")

	result, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "first", Content: "Next check."})
	require.NoError(t, err)
	require.Equal(t, "/s/Probe.jsonl", result.NativeReceipt.SessionID)
	require.NotEmpty(t, result.NativeReceipt.InputID)
	commands := r.commandsOfType(CommandSteerSubagent)
	require.Len(t, commands, 1)
	assert.Equal(t, "child-prefix:"+result.NativeReceipt.InputID+"\nNext check.", commands[0].Payload["message"])

	echo := fmt.Sprintf(`{"type":"message_end","message":{"role":"user","leapmuxInputId":%q}}`, result.NativeReceipt.InputID)
	reply := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The check passed."}]}}`
	turnEnd := `{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`
	yield := `{"type":"agent_pause","yielded":true,"messages":[{"role":"assistant","stopReason":"stop"}]}`
	r.emit(subagentEvent("Sibling", echo), subagentEvent("Sibling", reply), subagentEvent("Sibling", turnEnd), subagentEvent("Sibling", yield))
	assert.Empty(t, sibling.NativeReplyTurns(), "an echo on a sibling route cannot claim the receipt")
	r.emit(subagentEvent("Probe", `{"type":"agent_start"}`), subagentEvent("Probe", echo),
		subagentEvent("Probe", reply), subagentEvent("Probe", turnEnd), subagentEvent("Probe", yield),
		subagentEvent("Probe", `{"type":"agent_start"}`),
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Later autonomous output."}]}}`),
		frameCompleted)

	inputID := result.NativeReceipt.InputID
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "/s/Probe.jsonl", TurnID: inputID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: inputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "omp-reply:" + inputID + ":2"}},
	}}, child.NativeReplyTurns())
	assert.Empty(t, r.sink.NativeReplyTurns())
}

func TestOMPChildCloseWithoutEchoLeavesSteerUncertain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
		frameTaskStart, frameStarted, subagentEvent("Probe", `{"type":"agent_start"}`))
	child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
	_, err := r.agent.SteerChildInput(subagentRowKey(r.agent.sessionID, "Probe"), agent.SteerRequest{
		InputID: "steer", AttemptID: "steer-attempt", Content: "Next check.",
	})
	require.NoError(t, err)
	assert.Empty(t, child.SteerResolutions(), "the command acknowledgement proves no delivery")
	r.emit(frameCompleted)
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "steer", AttemptID: "steer-attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, child.SteerResolutions())
	assert.Empty(t, child.NativeReplyTurns())
	assert.Empty(t, r.sink.SteerResolutions())
}

func TestOMPOrdinaryChildUncertainDeliveryForgetsPendingChildInput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
		frameTaskStart, frameStarted)
	child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
	key := subagentRowKey(r.agent.sessionID, "Probe")
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandSteerSubagent {
			return &rigReply{Skip: true}
		}
		return nil
	})
	errCh := make(chan error, 1)
	go func() {
		_, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "c-uncertain", Content: "Follow up."})
		errCh <- err
	}()
	r.waitForCommand(CommandSteerSubagent, 1)
	require.NoError(t, r.stdinW.Close())
	err := <-errCh
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	r.emit(frameCompleted)
	assert.Empty(t, child.NativeReplyRetirements(), "unrecorded ordinary child input must be forgotten on ErrDeliveryUncertain")
	assert.Empty(t, child.NativeReplyTurns())
}

type ompCleanupTrackingSink struct {
	agent.ProviderServices
	mu           sync.Mutex
	cleanups     map[string][]string
	nilChildSink bool
}

func (s *ompCleanupTrackingSink) ChildSink(childID string) agent.ProviderServices {
	if s.nilChildSink {
		return nil
	}
	base := s.ProviderServices.ChildSink(childID)
	if base == nil {
		return nil
	}
	return &ompCleanupTrackingChildSink{ProviderServices: base, ownerID: childID, parent: s}
}

type ompCleanupTrackingChildSink struct {
	agent.ProviderServices
	ownerID string
	parent  *ompCleanupTrackingSink
}

func (s *ompCleanupTrackingChildSink) CleanupChildAgent(childID string) {
	s.parent.mu.Lock()
	if s.parent.cleanups == nil {
		s.parent.cleanups = make(map[string][]string)
	}
	s.parent.cleanups[s.ownerID] = append(s.parent.cleanups[s.ownerID], childID)
	s.parent.mu.Unlock()
	s.ProviderServices.CleanupChildAgent(childID)
}

func TestOMPNestedSubagentUsesParentSinkForChildLifecycle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var tracker *ompCleanupTrackingSink
	r.agent.sink = agent.DecorateProviderServices(r.agent.sink, func(base agent.ProviderServices) agent.ProviderServices {
		tracker = &ompCleanupTrackingSink{ProviderServices: base}
		return tracker
	})
	r.emit(frameTaskStart, frameStarted)
	outerID := subagentRow(t, r, "Probe").ChildAgentID
	outerSink := r.sink.Child(outerID)
	nestedStarted := strings.ReplaceAll(frameStarted, `"Probe"`, `"Probe.Leaf"`)
	nestedStarted = strings.ReplaceAll(nestedStarted, `/s/Probe.jsonl`, `/s/Leaf.jsonl`)
	r.emit(nestedStarted)
	r.agent.Mu.Lock()
	nested := r.agent.subagents["Probe.Leaf"]
	r.agent.Mu.Unlock()
	require.NotNil(t, nested)
	require.NotNil(t, nested.conv)
	// Nested child events should land on outerSink's child, not root's ChildSink.
	r.emit(subagentEvent("Probe.Leaf", `{"type":"message_end","message":{"role":"user","content":"Nested prompt"}}`))
	r.emit(strings.ReplaceAll(frameCompleted, `"Probe"`, `"Probe.Leaf"`))
	msgs := outerSink.Child(nested.childID).Messages()
	require.Len(t, msgs, 1, "nested child prompt must be persisted on parentSink's child")
	assert.Contains(t, string(msgs[0].Content), "Nested prompt")
	assert.Empty(t, r.sink.Child(nested.childID).Messages(), "root sink must not receive nested child transcript messages")

	tracker.mu.Lock()
	cleaned := append([]string(nil), tracker.cleanups[outerID]...)
	tracker.mu.Unlock()
	assert.Equal(t, []string{nested.childID}, cleaned, "nested child cleanup must route through outerSink")
}

func TestOMPChildSendInputErrorAndFallbackPaths(t *testing.T) {
	t.Parallel()

	t.Run("identified child steer preserves childInputs on ErrDeliveryUncertain", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
		r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
			frameTaskStart, frameStarted, subagentEvent("Probe", `{"type":"agent_start"}`))
		child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
		key := subagentRowKey(r.agent.sessionID, "Probe")
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandSteerSubagent {
				return &rigReply{Skip: true}
			}
			return nil
		})
		errCh := make(chan error, 1)
		go func() {
			_, err := r.agent.SteerChildInput(key, agent.SteerRequest{
				InputID: "c-steer-uncertain", AttemptID: "att-1", Content: "Steer before exit.",
			})
			errCh <- err
		}()
		r.waitForCommand(CommandSteerSubagent, 1)
		require.NoError(t, r.stdinW.Close())
		require.ErrorIs(t, <-errCh, agent.ErrDeliveryUncertain)

		r.emit(frameCompleted)
		assert.Equal(t, []agent.SteerResolution{{
			InputID: "c-steer-uncertain", AttemptID: "att-1", Outcome: agent.SteerOutcomeUncertain,
		}}, child.SteerResolutions(), "identified child steer must stay tracked on ErrDeliveryUncertain")
	})

	t.Run("refused ordinary and steer inputs forget childInputs", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
		r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
			frameTaskStart, frameStarted)
		child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
		key := subagentRowKey(r.agent.sessionID, "Probe")
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandSteerSubagent {
				return &rigReply{Error: "subagent refused"}
			}
			return nil
		})

		_, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "ord-refused", Content: "Follow up."})
		var refused *commandError
		require.ErrorAs(t, err, &refused)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

		r.emit(subagentEvent("Probe", `{"type":"agent_start"}`))
		_, err = r.agent.SteerChildInput(key, agent.SteerRequest{
			InputID: "steer-refused", AttemptID: "att-refused", Content: "Guide.",
		})
		require.ErrorAs(t, err, &refused)
		assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)

		r.emit(frameCompleted)
		assert.Empty(t, child.NativeReplyRetirements())
		assert.Empty(t, child.SteerResolutions())
	})

	t.Run("write failure forgets childInputs for ordinary and steer inputs", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
		r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
			frameTaskStart, frameStarted)
		child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
		key := subagentRowKey(r.agent.sessionID, "Probe")
		require.NoError(t, r.stdinW.Close())

		_, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "ord-write", Content: "Follow up."})
		require.Error(t, err)

		r.emit(subagentEvent("Probe", `{"type":"agent_start"}`))
		_, err = r.agent.SteerChildInput(key, agent.SteerRequest{
			InputID: "steer-write", AttemptID: "att-write", Content: "Guide.",
		})
		require.Error(t, err)

		r.emit(frameCompleted)
		assert.Empty(t, child.NativeReplyRetirements())
		assert.Empty(t, child.SteerResolutions())
	})

	t.Run("unmarked steerAtEcho fallback emits immediate echoed resolution with empty receipt", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name        string
			bridgeReady bool
			sessionFile string
			content     string
		}{
			{name: "bridge not ready", bridgeReady: false, sessionFile: "/s/Probe.jsonl", content: "Guide."},
			{name: "empty sessionFile", bridgeReady: true, sessionFile: "", content: "Guide."},
			{name: "slash command", bridgeReady: true, sessionFile: "/s/Probe.jsonl", content: "/status"},
			{name: "leading-space slash command", bridgeReady: true, sessionFile: "/s/Probe.jsonl", content: "  /status"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				r := newRig(t)
				r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
				if tc.bridgeReady {
					r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`)
				}
				started := strings.ReplaceAll(frameStarted, `/s/Probe.jsonl`, tc.sessionFile)
				r.emit(frameTaskStart, started, subagentEvent("Probe", `{"type":"agent_start"}`))
				child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
				key := subagentRowKey(r.agent.sessionID, "Probe")

				_, err := r.agent.SteerChildInput(key, agent.SteerRequest{
					InputID: "steer-fallback", AttemptID: "att-fallback", Content: tc.content,
				})
				require.NoError(t, err)
				commands := r.commandsOfType(CommandSteerSubagent)
				require.Len(t, commands, 1)
				assert.Equal(t, tc.content, commands[0].Payload["message"], "unmarked fallback must not prepend ChildPrefix")
				assert.Equal(t, []agent.SteerResolution{{
					InputID: "steer-fallback", AttemptID: "att-fallback", Outcome: agent.SteerOutcomeEchoed,
				}}, child.SteerResolutions())
			})
		}
	})
}

func TestOMPFinishChildReplyAtYieldVariantsAndOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("agent_pause with yielded false or omitted keeps childReply open", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
		r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
			frameTaskStart, frameStarted)
		child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
		key := subagentRowKey(r.agent.sessionID, "Probe")

		res, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "c1", Content: "Work."})
		require.NoError(t, err)
		inputID := res.NativeReceipt.InputID
		r.emit(
			subagentEvent("Probe", `{"type":"agent_start"}`),
			subagentEvent("Probe", fmt.Sprintf(`{"type":"message_end","message":{"role":"user","leapmuxInputId":%q}}`, inputID)),
			subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Part 1."}]}}`),
			subagentEvent("Probe", `{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`),
			subagentEvent("Probe", `{"type":"agent_pause","yielded":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`),
			subagentEvent("Probe", `{"type":"agent_pause","messages":[{"role":"assistant","stopReason":"stop"}]}`),
		)
		assert.Empty(t, child.NativeReplyTurns(), "non-yielding agent_pause must keep childReply open")

		r.emit(
			subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Part 2."}]}}`),
			subagentEvent("Probe", `{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`),
			subagentEvent("Probe", `{"type":"agent_pause","yielded":true,"messages":[{"role":"assistant","stopReason":"stop"}]}`),
		)
		turns := child.NativeReplyTurns()
		require.Len(t, turns, 1)
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
		assert.Len(t, turns[0].Items, 2)
	})

	t.Run("agent_end yield variants", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name        string
			agentEnd    string
			shouldYield bool
		}{
			{
				name:        "yielded false stays open",
				agentEnd:    `{"type":"agent_end","yielded":false,"isTerminal":true,"messages":[{"role":"assistant","stopReason":"stop"}]}`,
				shouldYield: false,
			},
			{
				name:        "yielded nil and isTerminal false stays open",
				agentEnd:    `{"type":"agent_end","isTerminal":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`,
				shouldYield: false,
			},
			{
				name:        "yielded nil and isTerminal nil yields",
				agentEnd:    `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}]}`,
				shouldYield: true,
			},
			{
				name:        "yielded nil and isTerminal true yields",
				agentEnd:    `{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"stop"}]}`,
				shouldYield: true,
			},
			{
				name:        "yielded true and isTerminal false yields",
				agentEnd:    `{"type":"agent_end","yielded":true,"isTerminal":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`,
				shouldYield: true,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				r := newRig(t)
				r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
				r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
					frameTaskStart, frameStarted)
				child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
				key := subagentRowKey(r.agent.sessionID, "Probe")

				res, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "c1", Content: "Work."})
				require.NoError(t, err)
				r.emit(
					subagentEvent("Probe", `{"type":"agent_start"}`),
					subagentEvent("Probe", fmt.Sprintf(`{"type":"message_end","message":{"role":"user","leapmuxInputId":%q}}`, res.NativeReceipt.InputID)),
					subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`),
					subagentEvent("Probe", `{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`),
					subagentEvent("Probe", tc.agentEnd),
				)
				if tc.shouldYield {
					require.Len(t, child.NativeReplyTurns(), 1)
					assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, child.NativeReplyTurns()[0].Outcome)
				} else {
					assert.Empty(t, child.NativeReplyTurns())
				}
			})
		}
	})

	t.Run("outcomes at yield", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name        string
			turnStop    string
			endMessages string
			markUnprov  bool
			wantPublish bool
			wantOutcome leapmuxv1.AgentInputTurnOutcome
		}{
			{
				name:        "length stopReason matching reply.stop completes",
				turnStop:    "length",
				endMessages: `[{"role":"assistant","stopReason":"length"}]`,
				wantPublish: true,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			},
			{
				name:        "toolUse stopReason matching reply.stop completes",
				turnStop:    "toolUse",
				endMessages: `[{"role":"assistant","stopReason":"toolUse"}]`,
				wantPublish: true,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			},
			{
				name:        "mismatched reply.stop retires as UNPROVED",
				turnStop:    "toolUse",
				endMessages: `[{"role":"assistant","stopReason":"stop"}]`,
				wantPublish: false,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			},
			{
				name:        "aborted publishes INTERRUPTED",
				turnStop:    "aborted",
				endMessages: `[{"role":"assistant","stopReason":"aborted"}]`,
				wantPublish: true,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
			},
			{
				name:        "error publishes FAILED",
				turnStop:    "error",
				endMessages: `[{"role":"assistant","stopReason":"error"}]`,
				wantPublish: true,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
			},
			{
				name:        "no assistant message retires as UNPROVED",
				turnStop:    "stop",
				endMessages: `[{"role":"user","content":"only user"}]`,
				wantPublish: false,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			},
			{
				name:        "reply.unproved retires as UNPROVED",
				turnStop:    "stop",
				endMessages: `[{"role":"assistant","stopReason":"stop"}]`,
				markUnprov:  true,
				wantPublish: false,
				wantOutcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				r := newRig(t)
				r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
				r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
					frameTaskStart, frameStarted)
				child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
				key := subagentRowKey(r.agent.sessionID, "Probe")

				res, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "c1", Content: "Work."})
				require.NoError(t, err)
				inputID := res.NativeReceipt.InputID
				r.emit(
					subagentEvent("Probe", `{"type":"agent_start"}`),
					subagentEvent("Probe", fmt.Sprintf(`{"type":"message_end","message":{"role":"user","leapmuxInputId":%q}}`, inputID)),
					subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Reply."}]}}`),
					subagentEvent("Probe", fmt.Sprintf(`{"type":"turn_end","message":{"role":"assistant","stopReason":%q}}`, tc.turnStop)),
				)
				if tc.markUnprov {
					r.agent.Mu.Lock()
					r.agent.subagents["Probe"].conv.childReply.unproved = true
					r.agent.Mu.Unlock()
				}
				r.emit(subagentEvent("Probe", fmt.Sprintf(`{"type":"agent_end","yielded":true,"messages":%s}`, tc.endMessages)))

				if tc.wantPublish {
					turns := child.NativeReplyTurns()
					require.Len(t, turns, 1)
					assert.Equal(t, tc.wantOutcome, turns[0].Outcome)
					assert.Empty(t, child.NativeReplyRetirements())
				} else {
					assert.Empty(t, child.NativeReplyTurns())
					expectedReceipt := agent.NativeInputReceipt{SessionID: "/s/Probe.jsonl", InputID: inputID, TurnID: inputID}
					assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
						expectedReceipt: tc.wantOutcome,
					}, child.NativeReplyRetirements())
				}
			})
		}
	})
}

func TestOMPRetireChildInputsRetiresActiveReplyAndPendingInSortedOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	r.agent.Mu.Lock()
	conv := r.agent.subagents["Probe"].conv
	r.agent.Mu.Unlock()
	require.NotNil(t, conv)

	recorder := &ompRetirementOrderSink{ProviderServices: conv.sink}
	conv.sink = recorder

	activeReply := newOMPReplyPrompt(agent.NativeInputReceipt{SessionID: "/s/Probe.jsonl", InputID: "active-lead", TurnID: "active-lead"})
	activeReply.appendInput("active-steer")

	r.agent.Mu.Lock()
	conv.childReply = activeReply
	conv.childInputs = map[string]ompChildInput{
		"z-ord": {
			receipt: agent.NativeInputReceipt{SessionID: "/s/Probe.jsonl", InputID: "z-ord"},
		},
		"b-steer": {
			receipt: agent.NativeInputReceipt{SessionID: "/s/Probe.jsonl", InputID: "b-steer"},
			steer:   agent.SteerResolution{InputID: "steer-b", AttemptID: "att-b"},
		},
		"m-ord": {
			receipt: agent.NativeInputReceipt{SessionID: "/s/Probe.jsonl", InputID: "m-ord"},
		},
		"a-steer": {
			receipt: agent.NativeInputReceipt{SessionID: "/s/Probe.jsonl", InputID: "a-steer"},
			steer:   agent.SteerResolution{InputID: "steer-a", AttemptID: "att-a"},
		},
	}
	r.agent.Mu.Unlock()

	r.agent.retireOMPChildInputs(conv)

	assert.Equal(t, []string{
		"retire:active-lead",
		"retire:active-steer",
		"steer:steer-a",
		"steer:steer-b",
		"retire:m-ord",
		"retire:z-ord",
	}, recorder.events)
	for _, steer := range recorder.steers {
		assert.Equal(t, agent.SteerOutcomeUncertain, steer.Outcome)
		assert.Empty(t, steer.Receipt)
	}
}

func TestOMPStartSubagentWhenChildSinkIsNilDoesNotPanicAndReportsRouteNotReady(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.sink = agent.DecorateProviderServices(r.agent.sink, func(base agent.ProviderServices) agent.ProviderServices {
		return &ompCleanupTrackingSink{ProviderServices: base, nilChildSink: true}
	})
	r.emit(frameTaskStart, frameStarted)

	r.agent.Mu.Lock()
	state := r.agent.subagents["Probe"]
	r.agent.Mu.Unlock()
	require.NotNil(t, state)
	assert.Nil(t, state.conv)

	// subagent_event must not panic when conv is nil.
	assert.NotPanics(t, func() {
		r.emit(subagentEvent("Probe", `{"type":"agent_start"}`))
		r.emit(subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hi"}]}}`))
	})

	key := subagentRowKey(r.agent.sessionID, "Probe")
	_, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "c1", Content: "Follow up."})
	require.ErrorIs(t, err, agent.ErrChildRouteNotReady)

	assert.NotPanics(t, func() {
		r.emit(frameCompleted)
	})
}
