//go:build unix

package goose

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGooseClearContextRefreshesFromSession(t *testing.T) {
	t.Parallel()

	a, _ := newGooseAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "claude-sonnet-4"},
				"modes":  {"currentModeId": "approve"}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("gpt-5.4")
	a.SetPermissionModeForTest("auto")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "claude-sonnet-4", a.ModelForTest())
	assert.Equal(t, "approve", a.PermissionModeForTest())

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "claude-sonnet-4", refresh.Model)
	assert.Equal(t, "approve", refresh.PermissionMode)
}
