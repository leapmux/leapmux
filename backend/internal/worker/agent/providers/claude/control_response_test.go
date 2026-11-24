package claude

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveControlResponse_ClaudeSelfDisplayAndPlanMode(t *testing.T) {
	t.Parallel()

	payload := controlPayload(t, ToolNameExitPlanMode, map[string]any{"plan": "1. Edit."})
	answer, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	require.NoError(t, err)
	res := agenttest.ResolveAnswer(claudeProvider{}, payload, claudeTestRequestID, answer)

	assert.True(t, res.SelfDisplayed)
	assert.Equal(t, agent.PlanModeControlExit, res.PlanModeControl)
	// The browser draws Claude's Approved / Rejected / feedback from the recorded prompt and the response.
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

	payload := controlPayload(t, "Bash", map[string]any{"command": "ls"})
	answer, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PermissionDecision(claudeOptionSession, ""))
	require.NoError(t, err)
	res := agenttest.ResolveAnswer(claudeProvider{}, payload, claudeTestRequestID, answer)
	assert.False(t, res.Withhold, "the session grant must not be refused")
	assert.JSONEq(t, `[{"type":"addRules","rules":[{"toolName":"Bash"}],"behavior":"allow","destination":"session"}]`,
		string(extractUpdatedPermissions(t, res.Content)))

	answer, err = agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PermissionDecision("once", ""))
	require.NoError(t, err)
	res = agenttest.ResolveAnswer(claudeProvider{}, payload, claudeTestRequestID, answer)
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
