package ohmypi

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSteerDeclaresRowAtEchoAndSurvival(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	echo, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok)
	assert.True(t, echo.SteerRowAtEcho())
	survivor, ok := any(a).(agent.SteerSurvivor)
	require.True(t, ok)
	assert.True(t, survivor.SteerSurvivesTurnEnd())
}

func TestSteerEchoAtTheUserMessage(t *testing.T) {
	t.Parallel()
	for _, steering := range []bool{false, true} {
		t.Run(fmt.Sprint(steering), func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"agent_start"}`)
			require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "Guide the next answer."}))
			assert.Empty(t, r.sink.SteerEchoReports())
			// Trimmed echo shape from .tmp/verify-c29/ohmypi/run2-steer.
			r.emit(fmt.Sprintf(`{"type":"message_end","message":{"role":"user","steering":%t,"content":[{"type":"text","text":"Guide the next answer."}]}}`, steering))
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
			assert.Empty(t, r.sink.Messages(), "the queue owns the user row")
		})
	}
}

func TestSteerAnsweredLocallyReportsTheEchoAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
		}
		return nil
	})
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "/status"}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerFailedAfterAckIsDroppedInOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "first"}))
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "second"}))
	prompts := r.commandsOfType(CommandPrompt)
	require.Len(t, prompts, 2)
	r.emit(fmt.Sprintf(`{"type":"response","id":%q,"command":"prompt","success":false,"error":"The second prompt was refused"}`, prompts[1].ID))
	assert.Empty(t, r.sink.SteerEchoReports(), "a later refusal waits for the earlier steer")
	r.emit(`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"first"}]}}`)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerLocalResultBeforeAckReportsOnlyOneEcho(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`), Before: []string{fmt.Sprintf(`{"type":"prompt_result","id":%q,"agentInvoked":false}`, command.ID)}}
		}
		return nil
	})
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "/status"}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerMalformedPromptResultDoesNotClaimAnEcho(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "guide"}))
	id := r.commandsOfType(CommandPrompt)[0].ID
	r.emit(fmt.Sprintf(`{"type":"prompt_result","id":%q}`, id), fmt.Sprintf(`{"type":"prompt_result","id":%q,"agentInvoked":null}`, id))
	assert.Empty(t, r.sink.SteerEchoReports())
	r.agent.Mu.Lock()
	pending := r.agent.turn.pendingSteers
	r.agent.Mu.Unlock()
	assert.Equal(t, 1, pending)
	r.emit(`{"type":"message_end","message":{"role":"user","content":"guide"}}`)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerEchoSurvivesThePreviousTurnEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "late steer"}))
	r.emit(`{"type":"agent_end","isTerminal":true,"messages":[]}`)
	assert.Empty(t, r.sink.SteerEchoReports())
	r.emit(`{"type":"agent_start"}`, `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"late steer"}]}}`)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerRefusedCallReportsNoEchoOrDrop(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Error: "The steer was refused"}
		}
		return nil
	})
	require.Error(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "refused"}))
	assert.Empty(t, r.agent.steers.Pending())
	assert.Empty(t, r.sink.SteerEchoReports())
}

