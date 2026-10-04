package commandcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingsApplyReadsNativeConfirmation(t *testing.T) {
	var calls []string
	a := agentWithPeer(t, func(method string, params json.RawMessage) agenttest.RPCReply {
		calls = append(calls, method)
		switch method {
		case methodSetModel:
			assert.JSONEq(t, `{"model":"requested-model"}`, string(params))
		case methodSetEffort:
			assert.JSONEq(t, `{"effort":"high"}`, string(params))
		case methodSessionState:
			return agenttest.RPCReply{Result: json.RawMessage(`{"protocolVersion":1,"session":{"id":"native-session","model":"confirmed-model","effort":"low","permissionMode":"default"}}`)}
		default:
			t.Errorf("unexpected native settings method: %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "requested-model", agent.OptionIDEffort: "high"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, []string{methodSetModel, methodSetEffort, methodSessionState}, calls)
	assert.Equal(t, optionmap.Map{agent.OptionIDModel: "confirmed-model", agent.OptionIDEffort: "low", agent.OptionIDPermissionMode: "default"}, result.SurfacedOptions)
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	assert.Equal(t, "native-session", sessionID)
}

func TestSettingsChangesDuringATurnRequireRestartWithoutRPC(t *testing.T) {
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		t.Errorf("a busy native session must receive no setting RPC: %s", method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
	})
	a.startTurn("turn-settings")
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
	assert.False(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
}

func TestPermissionChangeRequiresRestartWithoutRPC(t *testing.T) {
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		t.Errorf("a permission change must receive no live RPC: %s", method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
	})
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: "plan"})
	assert.False(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDPermissionMode].State)
}

func TestSettingsSetterFailureKeepsAllChoicesUnresolved(t *testing.T) {
	var calls []string
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		calls = append(calls, method)
		if method == methodSetModel {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"the native effort is unavailable"}`)}
	})
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "next-model", agent.OptionIDEffort: "high"})
	assert.Equal(t, []string{methodSetModel, methodSetEffort}, calls)
	assert.False(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
}

func TestSettingsConfirmationRejectsMalformedState(t *testing.T) {
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionState {
			return agenttest.RPCReply{Result: json.RawMessage(`{"protocolVersion":1,"session":{"id":"native-session","model":"","permissionMode":"default"}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
	assert.False(t, result.AppliedLive)
	a.Mu.Lock()
	sessionID, model := a.sessionID, a.model
	a.Mu.Unlock()
	assert.Equal(t, "native-model", model)
	assert.Equal(t, "native-session", sessionID)
}

func TestSettingsConfirmationCannotReplaceTheNativeSession(t *testing.T) {
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionState {
			return agenttest.RPCReply{Result: json.RawMessage(`{"protocolVersion":1,"session":{"id":"foreign-session","model":"next-model","effort":"high","permissionMode":"default"}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
	require.False(t, result.AppliedLive, "a settings confirmation must not silently replace the native session")
	a.Mu.Lock()
	sessionID, model := a.sessionID, a.model
	a.Mu.Unlock()
	assert.Equal(t, "native-session", sessionID)
	assert.Equal(t, "native-model", model)
}
