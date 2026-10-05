package commandcode

import (
	"encoding/json"
	"testing"
	"time"

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

// LeapMux stamps effort `auto` into the options of every Command Code agent,
// because the effort tiers belong to the model. `auto` is no native tier: the
// native host refuses it as an unknown effort, and Start sends no `--effort`
// for it. A live update therefore sends no `session/set_effort` for `auto`. The
// confirmed state of the host then states the real effort, and none at all
// settles as `auto`.
func TestSettingsAutoEffortSendsNoNativeEffort(t *testing.T) {
	var calls []string
	a := agentWithPeer(t, func(method string, params json.RawMessage) agenttest.RPCReply {
		calls = append(calls, method)
		switch method {
		case methodSetModel:
			assert.JSONEq(t, `{"model":"next-model"}`, string(params))
		case methodSessionState:
			return agenttest.RPCReply{Result: json.RawMessage(`{"protocolVersion":1,"session":{"id":"native-session","model":"next-model","permissionMode":"default"}}`)}
		default:
			t.Errorf("a settings update with effort auto must send no %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"Unknown effort: auto"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "next-model", agent.OptionIDEffort: agent.EffortAuto})

	assert.Equal(t, []string{methodSetModel, methodSessionState}, calls)
	require.True(t, result.AppliedLive, "a host with no explicit effort already matches auto")
	assert.Equal(t, optionmap.Map{agent.OptionIDModel: "next-model", agent.OptionIDEffort: "", agent.OptionIDPermissionMode: "default"}, result.SurfacedOptions)
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
}

// The native host cannot drop an effort that it holds. A request for `auto`
// while the host holds one needs a relaunch with no `--effort`. This covers a
// reader who picks Auto, and a model switch for which the host keeps the launch
// effort of the previous model.
func TestSettingsAutoEffortOverAnExplicitNativeEffortRequiresRestart(t *testing.T) {
	for name, options := range map[string]optionmap.Map{
		"Auto picked for the same model":     {agent.OptionIDEffort: agent.EffortAuto},
		"a model switch that resets to Auto": {agent.OptionIDModel: "next-model", agent.OptionIDEffort: agent.EffortAuto},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
				calls = append(calls, method)
				switch method {
				case methodSetModel:
				case methodSessionState:
					return agenttest.RPCReply{Result: json.RawMessage(`{"protocolVersion":1,"session":{"id":"native-session","model":"next-model","effort":"high","permissionMode":"default"}}`)}
				default:
					t.Errorf("a settings update with effort auto must send no %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"Unknown effort: auto"}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})

			result := a.UpdateSettings(options)

			assert.NotContains(t, calls, methodSetEffort)
			assert.False(t, result.AppliedLive)
			assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
		})
	}
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

// The host applies `--effort` as a read-modify-write of its user configuration, and the first
// state that it reports after a relaunch can still hold the previous effort, or none. Command
// Code 1.74.1 reported effort `high` for a relaunch with `--effort low`. The launch confirms the
// effort that it requested through `session/set_effort`, then reads the state again.
func TestConfirmLaunchEffortSetsTheRequestedEffortWhenTheHostHoldsAnother(t *testing.T) {
	for name, held := range map[string]*string{"the host holds another effort": ptr("high"), "the host holds no effort": nil} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			a := agentWithPeer(t, func(method string, params json.RawMessage) agenttest.RPCReply {
				calls = append(calls, method)
				switch method {
				case methodSetEffort:
					assert.JSONEq(t, `{"effort":"low"}`, string(params))
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				case methodSessionState:
					return agenttest.RPCReply{Result: json.RawMessage(`{"protocolVersion":1,"session":{"id":"native-session","model":"native-model","effort":"low","permissionMode":"default"}}`)}
				}
				t.Errorf("unexpected native method: %s", method)
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
			})
			a.Mu.Lock()
			if held != nil {
				a.effort = *held
			}
			a.Mu.Unlock()

			require.NoError(t, a.confirmLaunchEffort("low", time.Second))

			assert.Equal(t, []string{methodSetEffort, methodSessionState}, calls)
			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Equal(t, "low", a.effort)
		})
	}
}

func TestConfirmLaunchEffortSendsNothingWhenTheHostHoldsTheRequestedEffortOrNoneWasRequested(t *testing.T) {
	for name, tc := range map[string]struct{ held, want string }{
		"the host holds the requested effort": {held: "low", want: "low"},
		"no effort was requested":             {held: "high", want: ""},
		"auto was requested":                  {held: "high", want: agent.EffortAuto},
	} {
		t.Run(name, func(t *testing.T) {
			a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
				t.Errorf("the confirmation must send no native request: %s", method)
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
			})
			a.Mu.Lock()
			a.effort = tc.held
			a.Mu.Unlock()

			require.NoError(t, a.confirmLaunchEffort(tc.want, time.Second))

			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Equal(t, tc.held, a.effort)
		})
	}
}

// A refusal keeps the state that the host reports. The model may offer no such tier, and the
// confirmation must never fail a startup that the host completed.
func TestConfirmLaunchEffortKeepsTheHostStateWhenTheHostRefuses(t *testing.T) {
	var calls []string
	a := agentWithPeer(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		calls = append(calls, method)
		if method == methodSetEffort {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"Unknown effort: low"}`)}
		}
		t.Errorf("a refused effort must trigger no further native request: %s", method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
	})
	a.Mu.Lock()
	a.effort = "high"
	a.Mu.Unlock()

	require.NoError(t, a.confirmLaunchEffort("low", time.Second))

	assert.Equal(t, []string{methodSetEffort}, calls)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "high", a.effort)
}

func ptr[T any](value T) *T { return &value }
