package droid

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The service forwards the resolved bytes to the agent's stdin. Droid reads a
// JSON-RPC RESPONSE envelope there, keyed by the id of the request it answers.
// A bare result body, or an envelope with the wrong id, leaves the call hanging.
func TestResolveControlResponseWritesAResponseEnvelope(t *testing.T) {
	t.Parallel()
	request, _ := json.Marshal(map[string]any{
		"type":      "permission_request",
		"requestId": "droid-perm-1",
		"rpcId":     "rpc-42",
		"toolUse":   map[string]any{"type": "tool_use", "id": "c1", "name": "Edit", "input": map[string]any{"file_path": "x"}},
	})
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "droid-perm-1",
			"response":   map[string]any{"behavior": "allow"},
		},
	})
	resolution := droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-perm-1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	require.False(t, resolution.Withhold)
	require.NotEmpty(t, resolution.Content)

	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	assert.Equal(t, "2.0", envelope.JSONRPC, "the reply is a JSON-RPC envelope")
	assert.Equal(t, "response", envelope.Type)
	assert.Equal(t, "rpc-42", envelope.ID, "the reply answers the request's own id")

	var result map[string]string
	require.NoError(t, json.Unmarshal(envelope.Result, &result))
	assert.Equal(t, "proceed_once", result["selectedOption"])
}

func TestResolveControlResponseAnswersAnAskUser(t *testing.T) {
	t.Parallel()
	request, _ := json.Marshal(map[string]any{
		"type":       "ask_user_request",
		"requestId":  "droid-ask-1",
		"rpcId":      "rpc-43",
		"toolCallId": "c2",
	})
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "droid-ask-1",
			"response":   map[string]any{"behavior": "allow", "answers": map[string]string{"Color?": "Blue"}},
		},
	})
	resolution := droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-ask-1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	require.False(t, resolution.Withhold)

	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	assert.Equal(t, "rpc-43", envelope.ID)

	var result struct {
		Cancelled bool `json:"cancelled"`
		Answers   []struct {
			Index    int    `json:"index"`
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"answers"`
	}
	require.NoError(t, json.Unmarshal(envelope.Result, &result))
	assert.False(t, result.Cancelled)
	require.Len(t, result.Answers, 1)
	assert.Equal(t, "Blue", result.Answers[0].Answer)
}
