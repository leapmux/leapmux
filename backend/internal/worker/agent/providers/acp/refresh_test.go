//go:build unix

// A terminal test here runs `sleep` through the terminal host, which needs a
// POSIX shell.

package acp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// --- Tests for refreshFromSession via ClearContext ---

func TestACPClearContextPreservesNewerSessionUpdates(t *testing.T) {
	var a *testAgent
	a, _ = newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			a.handleACPSessionUpdate(json.RawMessage(`{"sessionId":"session-2","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}`))
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"}]}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.permissionMode = "agent"
	a.reapplySettings = a.reapplyModelAndSecondary
	a.refreshFromSession = a.applySessionRefresh
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)
	require.Equal(t, "plan", a.permissionMode)
	require.Equal(t, "plan", sink.LastSettingsRefresh().PermissionMode)
}

func TestACPClearContextPersistsOutgoingBufferedText(t *testing.T) {
	t.Parallel()

	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.appendAssistant("unfinished answer")

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"text",
		"text":"unfinished answer",
		"completion":"interrupted"
	}`, string(sink.Messages()[0].Content))
}

func TestACPClearContextReleasesATerminalCreatedDuringSessionNew(t *testing.T) {
	t.Parallel()

	sessionNewReceived := make(chan struct{})
	releaseSessionResponse := make(chan struct{})
	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			close(sessionNewReceived)
			<-releaseSessionResponse
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.sink = agent.NewProviderServices(&agenttest.Sink{})
	a.bind(&a.Base)
	t.Cleanup(a.releaseAllTerminals)

	cleared := make(chan error, 1)
	go func() {
		_, clearErr := a.ClearContext()
		cleared <- clearErr
	}()
	<-sessionNewReceived

	params, err := json.Marshal(acpTerminalCreateParams{
		SessionID: "session-1",
		Command:   "sleep 30",
		Cwd:       t.TempDir(),
	})
	require.NoError(t, err)
	a.terminalCreate(json.RawMessage(`1`), params)
	close(releaseSessionResponse)
	require.NoError(t, <-cleared)

	a.terminalsMu.Lock()
	terminalCount := len(a.terminals)
	a.terminalsMu.Unlock()
	assert.Zero(t, terminalCount, "the new session must not inherit an old-session terminal")
}

func TestACPClearContextRefreshesFromSession(t *testing.T) {
	t.Parallel()

	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "plan"}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.model = "gpt-4o"
	a.permissionMode = "agent"
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.reapplySettings = a.reapplyModelAndSecondary
	a.refreshFromSession = a.applySessionRefresh

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "gpt-5.4", a.model)
	assert.Equal(t, "plan", a.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "plan", refresh.PermissionMode)
}

// TestACPClearContextReappliesOption verifies a config-option
// selection (a mutable thought_level/permissions axis) is re-pushed via
// session/set_config_option after a context clear, so the user's choice survives the
// new session rather than reverting to the server default.
func TestACPClearContextReappliesOption(t *testing.T) {
	t.Parallel()

	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		switch method {
		case MethodSessionNew:
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"}}`)}
		case MethodSessionSetConfigOption:
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.model = "gpt-5.4"
	a.permissionMode = "agent"
	a.sink = agent.NewProviderServices(&agenttest.Sink{})
	a.reapplySettings = a.reapplyModelAndSecondary
	a.refreshFromSession = a.applySessionRefresh
	// The user had picked "high" in the prior session.
	seedThinkingEffort(a, "high")

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	var reapplied bool
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption && r.Params["configId"] == "thinking_effort" {
			reapplied = true
			assert.Equal(t, "high", r.Params["value"])
		}
	}
	assert.True(t, reapplied, "the option selection is re-pushed on ClearContext")
}

