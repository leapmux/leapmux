package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func openCodeInputReplyFixture(t *testing.T, messages func(query url.Values, inputID string) string) (*Agent, *agenttest.ControlSink, agent.NativeInputReceipt) {
	t.Helper()
	var submitted atomic.Value
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_1/prompt":
			var prompt openCodePrompt
			assert.NoError(t, json.NewDecoder(request.Body).Decode(&prompt))
			submitted.Store(prompt.ID)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"sessionID":"ses_1","type":"user"}}`, prompt.ID)
		case "/api/session/ses_1/message":
			_, _ = fmt.Fprint(w, messages(request.URL.Query(), submitted.Load().(string)))
		case "/api/session/ses_1":
			_, _ = fmt.Fprint(w, `{"data":{"id":"ses_1","agent":"build"}}`)
		case "/api/session/active":
			_, _ = fmt.Fprint(w, `{"data":{}}`)
		case "/api/session/ses_1/permission", "/api/session/ses_1/form":
			_, _ = fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected OpenCode request %s %s", request.Method, request.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	sender, ok := any(a).(agent.InputReceiptSender)
	require.True(t, ok, "the Worker needs the submitted native input identity")
	receipt, err := sender.SendInputWithReceipt("ordinary prompt", nil)
	require.NoError(t, err)
	require.Equal(t, "ses_1", receipt.SessionID)
	require.Equal(t, submitted.Load(), receipt.InputID)
	require.NotEmpty(t, receipt.InputID)
	return a, sink, receipt
}

func TestOpenCodeInputReplyUsesTheExactProjectedExecution(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "16", query.Get("limit"))
		if query.Get("cursor") == "" {
			assert.Equal(t, "desc", query.Get("order"))
			return `{"data":[
			{"id":"msg_future","type":"assistant","content":[{"type":"text","text":"a newer reply"}],"time":{"completed":12}},
			{"id":"msg_reply_end","type":"idle","outcome":"succeeded"}
			],"cursor":{"next":"older-messages"}}`
		}
		assert.Equal(t, "older-messages", query.Get("cursor"))
		assert.Empty(t, query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":"msg_answer","type":"assistant","content":[{"type":"text","text":"the exact reply"}],"time":{"completed":8}},
			{"id":%q,"type":"user"},
			{"id":"msg_previous_end","type":"idle","outcome":"succeeded"},
			{"id":"msg_previous","type":"assistant","content":[{"type":"text","text":"an old reply"}],"time":{"completed":2}}
		],"cursor":{}}`, inputID)
	})
	assert.Empty(t, sink.NativeReplyTurns(), "inbox admission alone is not a reply")

	end := []byte(`{"id":"evt_reply_end","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)
	require.NoError(t, a.acceptNativeEvent(t.Context(), end))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	turn := turns[0]
	assert.Equal(t, receipt.SessionID, turn.SessionID)
	assert.Equal(t, "msg_reply_end", turn.TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turn.Outcome)
	require.Len(t, turn.Inputs, 1)
	assert.Equal(t, receipt.InputID, turn.Inputs[0].InputID)
	require.Len(t, turn.Items, 1)
	assert.Equal(t, "opencode:msg_answer:text:0", turn.Items[0].MessageKey)
	assert.Greater(t, turn.Items[0].Ordinal, turn.Inputs[0].AfterOrdinal)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, turn.Items[0].MessageKey, rows[0].IdempotencyKey)
	assert.Contains(t, string(rows[0].Content), "the exact reply")
	assert.True(t, rows[1].TurnEnd)
}

func TestOpenCodeInputReplyRecoversItsNativeEndingAfterReconnect(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "asc", query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":%q,"type":"user"},
			{"id":"msg_answer","type":"assistant","content":[{"type":"text","text":"recovered reply"}],"time":{"completed":3}},
			{"id":"msg_done","type":"idle","outcome":"succeeded"},
			{"id":"msg_other_input","type":"user"},
			{"id":"msg_other","type":"assistant","content":[{"type":"text","text":"new answer"}],"time":{"completed":7}},
			{"id":"msg_other_done","type":"idle","outcome":"succeeded"}
		],"cursor":{}}`, inputID)
	})

	require.NoError(t, a.restoreRun(t.Context(), a.root))
	require.NoError(t, a.restoreRun(t.Context(), a.root))
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, "msg_done", turns[0].TurnID)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "opencode:msg_answer:text:0"}}, turns[0].Items)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestOpenCodeInputReplyKeepsEachBoundaryInTheSharedExecution(t *testing.T) {
	t.Parallel()
	for _, restore := range []bool{false, true} {
		mode := "execution end event"
		if restore {
			mode = "projected recovery without steer echo event"
		}
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var leadID string
			a, sink, lead := openCodeInputReplyFixture(t, func(query url.Values, steerID string) string {
				lead := fmt.Sprintf(`{"id":%q,"type":"user"}`, leadID)
				steer := fmt.Sprintf(`{"id":%q,"type":"user"}`, steerID)
				const first = `{"id":"msg_first","type":"assistant","content":[{"type":"text","text":"first reply"}],"time":{"completed":2}}`
				const second = `{"id":"msg_second","type":"assistant","content":[{"type":"text","text":"second reply"}],"time":{"completed":7}}`
				const ending = `{"id":"msg_done","type":"idle","outcome":"succeeded"}`
				if query.Get("order") == "asc" {
					return fmt.Sprintf(`{"data":[%s,%s,%s,%s,%s],"cursor":{}}`, lead, first, steer, second, ending)
				}
				assert.Equal(t, "desc", query.Get("order"))
				return fmt.Sprintf(`{"data":[%s,%s,%s,%s,%s],"cursor":{}}`, ending, second, steer, first, lead)
			})
			leadID = lead.InputID
			request := openCodeTestSteerRequest("queued-steer", "steer prompt")
			steer, err := a.SteerInputWithReceipt(request)
			require.NoError(t, err)
			require.NotEqual(t, lead.InputID, steer.InputID)
			if restore {
				require.NoError(t, a.restoreRun(t.Context(), a.root))
				require.NoError(t, a.restoreRun(t.Context(), a.root))
			} else {
				require.NoError(t, a.acceptNativeEvent(t.Context(), []byte(`{"id":"evt_done","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)))
			}
			assert.Equal(t, []agent.SteerResolution{{InputID: request.InputID, AttemptID: request.AttemptID, Outcome: agent.SteerOutcomeEchoed, Receipt: steer}}, sink.SteerResolutions())
			assert.Equal(t, []agent.NativeReplyTurn{{
				SessionID: lead.SessionID, TurnID: "msg_done", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs: []agent.NativeReplyBoundary{{InputID: lead.InputID, AfterOrdinal: 1}, {InputID: steer.InputID, AfterOrdinal: 2}},
				Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "opencode:msg_first:text:0"}, {Ordinal: 3, MessageKey: "opencode:msg_second:text:0"}},
			}}, sink.NativeReplyTurns())
			assert.Empty(t, sink.NativeReplyRetirements())
		})
	}
}

type openCodeReplyControlSink struct {
	agent.ProviderServices
	bindings []agent.NativeControlInputs
}

func (sink *openCodeReplyControlSink) SetNativeControlInputs(binding agent.NativeControlInputs) error {
	sink.bindings = append(sink.bindings, binding)
	return sink.ProviderServices.SetNativeControlInputs(binding)
}

func TestOpenCodeInputReplyDoesNotUseTheNextInputsExecutionResult(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "desc", query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":"msg_done","type":"idle","outcome":"succeeded"},
			{"id":"msg_other_answer","type":"assistant","content":[{"type":"text","text":"other input's reply"}],"time":{"completed":7}},
			{"id":"msg_other_input","type":"user"},
			{"id":%q,"type":"user"}
		],"cursor":{}}`, inputID)
	})
	end := []byte(`{"id":"evt_done","created":9,"type":"session.execution.succeeded","data":{"sessionID":"ses_1"}}`)
	require.NoError(t, a.acceptNativeEvent(t.Context(), end))
	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

func TestOpenCodeInputReplyBindsOnlyControlsFromItsOwnAssistant(t *testing.T) {
	t.Parallel()
	a, sink, receipt := openCodeInputReplyFixture(t, func(query url.Values, inputID string) string {
		assert.Equal(t, "desc", query.Get("order"))
		return fmt.Sprintf(`{"data":[
			{"id":"msg_owned","type":"assistant"},
			{"id":%q,"type":"user"},
			{"id":"msg_prior","type":"assistant"},
			{"id":"msg_prior_input","type":"user"}
		],"cursor":{}}`, inputID)
	})
	controlSink := &openCodeReplyControlSink{ProviderServices: a.root.sink}
	a.root.sink = controlSink

	for i, messageID := range []string{"msg_prior", "msg_owned"} {
		event := assemblyEvent(t, "permission.asked", map[string]any{
			"sessionID": receipt.SessionID, "id": fmt.Sprintf("per_%d", i), "action": "shell", "resources": []string{},
			"source": map[string]any{"type": "tool", "messageID": messageID, "id": "call"},
		})
		require.NoError(t, a.acceptNativeEvent(t.Context(), event))
	}
	controls := sink.PublishedControls()
	require.Len(t, controls, 2)
	assert.Equal(t, []agent.NativeControlInputs{{
		SessionID: receipt.SessionID, RequestID: controls[1].RequestID, InstanceID: controls[1].InstanceID,
		InputIDs: []string{receipt.InputID},
	}}, controlSink.bindings)
}
