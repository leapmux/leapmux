package opencode

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/require"
)

func TestACPBufferedPrimaryAgentCatalogControlsTheRequestedSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sessionID string
		options   string
		wantRPC   bool
	}{
		{"advertised during startup", "session-1", `[{"value":"build"},{"value":"plan"}]`, true},
		{"identical to fallback", "session-1", `[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]`, true},
		{"requested option absent", "session-1", `[{"value":"plan"}]`, false},
		{"another session", "previous-session", `[{"value":"build"},{"value":"plan"}]`, false},
		{"no advertised catalog", "session-1", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag, requests := newOpenCodeAgentForRPC(t)
			ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
			ag.BeginSessionUpdatesForTest()
			ag.HandleSessionUpdateForTest(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":%s}]}}`, tc.sessionID, tc.options)))
			fallback := []*leapmuxv1.AvailableOption{{Id: "build", Name: "Build"}, {Id: "plan", Name: "Plan"}}
			require.NoError(t, ag.ConfigurePrimaryAgentsForTest(nil, "plan", "build", fallback, "build"))
			if tc.wantRPC {
				require.Equal(t, "build", ag.CurrentPrimaryAgentForTest())
				recorded := requests()
				require.Len(t, recorded, 1)
				require.Equal(t, acp.MethodSessionSetMode, recorded[0].Method)
				require.Equal(t, "build", recorded[0].Params["modeId"])
			} else {
				require.Empty(t, requests(), "a fallback or another session cannot authorize a requested mode")
			}
		})
	}
}

func TestACPBufferedPrimaryAgentUpdatePrecedesTheRequestedSelection(t *testing.T) {
	a, requests := newOpenCodeAgentForRPC(t)
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.BeginSessionUpdatesForTest()
	a.HandleSessionUpdateForTest(json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}`, a.CurrentSessionID())))
	require.NoError(t, a.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: "build", Name: "Build"}, {ID: "plan", Name: "Plan"},
	}, "build", "build", nil, "build"))
	require.Equal(t, "build", a.CurrentPrimaryAgentForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acp.MethodSessionSetMode, recorded[0].Method)
	require.Equal(t, "build", recorded[0].Params["modeId"])
}

func TestACPAdvertisedSteeringKeepsItsSessionUntilTheReply(t *testing.T) {
	var agent *Agent
	replaced := false
	agent, _ = newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == "_provider/session/steer" && agent.SessionMuForTest().TryLock() {
			agent.Mu.Lock()
			agent.SetSessionIDForTest("replacement-session")
			agent.Mu.Unlock()
			agent.SessionMuForTest().Unlock()
			replaced = true
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	agent.SetSteerMethodForTest("_provider/session/steer")
	agent.SetPromptActiveForTest(true)
	require.NoError(t, agent.SteerAdvertised("Use the existing session.", nil))
	require.False(t, replaced, "session replacement must wait for the steering reply")
	require.Equal(t, "session-1", agent.CurrentSessionID())
}
