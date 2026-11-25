package mimo

import (
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func receiptEvent(t *testing.T, id, input, delivery, state, outcome string) []byte {
	t.Helper()
	return eventJSON(t, "session.receipt.updated", map[string]any{
		"receiptId": id, "inputMessageID": input, "deliveryMessageID": delivery,
		"sessionID": testSessionID, "agentID": mainActorID, "state": state, "outcome": outcome,
	})
}

func TestMiMoReplyUsesOnlyRetainedTextAfterDelivery(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("POST /session/ses_test/prompt_async", http.StatusAccepted, `{"receiptId":"owned"}`)
	_, err := a.SendInput("guide", nil)
	require.NoError(t, err)
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_01", roleAssistant, mainActorID, true),
		textPartEvent(t, partTypeText, "before", "msg_01", "before the delivery", true),
		messageEvent(t, "msg_03", roleAssistant, mainActorID, true),
		receiptEvent(t, "owned", "msg_02", "msg_03", "claimed", ""),
		textPartEvent(t, partTypeText, "first", "msg_03", "first exact answer", true),
		textPartEvent(t, partTypeText, "first", "msg_03", "first exact answer", true),
		textPartEvent(t, partTypeText, "retract", "msg_03", "retracted text", true),
		eventJSON(t, eventMessagePartRemoved, map[string]string{"sessionID": testSessionID, "messageID": "msg_03", "partID": "retract"}),
		messageEvent(t, "msg_04", roleAssistant, "child", true),
		textPartEvent(t, partTypeText, "child-only", "msg_04", "a child answer", true),
		messageEvent(t, "msg_05", roleAssistant, mainActorID, true),
		textPartEvent(t, partTypeText, "last", "msg_05", "last exact answer", true),
		receiptEvent(t, "owned", "msg_other", "msg_03", "settled", "success"),
	)
	assert.Empty(t, sink.NativeReplyTurns(), "an update for a different input cannot settle the receipt")
	feed(a, receiptEvent(t, "owned", "msg_02", "msg_03", "settled", "success"))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, []agent.NativeReplyItem{
		{Ordinal: 2, MessageKey: "mimo-part:first:text"},
		{Ordinal: 4, MessageKey: "mimo-part:last:text"},
	}, turns[0].Items)
}

func TestMiMoReceiptOutcomeWithoutReplyEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		state   string
		outcome string
		want    leapmuxv1.AgentInputTurnOutcome
	}{
		{"no delivery event", "settled", "success", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
		{"cancelled before delivery", "cancelled", "never_ran", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED},
		{"assistant failed", "settled", "assistant_error", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			server.respond("POST /session/ses_test/prompt_async", http.StatusAccepted, `{"receiptId":"owned"}`)
			receipt, err := a.SendInput("guide", nil)
			require.NoError(t, err)
			feed(a, messageEvent(t, "msg_02", roleAssistant, mainActorID, true),
				receiptEvent(t, "owned", "msg_01", "msg_02", tc.state, tc.outcome))
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.want}, sink.NativeReplyRetirements())
		})
	}
}

func TestMiMoAcknowledgmentDistinguishesAbsentAndInvalidReceipts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"no turn admitted", http.StatusNoContent, ""},
		{"missing receipt", http.StatusAccepted, `{}`},
		{"null acknowledgment", http.StatusAccepted, `null`},
		{"invalid identity", http.StatusAccepted, `{"receiptId":"../other"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, server := newSinkTestAgent(t)
			server.respond("POST /session/ses_test/prompt_async", tc.status, tc.body)
			receipt, err := a.SendInput("guide", nil)
			if tc.status == http.StatusNoContent {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			assert.Empty(t, receipt)
		})
	}
}

func TestMiMoReconnectedStreamCannotCompleteAnIncompleteReply(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.connected = make(chan struct{})
	a.onStreamConnected(t.Context())
	server.respond("POST /session/ses_test/prompt_async", http.StatusAccepted, `{"receiptId":"owned"}`)
	receipt, err := a.SendInput("guide", nil)
	require.NoError(t, err)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_02", roleAssistant, mainActorID, false),
		receiptEvent(t, "owned", "msg_01", "msg_02", "claimed", ""))
	a.onStreamConnected(t.Context())
	feed(a, messageEvent(t, "msg_02", roleAssistant, mainActorID, true),
		receiptEvent(t, "owned", "msg_01", "msg_02", "settled", "success"))
	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}

type mimoReplyControlSink struct {
	agenttest.ControlSink
	bindings []agent.NativeControlInputs
}

func (s *mimoReplyControlSink) SetNativeControlInputs(binding agent.NativeControlInputs) error {
	s.bindings = append(s.bindings, binding)
	return nil
}

func TestMiMoHTTPReceiptBindsAnAlreadyPublishedNativeQuestion(t *testing.T) {
	t.Parallel()
	sink := &mimoReplyControlSink{}
	a, server := newTestAgent(t, agent.NewProviderServices(sink))
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
			messageEvent(t, "msg_02", roleAssistant, mainActorID, false),
			receiptEvent(t, "owned", "msg_01", "msg_02", "claimed", ""),
			[]byte(`{"type":"question.asked","properties":{"id":"question-exact","sessionID":"ses_test","questions":[{"header":"Choice","question":"Select a checklist","options":[{"label":"First","description":"The first checklist"}]}],"tool":{"messageID":"msg_02","callID":"call-question"}}}`),
		)
		assert.Empty(t, sink.bindings)
		writeJSON(w, http.StatusAccepted, `{"receiptId":"owned"}`)
	})
	_, err := a.SendInput("guide", nil)
	require.NoError(t, err)
	require.Len(t, sink.bindings, 1)
	assert.Equal(t, []string{"owned"}, sink.bindings[0].InputIDs)
	assert.Equal(t, testSessionID, sink.bindings[0].SessionID)
	assert.NotEmpty(t, sink.bindings[0].InstanceID)
}

func TestMiMoReplySettlesBeforeItsHTTPAcknowledgment(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
			messageEvent(t, "msg_02", roleAssistant, mainActorID, false),
			receiptEvent(t, "foreign", "msg_01", "msg_02", "claimed", ""),
			textPartEvent(t, partTypeText, "foreign-text", "msg_02", "an unrelated reply", true),
			messageEvent(t, "msg_02", roleAssistant, mainActorID, true),
			receiptEvent(t, "foreign", "msg_01", "msg_02", "settled", "success"),
			messageEvent(t, "msg_04", roleAssistant, mainActorID, false),
			receiptEvent(t, "owned", "msg_03", "msg_04", "claimed", ""),
			textPartEvent(t, partTypeText, "owned-text", "msg_04", "the requested reply", true),
			messageEvent(t, "msg_04", roleAssistant, mainActorID, true),
			receiptEvent(t, "owned", "msg_03", "msg_04", "settled", "success"),
			statusEvent(t, contracts.MiMoStatusTypeIdle),
		)
		assert.Empty(t, sink.NativeReplyTurns(), "no HTTP call has acknowledged a receipt yet")
		writeJSON(w, http.StatusAccepted, `{"receiptId":"owned"}`)
	})
	receipt, err := a.SendInput("guide", nil)
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: testSessionID, InputID: "owned", TurnID: "owned"}, receipt)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: testSessionID, TurnID: "owned", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: "owned", AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "mimo-part:owned-text:text"}},
	}}, sink.NativeReplyTurns())
	feed(a, receiptEvent(t, "owned", "msg_03", "msg_04", "settled", "success"))
	assert.Len(t, sink.NativeReplyTurns(), 1)
	assert.Empty(t, sink.NativeReplyRetirements())
}
