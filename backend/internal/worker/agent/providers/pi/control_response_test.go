package pi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveControlResponse_PiPreservesTheResponse(t *testing.T) {
	t.Parallel()

	confirmed := true
	response, err := json.Marshal(map[string]interface{}{"confirmed": confirmed})
	require.NoError(t, err)

	res := piProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`{"method":"confirm"}`),
		ResponseContent: response,
	})

	assert.Equal(t, response, res.Content)
}

func TestPiResolveControlResponse_PreservesButWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, piProvider{})
}

func TestPiResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, piProvider{})
}

func TestPiNativeExtensionResponseDoesNotInferAdapterApprovalFromTheTitle(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"Allow a tool?", "MCP: probe wants to run write\n\nArguments:\n{}"} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			request, err := json.Marshal(map[string]any{
				"type": "extension_ui_request", "id": "native-permission", "method": "select",
				"title": title, "options": []string{"Allow once", "Allow for session", "Deny"},
			})
			require.NoError(t, err)
			response := []byte(`{"type":"extension_ui_response","id":"native-permission","value":"Deny"}`)
			result := (piProvider{}).ResolveControlResponse(agent.ControlResponseContext{
				RequestID: "native-permission", RequestPayload: request, ResponseContent: response,
			})
			assert.False(t, result.Withhold)
			assert.Equal(t, response, result.Content)
		})
	}
}
