package reasonix

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
