package ohmypi

import (
	"encoding/json"
	"fmt"
	"testing"

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
			require.NoError(t, r.agent.SteerInput("Guide the next answer.", nil))
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
	require.NoError(t, r.agent.SteerInput("/status", nil))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerFailedAfterAckIsDroppedInOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.SteerInput("first", nil))
	require.NoError(t, r.agent.SteerInput("second", nil))
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
	require.NoError(t, r.agent.SteerInput("/status", nil))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, r.sink.SteerEchoReports())
}

func TestSteerMalformedPromptResultDoesNotClaimAnEcho(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.SteerInput("guide", nil))
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
	require.NoError(t, r.agent.SteerInput("late steer", nil))
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
	require.Error(t, r.agent.SteerInput("refused", nil))
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
			require.NoError(t, r.agent.SteerInput("pending", nil))
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
		{raw: `" guide "`, text: " guide ", valid: true},
		{raw: `[{"type":"text","text":"one"},{"type":"text","text":"two"}]`, text: "one\ntwo", valid: true},
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
