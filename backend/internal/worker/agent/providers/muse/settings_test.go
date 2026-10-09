package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelAdmissionDoesNotClaimTheRequestedModel(t *testing.T) {
	a, _ := testAgent(t)
	reads := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		if method == methodSessionRead {
			reads++
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session","modelId":"native","activeTurnId":null},"history":{"mode":"none","items":null,"snapshot":null},"viewCursor":"head"}`)}
		}
		require.Equal(t, methodSetModel, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, a.applyOption(agent.OptionIDModel, "requested", time.Second))
	assert.Equal(t, "native", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDModel])
	assert.Equal(t, 1, reads)
}

func TestApprovalSettingsUseTheNativeEffectiveMode(t *testing.T) {
	a, _ := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSetApprovalMode, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted", "applyOutcome": "pinned", "effectiveMode": map[string]any{"mode": "denyUnmatched"}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, a.applyOption(agent.OptionIDPermissionMode, "allowAll", time.Second))
	assert.Equal(t, "denyUnmatched", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
}

func TestSettingsReadRejectsMalformedStateBeforeAnySettingChanges(t *testing.T) {
	a, _ := testAgent(t)
	a.settings[agent.OptionIDEffort] = "low"
	before := a.SettingsSnapshot().SurfacedOptions
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionRead, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session","modelId":"another-model"},"history":{"mode":"snapshot","snapshot":{"state":{"reasoningEffort":{"reasoningEffort":"invalid"}}}}}`)}
	}})
	require.Error(t, a.refreshNativeSettings("session", time.Second))
	assert.Equal(t, before, a.SettingsSnapshot().SurfacedOptions)
}

func TestApprovalSettingsRejectAnInvalidNativeEffectiveMode(t *testing.T) {
	a, _ := testAgent(t)
	a.settings[agent.OptionIDPermissionMode] = contracts.MuseApprovalModeOnRequest
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted", "effectiveMode": map[string]any{"mode": "invalid"}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.Error(t, a.applyOption(agent.OptionIDPermissionMode, contracts.MuseApprovalModeAllowAll, time.Second))
	assert.Equal(t, contracts.MuseApprovalModeOnRequest, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
}

func TestEffortAdmissionUsesConfirmedNativeStateAndReportsRefusal(t *testing.T) {
	for _, tc := range []struct {
		label   string
		refused bool
	}{{"applied", false}, {"refused", true}} {
		t.Run(tc.label, func(t *testing.T) {
			a, _ := testAgent(t)
			a.settings[agent.OptionIDEffort] = "low"
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				if method == methodSessionRead {
					return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session","modelId":"native"},"history":{"mode":"snapshot","snapshot":{"state":{"reasoningEffort":{"reasoningEffort":"high"}}}}}`)}
				}
				require.Equal(t, methodSetEffort, method)
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				assert.Equal(t, "high", params["reasoningEffort"])
				if tc.refused {
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"native effort refused"}`)}
				}
				result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: result}
			}})
			result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
			if tc.refused {
				assert.Equal(t, "low", result.SurfacedOptions[agent.OptionIDEffort])
				assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
			} else {
				assert.Equal(t, "high", result.SurfacedOptions[agent.OptionIDEffort])
				assert.NotEqual(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
			}
		})
	}
}

func TestSettingsReadCannotChangeAReplacementSession(t *testing.T) {
	a, _ := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionRead, method)
		a.stateMu.Lock()
		a.sessionID = "replacement"
		a.settings = optionmap.Map{agent.OptionIDModel: "replacement-model"}
		a.stateMu.Unlock()
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session","modelId":"old-model"}}`)}
	}})
	require.ErrorIs(t, a.refreshNativeSettings("session", time.Second), agent.ErrInputSessionChanged)
	assert.Equal(t, "replacement-model", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDModel])
}

func TestApprovalSettingsReplyCannotClaimAReplacementSession(t *testing.T) {
	a, _ := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSetApprovalMode, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		a.stateMu.Lock()
		a.sessionID = "replacement"
		a.settings = optionmap.Map{agent.OptionIDPermissionMode: contracts.MuseApprovalModeOnRequest}
		a.stateMu.Unlock()
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted", "effectiveMode": map[string]any{"mode": contracts.MuseApprovalModeAllowAll}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.ErrorIs(t, a.applyOption(agent.OptionIDPermissionMode, contracts.MuseApprovalModeAllowAll, time.Second), agent.ErrInputSessionChanged)
	assert.Equal(t, contracts.MuseApprovalModeOnRequest, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
}

func TestSettingsReadRejectsAnUnknownModeBeforeAnySettingChanges(t *testing.T) {
	a, _ := testAgent(t)
	a.settings[agent.OptionIDPermissionMode] = contracts.MuseApprovalModeOnRequest
	before := a.SettingsSnapshot().SurfacedOptions
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionRead, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session","modelId":"another-model","approvalMode":{"mode":"futureMode"}}}`)}
	}})
	require.Error(t, a.refreshNativeSettings("session", time.Second))
	assert.Equal(t, before, a.SettingsSnapshot().SurfacedOptions)
}
