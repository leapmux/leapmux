package codex

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestResolveControlResponse_CodexApprovalPreservesTheResponse(t *testing.T) {
	t.Parallel()

	// Native command approval preserves the response bytes.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	res := codexProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`),
		ResponseContent: content,
	})

	assert.Equal(t, content, res.Content)
	assert.Equal(t, agent.PlanModeControlNone, res.PlanModeControl)
}

func TestResolveControlResponse_CodexPlanModePrompt(t *testing.T) {
	t.Parallel()

	// The synthesized plan-mode prompt frame carries no top-level method; its request.tool_name is
	// the pruned context, and the neutral allow/deny envelope is forwarded verbatim.
	content := []byte(`{"response":{"request_id":"plan-1","response":{"behavior":"allow"}}}`)
	res := codexProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
		ResponseContent: content,
		ToolName:        ToolNamePlanModePrompt,
	})

	assert.Equal(t, content, res.Content)
	assert.Equal(t, agent.PlanModeControlPrompt, res.PlanModeControl)
}

func TestCodexResolveControlResponse_PreservesButWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, codexProvider{})
}

func TestCodexResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, codexProvider{})
}
