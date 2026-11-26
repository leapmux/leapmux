package muse

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseSteerReceiptUsesTheTurnThatReadsTheInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		echoTurn  string
		beforeAck bool
	}{
		{name: "the running turn reads the input before acknowledgment", echoTurn: "active-turn", beforeAck: true},
		{name: "a later turn reads the admitted input", echoTurn: "receiving-turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			const activeTurn = "active-turn"
			a.startTurn("session", activeTurn)
			var commandID string
			echo := func(session, item, command, turn, status string) {
				t.Helper()
				feed(t, a, contracts.MuseMethodItemCompleted, map[string]any{
					"sessionId": session,
					"item": map[string]any{
						"itemId": item, "kind": contracts.MuseItemKindUserMessage, "commandId": command,
						"turnId": turn, "revision": 1, "status": status, "text": "Update the checklist.",
					},
				})
			}
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodTurnSteer, method)
				var params struct {
					CommandID      string `json:"commandId"`
					ExpectedTurnID string `json:"expectedTurnId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				require.Equal(t, activeTurn, params.ExpectedTurnID)
				require.NotEmpty(t, params.CommandID)
				commandID = params.CommandID
				if tc.beforeAck {
					echo("session", "steer", commandID, tc.echoTurn, contracts.MuseItemStatusCompleted)
				}
				result, err := json.Marshal(turnResult{CommandID: commandID, Status: "accepted", TurnID: activeTurn})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: result}
			}})
			require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "Update the checklist.", InputID: "input", AttemptID: "attempt"}))
			if !tc.beforeAck {
				feed(t, a, contracts.MuseMethodTurnCompleted, turnCompletion{SessionID: "session", TurnID: activeTurn, Outcome: contracts.MuseTurnOutcomeCompleted})
				feed(t, a, methodTurnStarted, map[string]string{"sessionId": "session", "turnId": tc.echoTurn})
				echo("foreign", "foreign-user", commandID, tc.echoTurn, contracts.MuseItemStatusCompleted)
				echo("session", "unrelated-user", "other-command", tc.echoTurn, contracts.MuseItemStatusCompleted)
				echo("session", "unscoped-user", commandID, "", contracts.MuseItemStatusCompleted)
				echo("session", "unfinished-user", commandID, tc.echoTurn, contracts.MuseItemStatusInProgress)
				feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{
					ID: "retracted-user", Kind: contracts.MuseItemKindUserMessage, TurnID: &tc.echoTurn, CommandID: commandID,
					Revision: 2, Status: contracts.MuseItemStatusCompleted, Retracted: true,
				}})
				require.Empty(t, sink.SteerResolutions())
			}
			echo("session", "steer", commandID, tc.echoTurn, contracts.MuseItemStatusCompleted)
			echo("session", "steer", commandID, tc.echoTurn, contracts.MuseItemStatusCompleted)
			require.Equal(t, []agent.SteerResolution{{
				InputID: "input", AttemptID: "attempt", Outcome: agent.SteerOutcomeEchoed,
				Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: commandID, TurnID: tc.echoTurn},
			}}, sink.SteerResolutions())
		})
	}
}

func TestMuseDisposesOfAnUnechoedSteerAtRefusalOrHostExit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		refused bool
	}{
		{name: "a refused command cannot claim a later echo", refused: true},
		{name: "host exit leaves an accepted command uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			turn := "turn"
			a.startTurn("session", turn)
			var commandID string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodTurnSteer, method)
				var params struct {
					CommandID string `json:"commandId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				commandID = params.CommandID
				if tc.refused {
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"The input is invalid."}`)}
				}
				result, err := json.Marshal(turnResult{CommandID: commandID, Status: "accepted", TurnID: turn})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: result}
			}})
			err := agenttest.SteerInput(a, agent.SteerRequest{Content: "Revise the plan.", InputID: "input", AttemptID: "attempt"})
			var want []agent.SteerResolution
			if tc.refused {
				require.ErrorContains(t, err, "The input is invalid.")
			} else {
				require.NoError(t, err)
				a.retireHost(agent.MessageCompletionInterrupted)
				want = []agent.SteerResolution{{InputID: "input", AttemptID: "attempt", Outcome: agent.SteerOutcomeUncertain}}
			}
			require.NotEmpty(t, commandID)
			feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{
				ID: "late-user", Kind: contracts.MuseItemKindUserMessage, TurnID: &turn, CommandID: commandID,
				Revision: 1, Status: contracts.MuseItemStatusCompleted,
			}})
			assert.Equal(t, want, sink.SteerResolutions())
		})
	}
}

func TestMuseInputReceiptRetainsATurnThatCompletedBeforeTheAcknowledgment(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	const turnID = "native-turn"
	var commandID string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		assert.Equal(t, methodTurnStart, method)
		var params struct {
			CommandID string `json:"commandId"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		commandID = params.CommandID
		feed(t, a, methodTurnStarted, map[string]any{"sessionId": "session", "turnId": turnID})
		turn := turnID
		feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{
			ID: "reply", Kind: contracts.MuseItemKindAgentMessage, TurnID: &turn, Revision: 1, Status: contracts.MuseItemStatusCompleted, Text: "The exact reply.",
		}})
		feed(t, a, contracts.MuseMethodTurnCompleted, turnCompletion{SessionID: "session", TurnID: turnID, Outcome: contracts.MuseTurnOutcomeCompleted})
		result, err := json.Marshal(turnResult{CommandID: commandID, Status: "accepted", Disposition: museDispositionStarted, TurnID: turnID})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	receipt, err := a.SendInput("guide", nil)
	require.NoError(t, err)
	require.NotEmpty(t, commandID)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "session", InputID: commandID, TurnID: turnID}, receipt)
	assert.False(t, a.PublishTurnActive().Active, "a late acknowledgment leaves the completed turn idle")
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, turnID, rows[0].NativeTurnID)
	assert.Equal(t, turnID, rows[1].NativeTurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, rows[1].InputTurnOutcome)
}

