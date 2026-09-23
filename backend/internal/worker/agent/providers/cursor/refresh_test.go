//go:build unix

package cursor

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorClearContextRefreshesWithNormalization(t *testing.T) {
	t.Parallel()

	a, _ := newCursorAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			// Cursor returns the wire format "default[]" for auto model.
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "default[]"},
				"modes":  {"currentModeId": "agent"}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("some-model")
	a.SetPermissionModeForTest("plan")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "auto", a.ModelForTest())
	assert.Equal(t, "agent", a.PermissionModeForTest())

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "auto", refresh.Model)
	assert.Equal(t, "agent", refresh.PermissionMode)
}

// Cursor (a permission-mode provider) carries a surfaced config option through its
// ClearContext refresh. Regression guard for the parity fix that routed Cursor's
// session refresh through the shared extras merge (applySessionRefresh) instead of
// passing nil extras -- previously a Cursor option would be dropped on a context clear.
func TestCursorClearContextRefreshesOptions(t *testing.T) {
	t.Parallel()

	a, _ := newCursorAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "default[]"},
				"modes":  {"currentModeId": "agent"},
				"configOptions": [
					{"id":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]},
					{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}
				]
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("some-model")
	a.SetPermissionModeForTest("plan")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The option group is surfaced after the mapped permission-mode group.
	groups := a.OptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, agent.OptionIDPermissionMode, groups[0].GetId())
	assert.Equal(t, "thoughtLevel", groups[1].GetId())

	// The refresh now carries the option value (previously dropped: Cursor passed nil).
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "auto", refresh.Model)
	assert.Equal(t, "agent", refresh.PermissionMode)
	assert.Equal(t, "high", refresh.Options["thoughtLevel"])
}
