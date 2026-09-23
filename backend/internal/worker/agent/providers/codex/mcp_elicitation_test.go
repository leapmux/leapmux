package codex

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

func TestMCPElicitationApprovalScope(t *testing.T) {
	request := json.RawMessage(`{"id":"001","method":"mcpServer/elicitation/request","params":{"_meta":{"codex_approval_kind":"mcp_tool_call","persist":["session"]}}}`)
	for _, scope := range []string{"session", "always", "unknown"} {
		reply, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": "001", "response": map[string]any{"action": "accept", "content": map[string]any{}, "_meta": map[string]string{"persist": scope}}}})
		require.NoError(t, err)
		result, claimed := providerkit.ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: reply}, contracts.MCPElicitationMethodCodex, codexMCPElicitationMeta)
		require.True(t, claimed)
		assert.Equal(t, scope != "session", result.Withhold)
		if !result.Withhold {
			assert.Contains(t, string(result.Content), `"_meta":{"persist":"session"}`)
		}
	}
	reply := []byte(`{"response":{"request_id":"001","response":{"action":"decline","content":{"private":"unused"},"_meta":{"persist":"always"}}}}`)
	result, claimed := providerkit.ResolveMCPElicitationResponse(agent.ControlResponseContext{RequestPayload: request, ResponseContent: reply}, contracts.MCPElicitationMethodCodex, codexMCPElicitationMeta)
	require.True(t, claimed)
	require.False(t, result.Withhold)
	assert.NotContains(t, string(result.Content), "private")
	assert.NotContains(t, string(result.Content), "_meta")
}

// TestCodexMCPElicitationMetaRequiresAToolApprovalKind pins the kind half of
// Codex's rule: an elicitation that is not a tool-call or tool-suggestion
// approval offers no scope, even when its request lists one.
func TestCodexMCPElicitationMetaRequiresAToolApprovalKind(t *testing.T) {
	for kind, want := range map[string]bool{
		contracts.MCPElicitationApprovalKindToolCall:       true,
		contracts.MCPElicitationApprovalKindToolSuggestion: true,
		"other_kind": false,
		"":           false,
	} {
		request := json.RawMessage(`{"params":{"_meta":{"codex_approval_kind":"` + kind + `","persist":["session","always"]}}}`)
		assert.Equal(t, want, codexMCPElicitationMeta(request, contracts.MCPElicitationApprovalScopeSession), "kind %q", kind)
	}
	assert.False(t, codexMCPElicitationMeta(json.RawMessage(`broken`), contracts.MCPElicitationApprovalScopeSession))
}

func TestCodexPublishesZeroIDMCPElicitation(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	raw := []byte(`{"method":"mcpServer/elicitation/request","id":0,"params":{"threadId":"thread","mode":"form","message":"Allow this tool?","requestedSchema":{"type":"object","properties":{}}}}`)
	agent.HandleOutput(raw)
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, "jsonrpc:0", sink.PublishedControls()[0].RequestID)
	assert.Equal(t, raw, sink.PublishedControls()[0].Payload)
}