func TestInputPartsRetainsTextAndImageBytes(t *testing.T) {
	t.Parallel()
	data := []byte{0, 1, 2, 255}
	parts, err := inputParts("", []*leapmuxv1.Attachment{{Filename: "proof.png", MimeType: "image/png", Data: data}})
	require.NoError(t, err)
	require.Len(t, parts, 2)
	require.NotNil(t, parts[0].Text)
	assert.Equal(t, "", *parts[0].Text)
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), parts[1].Base64Data)
	assert.Equal(t, "image/png", parts[1].MediaType)
	raw, err := json.Marshal(parts)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"text","text":""},{"type":"image","mediaType":"image/png","base64Data":"AAEC/w=="}]`, string(raw))
}

func TestInputPartsRejectsUnsupportedBinaryPayloads(t *testing.T) {
	t.Parallel()
	for _, media := range []string{"application/pdf", "application/octet-stream"} {
		_, err := inputParts("proof", []*leapmuxv1.Attachment{{Filename: "proof.bin", MimeType: media, Data: []byte{255, 1}}})
		require.Error(t, err)
	}
}

func TestMuseQueuedDispositionStartsNoTurn(t *testing.T) {
	for _, scenario := range []struct {
		disposition string
		started     bool
		want        bool
	}{
		{"queued", false, false}, {"started", false, true}, {"queued", true, true}, {"future", false, false},
	} {
		t.Run(fmt.Sprintf("%s/started=%t", scenario.disposition, scenario.started), func(t *testing.T) {
			a, sink := testAgent(t)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				assert.Equal(t, methodTurnStart, method)
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				data, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted", "turnId": "turn", "disposition": scenario.disposition, "startedNewTurn": scenario.started})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: data}
			}})
			require.NoError(t, agenttest.SendInput(a, "message", nil))
			if scenario.want {
				assert.Equal(t, "turn", a.sessions["session"].turnID)
				assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
			} else {
				assert.Empty(t, a.sessions["session"].turnID)
				assert.Empty(t, sink.TurnActiveCalls)
				feed(t, a, methodTurnStarted, map[string]any{"sessionId": "session", "turnId": "turn"})
				assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
			}
		})
	}
}

func TestMuseRetriedInputReusesTheCommandID(t *testing.T) {
	for _, repeatedFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeated-failure=%t", repeatedFailure), func(t *testing.T) {
			a, _ := testAgent(t)
			var ids []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
				var params struct {
					CommandID string `json:"commandId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				ids = append(ids, params.CommandID)
				if len(ids) == 1 || repeatedFailure && len(ids) == 2 {
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"commandId":"` + params.CommandID + `","status":"accepted","disposition":"queued","turnId":"native-turn"}`)}
			}})
			require.ErrorIs(t, agenttest.SendInput(a, "message", nil), agent.ErrDeliveryUncertain)
			if repeatedFailure {
				require.ErrorIs(t, agenttest.SendInput(a, "message", nil), agent.ErrDeliveryUncertain)
			}
			require.NoError(t, agenttest.SendInput(a, "message", nil))
			for _, id := range ids {
				assert.Equal(t, ids[0], id)
			}
			require.NoError(t, agenttest.SendInput(a, "message", nil))
			assert.NotEqual(t, ids[0], ids[len(ids)-1], "a successful input clears the uncertain command")
		})
	}
}

func TestMuseRetriedInputDoesNotReuseAnotherInputIdentity(t *testing.T) {
	for _, change := range []string{"text", "attachment", "session", "steer"} {
		t.Run(change, func(t *testing.T) {
			a, _ := testAgent(t)
			var ids []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
				var params struct {
					CommandID string `json:"commandId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				ids = append(ids, params.CommandID)
				if len(ids) == 1 {
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
				data, err := json.Marshal(map[string]any{"commandId": params.CommandID, "status": "accepted", "disposition": museDispositionQueued, "turnId": "native-turn"})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: data}
			}})
			require.ErrorIs(t, agenttest.SendInput(a, "message", nil), agent.ErrDeliveryUncertain)
			switch change {
			case "text":
				require.NoError(t, agenttest.SendInput(a, "different", nil))
			case "attachment":
				require.NoError(t, agenttest.SendInput(a, "message", []*leapmuxv1.Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("different")}}))
			case "session":
				a.sessionID = "replacement"
				require.NoError(t, agenttest.SendInput(a, "message", nil))
				a.sessionID = "session"
			case "steer":
				a.startTurn("session", "turn")
				require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "message"}))
				a.sessions["session"].turnID = ""
			}
			require.NoError(t, agenttest.SendInput(a, "message", nil))
			require.Len(t, ids, 3)
			assert.NotEqual(t, ids[0], ids[1])
			assert.NotEqual(t, ids[0], ids[2])
		})
	}
}

