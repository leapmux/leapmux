package pi

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

func TestPiExactReplyKeepsTheSamePromptAcrossNativeRetry(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session-file"
	setPiReplyWriter(t, a, func(id string) {
		a.HandleOutput(piReplyResponse(id, "started"))
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}],"willRetry":true}`))
		assert.Empty(t, sink.NativeReplyTurns(), "a retrying run preserves the original input")
		a.HandleOutput([]byte(`{"type":"agent_start"}`))
		a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Recovered reply."}]}}`))
		a.HandleOutput(piSuccessfulReplyEnd)
		assert.Empty(t, sink.NativeReplyTurns(), "the final low-level run still needs native settlement")
		a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	})
	receipt, err := a.SendInputWithReceipt("Original input.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.InputID)
	require.Equal(t, receipt.InputID, receipt.TurnID)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-file", TurnID: receipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:" + receipt.TurnID + ":2"}},
	}}, sink.NativeReplyTurns())
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestPiReplyAdmissionStaysBehindOlderBufferedEvents(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.sessionFile = "session"
	require.True(t, a.interruptOutput.begin(func() bool { return true }))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Older native reply."}]}}`))
	a.HandleOutput(piSuccessfulReplyEnd)
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	setPiReplyWriter(t, a, func(id string) {
		a.HandleOutput(piReplyResponse(id, "started"))
	})
	receipt, err := a.SendInputWithReceipt("New input.", nil)
	require.NoError(t, err)
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"New native reply."}]}}`))
	a.HandleOutput(piSuccessfulReplyEnd)
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	assert.Empty(t, sink.NativeReplyTurns())
	a.interruptOutput.finish(func() {}, a.dispatchPiOutput)
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, receipt.TurnID, turns[0].TurnID)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "pi-reply:" + receipt.TurnID + ":2"}}, turns[0].Items)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestPiExactReplyRequiresNativeAdmissionAndSettlement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(string) []byte
		settle   bool
		outcome  leapmuxv1.AgentInputTurnOutcome
	}{
		{
			name: "queued", response: func(id string) []byte { return piReplyResponse(id, "queued") }, settle: true,
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
		{
			name: "refused",
			response: func(id string) []byte {
				return []byte(fmt.Sprintf(`{"id":%q,"type":"response","command":"prompt","success":false,"error":"No model."}`, id))
			},
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		},
		{
			name: "process loss", response: func(id string) []byte { return piReplyResponse(id, "started") },
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			a.sessionFile = "session"
			setPiReplyWriter(t, a, func(id string) {
				a.HandleOutput(tc.response(id))
			})
			receipt, err := a.SendInputWithReceipt("One input.", nil)
			require.NoError(t, err)
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			a.HandleOutput(piSuccessfulReplyEnd)
			if tc.settle {
				a.HandleOutput([]byte(`{"type":"agent_settled"}`))
			} else {
				a.settleHeldPiTurn(agent.MessageCompletionInterrupted)
			}
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, sink.NativeReplyRetirements())
		})
	}
}

func TestPiExactReplyReportsNativeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		disposition string
		events      []string
		outcome     leapmuxv1.AgentInputTurnOutcome
		unproved    bool
	}{
		{
			name: "handled without a model run", disposition: "handled",
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		},
		{
			name: "native interruption", disposition: "started",
			events: []string{
				`{"type":"agent_start"}`,
				`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted"}]}`,
				`{"type":"agent_settled"}`,
			},
			outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED,
		},
		{
			name: "uncommitted text", disposition: "started",
			events: []string{
				`{"type":"agent_start"}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"An uncommitted prefix."}}`,
				string(piSuccessfulReplyEnd),
				`{"type":"agent_settled"}`,
			},
			outcome:  leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			unproved: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newPiAgentWithSink(agent.NewProviderServices(sink))
			setPiReplyWriter(t, a, func(id string) { a.HandleOutput(piReplyResponse(id, tc.disposition)) })
			receipt, err := a.SendInputWithReceipt("One input.", nil)
			require.NoError(t, err)
			for _, event := range tc.events {
				a.HandleOutput([]byte(event))
			}
			if tc.unproved {
				assert.Empty(t, sink.NativeReplyTurns())
				assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, sink.NativeReplyRetirements())
				return
			}
			assert.Equal(t, []agent.NativeReplyTurn{{
				SessionID: receipt.SessionID, TurnID: receipt.TurnID, Outcome: tc.outcome,
				Inputs: []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
			}}, sink.NativeReplyTurns())
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

var piSuccessfulReplyEnd = []byte(`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}],"willRetry":false}`)

func setPiReplyWriter(t *testing.T, a *Agent, prompt func(string)) {
	t.Helper()
	setPiInterruptWriter(a, func(data []byte) (int, error) {
		var command struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &command); err != nil {
			return 0, err
		}
		if command.Type == CommandPrompt {
			require.NotEmpty(t, command.ID)
			prompt(command.ID)
		} else {
			a.HandleOutput([]byte(fmt.Sprintf(`{"id":%q,"type":"response","command":%q,"success":true,"data":{}}`, command.ID, command.Type)))
		}
		return len(data), nil
	})
}

func piReplyResponse(id, disposition string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"type":"response","command":"prompt","success":true,"data":{"disposition":%q}}`, id, disposition))
}