func TestSteerClearReportsDroppedInputsOnlyAfterNativeSuccess(t *testing.T) {
	t.Parallel()
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"agent_start"}`)
			require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "pending"}))
			r.respond(func(command recordedCommand) *rigReply {
				switch command.Type {
				case CommandNewSession:
					return &rigReply{Data: json.RawMessage(fmt.Sprintf(`{"cancelled":%t}`, cancelled))}
				case CommandGetState:
					return &rigReply{Error: "state unavailable"}
				default:
					return nil
				}
			})
			_, err := r.agent.ClearContext()
			if cancelled {
				require.ErrorIs(t, err, agent.ErrContextClearCancelled)
				assert.Empty(t, r.sink.SteerEchoReports())
				assert.Len(t, r.agent.steers.Pending(), 1)
			} else {
				require.ErrorContains(t, err, "state unavailable")
				assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, r.sink.SteerEchoReports())
				assert.Empty(t, r.agent.steers.Pending())
			}
		})
	}
}

func TestSteerEchoTextValidatesTheNativeContent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw, text string
		valid     bool
	}{
		{raw: `""`, text: "", valid: true},
		{raw: `[]`, text: "", valid: true},
		{raw: `" guide "`, text: " guide ", valid: true},
		{raw: `[{"type":"text","text":"one"},{"type":"text","text":"two"}]`, text: "one\ntwo", valid: true},
		{raw: `[{"type":"text","text":""},{"type":"text","text":"one"},{"type":"text","text":""},{"type":"text","text":"two"}]`, text: "one\ntwo", valid: true},
		{raw: `[{"type":"text","text":"caption"},{"type":"image","data":"bytes"},{"type":"text","text":"tail"}]`, text: "caption\ntail", valid: true},
		{raw: `[{"type":"image","data":"bytes"}]`, valid: true},
		{raw: `null`}, {raw: ``}, {raw: `42`}, {raw: `{}`}, {raw: `[null]`},
		{raw: `[{"type":"text"}]`}, {raw: `[{"type":"text","text":null}]`},
		{raw: `[{"type":"future"}]`},
	} {
		t.Run(test.raw, func(t *testing.T) {
			text, valid := steerEchoText(json.RawMessage(test.raw))
			assert.Equal(t, test.valid, valid)
			assert.Equal(t, test.text, text)
		})
	}
}

func TestHandleMessageEndUnmatchedSteerInputIDDecrementsPendingOnceWithoutLegacyEcho(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "guide"}))
	r.agent.Mu.Lock()
	require.Equal(t, 1, r.agent.turn.pendingSteers)
	r.agent.Mu.Unlock()

	r.emit(`{"type":"message_end","message":{"role":"user","steering":true,"leapmuxInputId":"unmatched-id","content":"guide"}}`)

	r.agent.Mu.Lock()
	pending := r.agent.turn.pendingSteers
	r.agent.Mu.Unlock()
	assert.Equal(t, 0, pending, "unmatched steer with steering:true must decrement pendingSteers once")
	assert.Empty(t, r.sink.SteerEchoReports(), "unmatched SteerInputID must not fall through to legacy EchoKey")
	assert.Len(t, r.agent.steers.Pending(), 1, "legacy steer must remain pending")
}

func TestObserveOMPUserMessageBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("stale session mismatch returns false and preserves steer", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.Mu.Lock()
		r.agent.sessionFile = "/s/old.jsonl"
		r.agent.sessionID = "old-id"
		r.agent.Mu.Unlock()
		r.agent.registerOMPSteer(agent.SteerResolution{
			InputID:   "steer-stale",
			AttemptID: "att-stale",
			Receipt:   agent.NativeInputReceipt{SessionID: "/s/old.jsonl", InputID: "k-stale", TurnID: "k-stale"},
		})

		r.agent.Mu.Lock()
		r.agent.sessionFile = "/s/new.jsonl"
		r.agent.sessionID = "new-id"
		r.agent.Mu.Unlock()

		assert.False(t, r.agent.observeOMPUserMessage("k-stale"))
		assert.Empty(t, r.sink.SteerResolutions())
		r.agent.Mu.Lock()
		_, stillTracked := r.agent.identifiedSteers["k-stale"]
		r.agent.Mu.Unlock()
		assert.True(t, stillTracked)
	})

	t.Run("session-handle alignment when replyOutput is active", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.agent.Mu.Lock()
		r.agent.sessionFile = "/s/active.jsonl"
		r.agent.sessionID = "active-id"
		r.agent.Mu.Unlock()

		lead, err := r.agent.SendInput("Lead prompt.", nil)
		require.NoError(t, err)
		r.emit(`{"type":"agent_start"}`)

		// Register steer under a.sessionID (pre-transition handle) while replyOutput uses sessionFile.
		r.agent.registerOMPSteer(agent.SteerResolution{
			InputID:   "steer-align",
			AttemptID: "att-align",
			Receipt:   agent.NativeInputReceipt{SessionID: "active-id", InputID: "k-align", TurnID: "k-align"},
		})

		assert.True(t, r.agent.observeOMPUserMessage("k-align"))
		assert.Equal(t, []agent.SteerResolution{{
			InputID:   "steer-align",
			AttemptID: "att-align",
			Outcome:   agent.SteerOutcomeEchoed,
			Receipt:   agent.NativeInputReceipt{SessionID: "/s/active.jsonl", InputID: "k-align", TurnID: lead.TurnID},
		}}, r.sink.SteerResolutions())
	})

	t.Run("closes completed reply on next user message", func(t *testing.T) {
		t.Parallel()
		for _, stopReason := range []string{"stop", "length"} {
			for _, unproved := range []bool{false, true} {
				name := fmt.Sprintf("stopReason=%s/unproved=%t", stopReason, unproved)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					r := newRig(t)
					lead, err := r.agent.SendInput("First prompt.", nil)
					require.NoError(t, err)
					r.emit(
						`{"type":"agent_start"}`,
						`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"First answer."}]}}`,
						fmt.Sprintf(`{"type":"turn_end","message":{"role":"assistant","stopReason":%q}}`, stopReason),
					)
					if unproved {
						r.agent.Mu.Lock()
						require.NotNil(t, r.agent.replyOutput)
						r.agent.replyOutput.unproved = true
						r.agent.Mu.Unlock()
					}
					r.emit(`{"type":"message_end","message":{"role":"user","content":"Next user message"}}`)
					if unproved {
						assert.Empty(t, r.sink.NativeReplyTurns())
						assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
							lead: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
						}, r.sink.NativeReplyRetirements())
					} else {
						turns := r.sink.NativeReplyTurns()
						require.Len(t, turns, 1)
						assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
						assert.Equal(t, lead.TurnID, turns[0].TurnID)
						assert.Empty(t, r.sink.NativeReplyRetirements())
					}
				})
			}
		}
	})
}

func TestObserveOMPAssistantBoundaryMessageStartClearsStopReason(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.agent.SendInput("Lead prompt.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Part 1."}]}}`,
		`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`,
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_end","message":{"role":"user","content":"Injected user message"}}`,
	)
	assert.Empty(t, r.sink.NativeReplyTurns(), "assistant message_start must clear reply.stop so the next user message does not close the reply")
	r.agent.Mu.Lock()
	stillOpen := r.agent.replyOutput != nil
	r.agent.Mu.Unlock()
	assert.True(t, stillOpen)
}
