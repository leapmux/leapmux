package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestResolveControlResponse_ClaudeSelfDisplayAndPlanMode(t *testing.T) {
	t.Parallel()

	res := claudeProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		ResponseContent: []byte(`{"type":"control_response","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
		ToolName:        ToolNameExitPlanMode,
	})

	assert.True(t, res.SelfDisplayed)
	assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
	// The tool name is all the frontend needs to render Claude's Approved / Rejected / feedback.
}

func TestClaudeResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	// Claude keys its context off ToolName, which is empty here.
	agenttest.AssertPreservesTheResponseWithoutARequest(t, claudeProvider{})
}