// TestACPClearContextKeepsReappliedOptionOverSessionDefault is the regression guard for
// [E6]: when session/new reports an option at the server default (OpenCode/Kilo/Goose
// session responses DO carry configOptions), the ClearContext refresh must NOT revert the
// value reapplyOptions just re-pushed. The captured session/new snapshot predates the
// re-push, so folding its stale default would clobber the user's choice -- the
// applyOptionGroupsKeepingStoredLocked path keeps the re-applied value instead.
func TestACPClearContextKeepsReappliedOptionOverSessionDefault(t *testing.T) {
	t.Parallel()

	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		switch method {
		case MethodSessionNew:
			// The fresh session reports thinking_effort at the server default "medium".
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"},"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"medium","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)}
		case MethodSessionSetConfigOption:
			// The re-push confirms "high".
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.model = "gpt-5.4"
	a.permissionMode = "agent"
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.reapplySettings = a.reapplyModelAndSecondary
	a.refreshFromSession = a.applySessionRefresh
	// The user had picked "high" in the prior session.
	seedThinkingEffort(a, "high")

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	assert.Equal(t, "high", optionids.GroupByID(a.OptionGroups(), "thinking_effort").GetCurrentValue(),
		"the re-applied option survives the session refresh, not reverted to the session default")
	assert.Equal(t, "high", sink.LastSettingsRefresh().Options["thinking_effort"],
		"the persisted refresh carries the re-applied value, not the stale session default")
}

// TestACPClearContextKeepsNonHighEffortOverModelRaise is the regression guard for the
// ClearContext effort-clobber: when the model re-push surfaces the fresh session's effort axis
// at the daemon default "none", raiseEffortOffNone raises it to "high" and FOLDS that into the
// in-memory option values. reapplyOptions must re-push the user's STORED selection ("low"),
// captured before the model write -- not the just-raised in-memory "high". Reading the live
// (clobbered) value would silently lose any non-"high" effort on every /clear.
func TestACPClearContextKeepsNonHighEffortOverModelRaise(t *testing.T) {
	t.Parallel()

	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *testAgent {
			a := &testAgent{}
			a.hooks.ModeChannel = ModeChannelPermissionMode
			a.hooks.EffortConfigID = testThinkingEffort
			return a
		},
		func(a *testAgent) *Base { return &a.Base },
		func(req agenttest.RecordedRequest) agenttest.RPCReply {
			switch req.Method {
			case MethodSessionNew:
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"}}`)}
			case MethodSessionSetConfigOption:
				// The model re-push surfaces thinking_effort at the daemon default "none"; an
				// effort write echoes whatever value it set.
				if req.Params["configId"] == ConfigOptionIDModel {
					return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"none","options":[{"value":"none"},{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)}
				}
				value, _ := req.Params["value"].(string)
				return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"` + value + `","options":[{"value":"none"},{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
	ag.model = "gpt-5.4"
	ag.permissionMode = "agent"
	sink := &agenttest.Sink{}
	ag.sink = agent.NewProviderServices(sink)
	ag.reapplySettings = ag.reapplyModelAndSecondary
	ag.refreshFromSession = ag.applySessionRefresh
	// The user had picked "low" -- a real level below the raise's "high".
	seedThinkingEffort(ag, "low")

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	// The LAST thinking_effort write is the reapply of the stored "low", landing after the
	// model write's "none" -> "high" raise.
	var lastEffortWrite string
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption && r.Params["configId"] == "thinking_effort" {
			lastEffortWrite, _ = r.Params["value"].(string)
		}
	}
	assert.Equal(t, "low", lastEffortWrite, "the stored non-high effort is re-pushed, not the raised default")
	assert.Equal(t, "low", optionids.GroupByID(ag.OptionGroups(), "thinking_effort").GetCurrentValue(),
		"the running session ends on the stored effort, not the model-raise default")
}

// An agent can report an option only after a write. Kiro's session/new response
// omits the effort axis, and the model write of the reapply brings it back. The
// refresh reads the session/new response, which predates that write, so it must
// not drop the axis that the newer payload reports.
func TestACPClearContextKeepsAnOptionThatTheReapplyRevealed(t *testing.T) {
	t.Parallel()

	const model = `{"id":"model","category":"model","currentValue":"gpt-5.4","options":[{"value":"gpt-5.4"}]}`
	effort := func(current string) string {
		return `{"id":"thinking_effort","category":"thought_level","currentValue":"` + current + `","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}`
	}
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t, newTestAgent,
		func(a *testAgent) *Base { return &a.Base },
		func(req agenttest.RecordedRequest) agenttest.RPCReply {
			switch req.Method {
			case MethodSessionNew:
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","modes":{"currentModeId":"agent"},"configOptions":[` + model + `]}`)}
			case MethodSessionSetConfigOption:
				// The model write reveals the axis at its default, and an effort write
				// echoes the value that it set.
				current := "high"
				if req.Params["configId"] == testThinkingEffort {
					current, _ = req.Params["value"].(string)
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[` + model + `,` + effort(current) + `]}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
	ag.model = "gpt-5.4"
	ag.permissionMode = "agent"
	sink := &agenttest.Sink{}
	ag.sink = agent.NewProviderServices(sink)
	ag.reapplySettings = ag.reapplyModelAndSecondary
	ag.refreshFromSession = ag.applySessionRefresh
	seedThinkingEffort(ag, "low")

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	var writes []string
	for _, r := range requests() {
		if r.Method == MethodSessionSetConfigOption {
			writes = append(writes, fmt.Sprintf("%v=%v", r.Params["configId"], r.Params["value"]))
		}
	}
	assert.Equal(t, []string{"model=gpt-5.4", testThinkingEffort + "=low"}, writes)
	g := optionids.GroupByID(ag.OptionGroups(), testThinkingEffort)
	require.NotNil(t, g, "the refresh keeps the axis that the model write revealed")
	assert.Equal(t, "low", g.GetCurrentValue())
	assert.Equal(t, "low", sink.LastSettingsRefresh().Options[testThinkingEffort])
}

// A new session that drops an option, with a reapply whose writes return no
// options, still drops the option: no payload newer than the session response
// exists, so that response decides.
func TestACPClearContextDropsAnOptionThatTheNewSessionOmits(t *testing.T) {
	t.Parallel()

	ag, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","modes":{"currentModeId":"agent"},"configOptions":[{"id":"model","category":"model","currentValue":"gpt-5.4","options":[{"value":"gpt-5.4"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.model = "gpt-5.4"
	ag.permissionMode = "agent"
	ag.sink = agent.NewProviderServices(&agenttest.Sink{})
	ag.reapplySettings = ag.reapplyModelAndSecondary
	ag.refreshFromSession = ag.applySessionRefresh
	seedThinkingEffort(ag, "low")

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), testThinkingEffort),
		"the session response is the newest payload, and it omits the axis")
}

// TestACPClearContextListOnlyChangeBroadcastsStatus is the regression guard for [C13]:
// a ClearContext session refresh that changes only the option-group LIST (the new session
// offers an option with a different set of available values, but the current selection is
// unchanged) must push a status refresh. PersistSettingsRefresh merges option VALUES, which
// did not change, so it no-ops and never carries the new catalog -- without a direct
// BroadcastStatusActive the frontend's option list goes stale until an unrelated push.
func TestACPClearContextListOnlyChangeBroadcastsStatus(t *testing.T) {
	t.Parallel()

	ag, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		switch method {
		case MethodSessionNew:
			// The fresh session reports thinking_effort still at "high" but with "medium"
			// no longer offered -- a list-only change (value kept, available set shrinks).
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"},"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"high"}]}]}`)}
		case MethodSessionSetConfigOption:
			// The re-push confirms "high" against the new session's narrower list, as a
			// write in that session does. Its payload folds before the refresh, and the
			// writes of the reapply broadcast nothing, so the refresh must report the
			// change from the groups that the reader saw before the clear.
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"high"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.model = "gpt-5.4"
	ag.permissionMode = "agent"
	sink := &agenttest.Sink{}
	ag.sink = agent.NewProviderServices(sink)
	ag.reapplySettings = ag.reapplyModelAndSecondary
	ag.refreshFromSession = ag.applySessionRefresh
	// The user had picked "high" in the prior session, surfaced with the full list.
	seedThinkingEffort(ag, "high")

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	// The current selection is unchanged...
	g := optionids.GroupByID(ag.OptionGroups(), "thinking_effort")
	require.NotNil(t, g)
	assert.Equal(t, "high", g.GetCurrentValue(), "the current selection is kept")
	// ...but the available list shrank to the new session's narrower set...
	assert.Len(t, g.GetOptions(), 2, "the new session's narrower option list is applied")
	// ...and that list-only change is broadcast as a status refresh so the frontend's option
	// groups don't go stale (PersistSettingsRefresh would no-op since the value didn't change).
	assert.Equal(t, 1, sink.StatusActiveCount(), "a list-only ClearContext change pushes a status refresh")
}

// The same list-only change, when the writes of the reapply return no options:
// the session response is then the newest payload, and its fold both narrows
// the list and reports the change.
func TestACPClearContextListOnlyChangeFromTheSessionResponseBroadcastsStatus(t *testing.T) {
	t.Parallel()

	ag, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"},"configOptions":[{"id":"thinking_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"high"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.model = "gpt-5.4"
	ag.permissionMode = "agent"
	sink := &agenttest.Sink{}
	ag.sink = agent.NewProviderServices(sink)
	ag.reapplySettings = ag.reapplyModelAndSecondary
	ag.refreshFromSession = ag.applySessionRefresh
	seedThinkingEffort(ag, "high")

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	g := optionids.GroupByID(ag.OptionGroups(), "thinking_effort")
	require.NotNil(t, g)
	assert.Equal(t, "high", g.GetCurrentValue())
	assert.Len(t, g.GetOptions(), 2, "the session response narrows the list")
	assert.Equal(t, 1, sink.StatusActiveCount(), "a list-only ClearContext change pushes a status refresh")
}

// The permission-mode mirror of S4: on ClearContext a permission-mode provider rebuilds
// availableModes from the native modes channel, so a new session whose mode list changed
// is reflected even when no configOptions `mode` is present. [S4]
func TestACPClearContextRefreshesModeListFromNativeModes(t *testing.T) {
	t.Parallel()

	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "agent", "availableModes": [{"id":"agent"},{"id":"plan"},{"id":"ask"}]}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.model = "gpt-4o"
	a.permissionMode = "agent"
	a.availableModes = []*leapmuxv1.AvailableOption{
		{Id: "agent", Name: "Agent"},
		{Id: "plan", Name: "Plan"},
	}
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.reapplySettings = a.reapplyModelAndSecondary
	a.refreshFromSession = a.applySessionRefresh

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The stale handshake mode list is replaced by the new session's native modes channel.
	require.Len(t, a.availableModes, 3)
	assert.Equal(t, "ask", a.availableModes[2].GetId(),
		"the native modes channel refreshes the mode list on ClearContext")
	assert.Equal(t, "agent", a.permissionMode)
}

// On ClearContext a permission-mode provider applies the configOptions `mode`
// override (matching applyHandshakeMode), not just the modes-channel value -- so
// the mode resolves the same way the handshake does instead of diverging on clear.
func TestACPClearContextAppliesConfigOptionModeOverride(t *testing.T) {
	t.Parallel()

	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			// The modes channel says "agent" but the configOptions `mode` says "plan".
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "agent"},
				"configOptions": [{"id":"mode","currentValue":"plan","options":[
					{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]}]
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.model = "gpt-4o"
	a.permissionMode = "agent"
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.reapplySettings = a.reapplyModelAndSecondary
	a.refreshFromSession = a.applySessionRefresh

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The configOptions override wins over the modes-channel "agent".
	assert.Equal(t, "plan", a.permissionMode)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().PermissionMode)
}
