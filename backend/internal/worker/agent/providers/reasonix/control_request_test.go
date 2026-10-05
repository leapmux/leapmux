package reasonix

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controlOrderSink records the assembled texts that the transcript holds at the
// moment when each control request reaches the reader.
type controlOrderSink struct {
	agenttest.ControlSink
	textsAtControl [][]string
}

func (s *controlOrderSink) PublishControlRequest(request agent.ControlRequest) error {
	s.textsAtControl = append(s.textsAtControl, assembledTexts(s.Messages()))
	return s.ControlSink.PublishControlRequest(request)
}

// assembledTexts returns the text of each assembled message, in order, with a
// prefix that gives its kind.
func assembledTexts(messages []agenttest.Message) []string {
	var texts []string
	for _, message := range messages {
		var envelope map[string]string
		if json.Unmarshal(message.Content, &envelope) != nil || envelope[contracts.AssembledMessageFieldType] != contracts.AssembledMessageType {
			continue
		}
		texts = append(texts, envelope[contracts.AssembledMessageFieldKind]+":"+envelope[contracts.AssembledMessageFieldText])
	}
	return texts
}

// Reasonix 1.38.7 streams the plan of Plan mode as the answer, and then asks to
// leave Plan mode with a session/request_permission that no tool_call precedes
// (internal/control/turn_orchestrator.go executeApprovedPlan: "The plan is
// already visible as the assistant's answer, so the request carries no
// subject"). The prompt stays open until the reader answers. The params of the
// request are the ones that the installed CLI sent to a probe client in front of
// a local mock model.
//
// The plan must therefore be in the transcript when the request reaches the
// reader. Otherwise the reader must approve or reject a plan that no row shows.
func TestReasonixStoresThePlanBeforeItsExitPlanModeRequest(t *testing.T) {
	sink := &controlOrderSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("plan-session")

	for _, frame := range []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"plan-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Plan the inspection."}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"plan-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"1. Inspect. "}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"plan-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"2. Report."}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"plan-session","toolCall":{"toolCallId":"gate-1","title":"exit_plan_mode","kind":"other","status":"pending","_meta":{"reasonix.io":{"approvalId":"1","fresh":false,"subject":"","tool":"exit_plan_mode"}}},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"},{"optionId":"reject_once","name":"Reject","kind":"reject_once"}]}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	require.Equal(t, 1, sink.PublishedControlCount(), "the exit_plan_mode request reaches the reader")
	require.Len(t, sink.textsAtControl, 1)
	assert.Equal(t, []string{"reasoning:Plan the inspection.", "text:1. Inspect. 2. Report."}, sink.textsAtControl[0],
		"the thought and the plan are in the transcript when the request reaches the reader")
	assert.Equal(t, sink.textsAtControl[0], assembledTexts(sink.Messages()), "the request stores no text a second time")
}

func TestReasonixControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("test-session")
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, func(content []byte) {
		require.True(t, a.handleExtraMethod(providerkit.ParseLine(content)))
	}, "_reasonix.io/mcp/request_interaction")
}

func TestReasonixAnswersNativeMCPInteraction(t *testing.T) {
	t.Parallel()
	request := json.RawMessage(`{"jsonrpc":"2.0","id":"001","method":"_reasonix.io/mcp/request_interaction","params":{"mode":"form"}}`)
	response := json.RawMessage(`{"response":{"request_id":"001","response":{"action":"accept","content":{"count":0,"enabled":false}}}}`)
	resolved := reasonixProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: response})
	require.False(t, resolved.Withhold)
	var reply struct {
		ID     string `json:"id"`
		Result struct {
			Action  string         `json:"action"`
			Content map[string]any `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolved.Content, &reply))
	assert.Equal(t, "001", reply.ID)
	assert.Equal(t, "accept", reply.Result.Action)
	assert.Equal(t, map[string]any{"count": float64(0), "enabled": false}, reply.Result.Content)
}

func TestReasonixWithholdsInvalidMCPAnswers(t *testing.T) {
	t.Parallel()
	request := json.RawMessage(`{"jsonrpc":"2.0","id":7,"method":"_reasonix.io/mcp/request_interaction"}`)
	for _, response := range []string{
		`{}`,
		`{"response":{"request_id":"other","response":{"action":"accept"}}}`,
		`{"response":{"request_id":"7","response":{"action":"unknown"}}}`,
		`{"response":{"request_id":"7","response":{"action":"accept","_meta":{"persist":"session"}}}}`,
	} {
		resolved := reasonixProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: []byte(response)})
		assert.True(t, resolved.Withhold, response)
	}
}

func TestReasonixDeclineRemovesFormContent(t *testing.T) {
	t.Parallel()
	request := json.RawMessage(`{"jsonrpc":"2.0","id":7,"method":"_reasonix.io/mcp/request_interaction"}`)
	response := json.RawMessage(`{"response":{"request_id":"7","response":{"action":"decline","content":{"count":0}}}}`)
	resolved := reasonixProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: response})
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"decline"}}`, string(resolved.Content))
}
