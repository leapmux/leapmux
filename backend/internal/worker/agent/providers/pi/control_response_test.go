package pi

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pi reads its own dialog answer, so the resolution forwards the answer that
// AnswerControlRequest builds as it is.
func TestResolveControlResponse_PiPreservesTheResponse(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"extension_ui_request","id":"dlg-1","method":"confirm","title":"Continue?"}`)
	answer, err := agenttest.AnswerControl(piProvider{}, payload, "dlg-1", agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"extension_ui_response","id":"dlg-1","confirmed":true}`, string(answer.Content))

	res := agenttest.ResolveAnswer(piProvider{}, payload, "dlg-1", answer)

	assert.Equal(t, answer.Content, res.Content)
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
			result := agenttest.ResolveAnswer(piProvider{}, request, "native-permission", agent.ControlAnswer{Content: response})
			assert.False(t, result.Withhold)
			assert.Equal(t, response, result.Content)
		})
	}
}