func TestMuseRetriedInputForgetsAConfirmedRefusal(t *testing.T) {
	a, _ := testAgent(t)
	var ids []string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params struct {
			CommandID string `json:"commandId"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		ids = append(ids, params.CommandID)
		if len(ids) == 1 {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"The input is invalid."}`)}
	}})
	require.ErrorIs(t, agenttest.SendInput(a, "message", nil), agent.ErrDeliveryUncertain)
	require.Error(t, agenttest.SendInput(a, "message", nil))
	require.Error(t, agenttest.SendInput(a, "message", nil))
	require.Len(t, ids, 3)
	assert.Equal(t, ids[0], ids[1])
	assert.NotEqual(t, ids[0], ids[2])
}

func TestMuseSteerReturnsNilWhenEchoPrecedesRPCFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		retire bool
		reply  agenttest.RPCReply
	}{
		{name: "uncertain acknowledgement after echo", reply: agenttest.RPCReply{Result: json.RawMessage(`{}`)}},
		{name: "rpc refusal after echo", reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"late failure"}`)}},
		{name: "host retire and rpc failure after echo", retire: true, reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"late failure"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			const activeTurn = "active-turn"
			a.startTurn("session", activeTurn)
			var commandID string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodTurnSteer, method)
				var params struct {
					CommandID string `json:"commandId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				commandID = params.CommandID
				feed(t, a, contracts.MuseMethodItemCompleted, map[string]any{
					"sessionId": "session",
					"item": map[string]any{
						"itemId": "steer-echo", "kind": contracts.MuseItemKindUserMessage, "commandId": commandID,
						"turnId": activeTurn, "revision": 1, "status": contracts.MuseItemStatusCompleted, "text": "Update.",
					},
				})
				if tc.retire {
					a.retireHost(agent.MessageCompletionInterrupted)
				}
				return tc.reply
			}})
			receipt, err := a.SteerInput(agent.SteerRequest{Content: "Update.", InputID: "input", AttemptID: "attempt"})
			require.NoError(t, err)
			assert.Empty(t, receipt)
			assert.Empty(t, a.inFlightSteers)
			require.Equal(t, []agent.SteerResolution{{
				InputID: "input", AttemptID: "attempt", Outcome: agent.SteerOutcomeEchoed,
				Receipt: agent.NativeInputReceipt{SessionID: "session", InputID: commandID, TurnID: activeTurn},
			}}, sink.SteerResolutions())
		})
	}
}

func TestMuseSteerStillFailsWhenHostRetiresUnechoedBeforeRPCFailure(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	a.startTurn("session", "active-turn")
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodTurnSteer, method)
		a.retireHost(agent.MessageCompletionInterrupted)
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	_, err := a.SteerInput(agent.SteerRequest{Content: "Update.", InputID: "input", AttemptID: "attempt"})
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, a.inFlightSteers)
	require.Equal(t, []agent.SteerResolution{{
		InputID: "input", AttemptID: "attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions())
}

func TestMuseInputTurnOutcomeMapsFinalStatuses(t *testing.T) {
	t.Parallel()
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, museInputTurnOutcome(contracts.MuseTurnOutcomeCompleted))
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, museInputTurnOutcome(contracts.MuseTurnOutcomeCancelled))
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, museInputTurnOutcome(contracts.MuseTurnOutcomeFailed))
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, museInputTurnOutcome(""))
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, museInputTurnOutcome("unknown"))
}
