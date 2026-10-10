package claude

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// A session-scoped allow carries the remembered grant the reader chose: one
// allow rule for the approved tool, with a session destination, appended to the
// reply's `updatedPermissions`. An allow without the session choice states no
// grant, so the once answer stays bare.
func TestClaudeResolveControlResponse_SessionChoiceAppendsTheSessionGrant(t *testing.T) {
	t.Parallel()

	res := claudeProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		ResponseContent: []byte(`{"type":"control_response","response":{"request_id":"req-1","response":{"behavior":"allow","choice":"session"}}}`),
		ToolName:        "Bash",
	})
	assert.False(t, res.Withhold, "the session grant must not be refused")
	assert.JSONEq(t, `[{"type":"addRules","rules":[{"toolName":"Bash"}],"behavior":"allow","destination":"session"}]`,
		string(extractUpdatedPermissions(t, res.Content)))

	res = claudeProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		ResponseContent: []byte(`{"type":"control_response","response":{"request_id":"req-1","response":{"behavior":"allow"}}}`),
		ToolName:        "Bash",
	})
	assert.Empty(t, string(extractUpdatedPermissions(t, res.Content)),
		"the once answer carries no permission update")
}

func extractUpdatedPermissions(t *testing.T, content []byte) json.RawMessage {
	t.Helper()
	var root struct {
		Response struct {
			Response struct {
				UpdatedPermissions json.RawMessage `json:"updatedPermissions"`
			} `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &root))
	return root.Response.Response.UpdatedPermissions
}
