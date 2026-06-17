//go:build unix

// Depends on helpers in claude_test.go / goose_test.go that spawn /bin/sh.

package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

// --- Tests for refreshFromSession via ClearContext ---

func TestCopilotClearContextRefreshesFromSession(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "plan"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-4o"
	agent.permissionMode = "agent"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "gpt-5.4", agent.model)
	assert.Equal(t, "plan", agent.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "plan", refresh.PermissionMode)
}

// TestCopilotClearContextReappliesOption verifies a config-option
// selection (a mutable thought_level/permissions axis) is re-pushed via
// session/set_config_option after a context clear, so the user's choice survives the
// new session rather than reverting to the server default.
func TestCopilotClearContextReappliesOption(t *testing.T) {
	agent, requests := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		switch method {
		case acpMethodSessionNew:
			return json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"}}`)
		case acpMethodSessionSetConfigOption:
			return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.4"
	agent.permissionMode = "agent"
	agent.sink = &testSink{}
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh
	// The user had picked "high" in the prior session.
	seedReasoningEffort(agent, "high")

	_, ok := agent.ClearContext()
	require.True(t, ok)

	var reapplied bool
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption && r.Params["configId"] == "reasoning_effort" {
			reapplied = true
			assert.Equal(t, "high", r.Params["value"])
		}
	}
	assert.True(t, reapplied, "the option selection is re-pushed on ClearContext")
}

// TestCopilotClearContextKeepsReappliedOptionOverSessionDefault is the regression guard for
// [E6]: when session/new reports an option at the server default (OpenCode/Kilo/Copilot
// session responses DO carry configOptions), the ClearContext refresh must NOT revert the
// value reapplyOptions just re-pushed. The captured session/new snapshot predates the
// re-push, so folding its stale default would clobber the user's choice -- the
// applyOptionGroupsKeepingStoredLocked path keeps the re-applied value instead.
func TestCopilotClearContextKeepsReappliedOptionOverSessionDefault(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		switch method {
		case acpMethodSessionNew:
			// The fresh session reports reasoning_effort at the server default "medium".
			return json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"},"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"medium","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
		case acpMethodSessionSetConfigOption:
			// The re-push confirms "high".
			return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.4"
	agent.permissionMode = "agent"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh
	// The user had picked "high" in the prior session.
	seedReasoningEffort(agent, "high")

	_, ok := agent.ClearContext()
	require.True(t, ok)

	assert.Equal(t, "high", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue(),
		"the re-applied option survives the session refresh, not reverted to the session default")
	assert.Equal(t, "high", sink.LastSettingsRefresh().Options["reasoning_effort"],
		"the persisted refresh carries the re-applied value, not the stale session default")
}

// TestCopilotClearContextKeepsNonHighEffortOverModelRaise is the regression guard for the
// ClearContext effort-clobber: when the model re-push surfaces the fresh session's effort axis
// at the daemon default "none", raiseEffortOffNone raises it to "high" and FOLDS that into the
// in-memory option values. reapplyOptions must re-push the user's STORED selection ("low"),
// captured before the model write -- not the just-raised in-memory "high". Reading the live
// (clobbered) value would silently lose any non-"high" effort on every /clear.
func TestCopilotClearContextKeepsNonHighEffortOverModelRaise(t *testing.T) {
	agent, requests := newACPAgentForRPCWithRequestResponder(t,
		func() *CopilotCLIAgent {
			a := &CopilotCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			return a
		},
		func(a *CopilotCLIAgent) *acpBase { return &a.acpBase },
		func(req recordedRequest) json.RawMessage {
			switch req.Method {
			case acpMethodSessionNew:
				return json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"}}`)
			case acpMethodSessionSetConfigOption:
				// The model re-push surfaces reasoning_effort at the daemon default "none"; an
				// effort write echoes whatever value it set.
				if req.Params["configId"] == acpConfigOptionIDModel {
					return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"none","options":[{"value":"none"},{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
				}
				value, _ := req.Params["value"].(string)
				return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"` + value + `","options":[{"value":"none"},{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
			}
			return json.RawMessage(`{}`)
		})
	agent.model = "gpt-5.4"
	agent.permissionMode = "agent"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh
	// The user had picked "low" -- a real level below the raise's "high".
	seedReasoningEffort(agent, "low")

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The LAST reasoning_effort write is the reapply of the stored "low", landing after the
	// model write's "none" -> "high" raise.
	var lastEffortWrite string
	for _, r := range requests() {
		if r.Method == acpMethodSessionSetConfigOption && r.Params["configId"] == "reasoning_effort" {
			lastEffortWrite, _ = r.Params["value"].(string)
		}
	}
	assert.Equal(t, "low", lastEffortWrite, "the stored non-high effort is re-pushed, not the raised default")
	assert.Equal(t, "low", optionids.GroupByID(agent.OptionGroups(), "reasoning_effort").GetCurrentValue(),
		"the running session ends on the stored effort, not the model-raise default")
}

// TestCopilotClearContextListOnlyChangeBroadcastsStatus is the regression guard for [C13]:
// a ClearContext session refresh that changes only the option-group LIST (the new session
// offers an option with a different set of available values, but the current selection is
// unchanged) must push a status refresh. PersistSettingsRefresh merges option VALUES, which
// did not change, so it no-ops and never carries the new catalog -- without a direct
// BroadcastStatusActive the frontend's option list goes stale until an unrelated push.
func TestCopilotClearContextListOnlyChangeBroadcastsStatus(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		switch method {
		case acpMethodSessionNew:
			// The fresh session reports reasoning_effort still at "high" but with "medium"
			// no longer offered -- a list-only change (value kept, available set shrinks).
			return json.RawMessage(`{"sessionId":"session-2","models":{"currentModelId":"gpt-5.4"},"modes":{"currentModeId":"agent"},"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"high"}]}]}`)
		case acpMethodSessionSetConfigOption:
			// The re-push confirms "high" against the prior (full) list, so the list only
			// changes when applySessionRefresh folds the narrower session/new payload above.
			return json.RawMessage(`{"configOptions":[{"id":"reasoning_effort","category":"thought_level","currentValue":"high","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]}]}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.4"
	agent.permissionMode = "agent"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh
	// The user had picked "high" in the prior session, surfaced with the full list.
	seedReasoningEffort(agent, "high")

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The current selection is unchanged...
	g := optionids.GroupByID(agent.OptionGroups(), "reasoning_effort")
	require.NotNil(t, g)
	assert.Equal(t, "high", g.GetCurrentValue(), "the current selection is kept")
	// ...but the available list shrank to the new session's narrower set...
	assert.Len(t, g.GetOptions(), 2, "the new session's narrower option list is applied")
	// ...and that list-only change is broadcast as a status refresh so the frontend's option
	// groups don't go stale (PersistSettingsRefresh would no-op since the value didn't change).
	assert.Equal(t, 1, sink.StatusActiveCount(), "a list-only ClearContext change pushes a status refresh")
}

func TestCursorClearContextRefreshesWithNormalization(t *testing.T) {
	agent, _ := newCursorAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			// Cursor returns the wire format "default[]" for auto model.
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "default[]"},
				"modes":  {"currentModeId": "agent"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "some-model"
	agent.permissionMode = "plan"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "auto", agent.model)
	assert.Equal(t, "agent", agent.permissionMode)

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
	agent, _ := newCursorAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "default[]"},
				"modes":  {"currentModeId": "agent"},
				"configOptions": [
					{"id":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]},
					{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}
				]
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "some-model"
	agent.permissionMode = "plan"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The option group is surfaced after the mapped permission-mode group.
	groups := agent.OptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, OptionIDPermissionMode, groups[0].GetId())
	assert.Equal(t, "thoughtLevel", groups[1].GetId())

	// The refresh now carries the option value (previously dropped: Cursor passed nil).
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "auto", refresh.Model)
	assert.Equal(t, "agent", refresh.PermissionMode)
	assert.Equal(t, "high", refresh.Options["thoughtLevel"])
}

func TestOpenCodeClearContextRefreshesPrimaryAgent(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "plan"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = "build"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "openai/gpt-5", agent.model)
	assert.Equal(t, "plan", agent.currentPrimaryAgent)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, "plan", refresh.Options[OptionIDPrimaryAgent])
}

// On ClearContext a primary-agent provider must drop a modes-channel currentModeId
// that the hidden filter removes (e.g. OpenCode's "compaction"), mirroring the
// handshake guard (configurePrimaryAgents) and the runtime guard
// (syncConfigOptionSelectLocked). Without the drop, the raw currentModeId write
// adopts the hidden pseudo-agent and persists it, seeding the picker with a
// selection that has no visible option. The response carries no configOptions `mode`
// to correct it, isolating the raw-write path.
func TestOpenCodeClearContextDropsHiddenCurrentPrimaryAgent(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "compaction", "availableModes": [{"id":"build"},{"id":"plan"},{"id":"compaction"}]}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = "build"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The hidden pseudo-agent is dropped; the stored "build" (re-pushed by reapply)
	// is kept rather than overwritten with "compaction".
	assert.Equal(t, "build", agent.currentPrimaryAgent,
		"a hidden pseudo-agent must not be adopted as the current primary agent")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "build", sink.LastSettingsRefresh().Options[OptionIDPrimaryAgent],
		"the refresh carries the kept primary agent, not the dropped hidden current")
}

func TestKiloClearContextRefreshesPrimaryAgent(t *testing.T) {
	agent, _ := newKiloAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "anthropic/claude-sonnet-4"},
				"modes":  {"currentModeId": "code"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-opus-4"
	agent.currentPrimaryAgent = "plan"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	assert.Equal(t, "code", agent.currentPrimaryAgent)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "anthropic/claude-sonnet-4", refresh.Model)
	assert.Equal(t, "code", refresh.Options[OptionIDPrimaryAgent])
}

// S4: ClearContext refreshes the available-model list (not just the current id)
// from the new session, including the configOptions channel OpenCode/Kilo use.
func TestOpenCodeClearContextRefreshesAvailableModels(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"modes": {"currentModeId": "build"},
				"configOptions": [{"id":"model","currentValue":"openai/gpt-5","options":[
					{"value":"openai/gpt-5","name":"GPT-5"},
					{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}
				]}]
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-sonnet-4"
	agent.currentPrimaryAgent = "build"
	agent.availableModels = []*ModelInfo{{Id: "stale/model"}}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The stale handshake-time list is replaced by the new session's models.
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "openai/gpt-5", agent.availableModels[0].GetId())
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.availableModels[1].GetId())
	// The model the user had is preserved across the clear (re-pushed by reapply).
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
}

// On ClearContext a primary-agent provider rebuilds availablePrimaryAgents from the
// NATIVE modes channel (not only the configOptions select), so a new session whose
// agent list grew/shrank is reflected instead of freezing at the handshake list while
// the model list refreshes. Here the new session adds "review" through the modes
// channel and carries no configOptions. [S4]
func TestOpenCodeClearContextRefreshesPrimaryAgentListFromNativeModes(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "plan", "availableModes": [{"id":"build"},{"id":"plan"},{"id":"review"}]}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = "build"
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: "build", Name: "Build"},
		{Id: "plan", Name: "Plan"},
	}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The stale handshake list is replaced by the new session's native modes channel.
	require.Len(t, agent.availablePrimaryAgents, 3)
	assert.Equal(t, "review", agent.availablePrimaryAgents[2].GetId(),
		"the native modes channel refreshes the primary-agent list on ClearContext")
	// The reported current ("plan") is adopted against the refreshed list.
	assert.Equal(t, "plan", agent.currentPrimaryAgent)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().Options[OptionIDPrimaryAgent])
}

// On ClearContext a primary-agent provider whose new session DROPS the stored agent
// from the rebuilt list (and reports no valid replacement) re-seeds the current to the
// default-or-first option rather than keep an orphan -- the ClearContext mirror of the
// runtime re-seed, resolving the current the same way the handshake does. [S2]
func TestOpenCodeClearContextReseedsOrphanedPrimaryAgent(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			// The new session lists [build, review] (dropping the stored "plan") and
			// reports no current agent, with no configOptions to correct it.
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"availableModes": [{"id":"build"},{"id":"review"}]}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = "plan"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// "plan" is gone from the rebuilt list, so the current re-seeds to the first option.
	require.Len(t, agent.availablePrimaryAgents, 2)
	assert.Equal(t, "build", agent.currentPrimaryAgent,
		"a stored agent dropped from the new session re-seeds to the first available option")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "build", sink.LastSettingsRefresh().Options[OptionIDPrimaryAgent])
}

// The permission-mode mirror of S4: on ClearContext a permission-mode provider rebuilds
// availableModes from the native modes channel, so a new session whose mode list changed
// is reflected even when no configOptions `mode` is present. [S4]
func TestCopilotClearContextRefreshesModeListFromNativeModes(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "agent", "availableModes": [{"id":"agent"},{"id":"plan"},{"id":"ask"}]}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-4o"
	agent.permissionMode = "agent"
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: "agent", Name: "Agent"},
		{Id: "plan", Name: "Plan"},
	}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The stale handshake mode list is replaced by the new session's native modes channel.
	require.Len(t, agent.availableModes, 3)
	assert.Equal(t, "ask", agent.availableModes[2].GetId(),
		"the native modes channel refreshes the mode list on ClearContext")
	assert.Equal(t, "agent", agent.permissionMode)
}

// A ClearContext whose new session reports no primary agent (empty
// currentModeId) while the stored primary agent is empty must not emit a
// primary-agent extras key: applySessionRefresh's snapshot passes
// primaryAgentOptions(""), i.e. nil, so PersistSettingsRefresh keeps the stored
// extras rather than clearing them to "{}" via a map{primaryAgent:""}.
func TestOpenCodeClearContextEmptyPrimaryAgentPreservesExtras(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = ""
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.NotContains(t, refresh.Options, OptionIDPrimaryAgent,
		"empty primary agent must not emit the extras key, else it clears the stored value")
}

// On ClearContext a permission-mode provider applies the configOptions `mode`
// override (matching applyHandshakeMode), not just the modes-channel value -- so
// the mode resolves the same way the handshake does instead of diverging on clear.
func TestCopilotClearContextAppliesConfigOptionModeOverride(t *testing.T) {
	agent, _ := newCopilotAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			// The modes channel says "agent" but the configOptions `mode` says "plan".
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "gpt-5.4"},
				"modes":  {"currentModeId": "agent"},
				"configOptions": [{"id":"mode","currentValue":"plan","options":[
					{"value":"agent","name":"Agent"},{"value":"plan","name":"Plan"}]}]
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-4o"
	agent.permissionMode = "agent"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The configOptions override wins over the modes-channel "agent".
	assert.Equal(t, "plan", agent.permissionMode)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().PermissionMode)
}

// On ClearContext a primary-agent provider (OpenCode/Kilo) rebuilds
// availablePrimaryAgents and applies the configOptions primary-agent override --
// the mirror of TestCopilotClearContextAppliesConfigOptionModeOverride for the
// permission-mode side. Without it the picker would freeze at the handshake list and
// a session reporting its current agent only through the configOptions select (empty
// modes-channel currentModeId) would keep the stale selection.
func TestOpenCodeClearContextAppliesConfigOptionPrimaryAgentOverride(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			// The modes channel says "build" but the configOptions `mode` says "plan",
			// and the configOptions list adds an agent ("review") absent from the
			// pre-seeded handshake list.
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "build"},
				"configOptions": [{"id":"mode","currentValue":"plan","options":[
					{"value":"build","name":"Build"},
					{"value":"plan","name":"Plan"},
					{"value":"review","name":"Review"}]}]
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-4o"
	agent.currentPrimaryAgent = "build"
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: "build", Name: "Build"},
		{Id: "plan", Name: "Plan"},
	}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The configOptions override wins over the modes-channel "build".
	assert.Equal(t, "plan", agent.currentPrimaryAgent)
	// The available list is rebuilt from the new session, surfacing "review".
	require.Len(t, agent.availablePrimaryAgents, 3)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().Options[OptionIDPrimaryAgent])
}

// On ClearContext the new session's unmapped config options are surfaced as
// mutable option groups and their values ride along in the settings-refresh
// extras, next to (without clobbering) the primaryAgent key. This is the
// ClearContext seam of option surfacing, mirroring the handshake and runtime seams.
func TestOpenCodeClearContextRefreshesOptions(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"modes":  {"currentModeId": "build"},
				"configOptions": [
					{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},
					{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"openai/gpt-5","name":"GPT-5"}]},
					{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}
				]
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.currentPrimaryAgent = "build"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The option group is surfaced alongside the mapped primary-agent group.
	groups := agent.OptionGroups()
	assert.NotNil(t, optionids.GroupByID(groups, OptionIDPrimaryAgent))
	assert.NotNil(t, optionids.GroupByID(groups, "thoughtLevel"))

	// The refresh carries both the primary agent and the option value.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().Options
	assert.Equal(t, "build", extras[OptionIDPrimaryAgent], "the primaryAgent key is not clobbered")
	assert.Equal(t, "high", extras["thoughtLevel"])
}

// A ClearContext whose new session parses but reports no models in either channel
// must leave BOTH availableModels and the remembered models-field catalog at their
// prior values. Resetting modelsFieldInfos to empty while the stale list lingers
// would drop models-field-only entries on the next config_option_update re-union.
func TestOpenCodeClearContextEmptyModelsKeepsCatalog(t *testing.T) {
	agent, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{"sessionId": "session-2", "modes": {"currentModeId": "build"}}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-sonnet-4"
	agent.currentPrimaryAgent = "build"
	agent.availableModels = []*ModelInfo{{Id: "kept/model"}}
	agent.modelsFieldInfos = []acpModelInfo{{ModelID: "kept/model"}}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// No models in the response -> both mirrors keep their prior values.
	require.Len(t, agent.availableModels, 1)
	assert.Equal(t, "kept/model", agent.availableModels[0].GetId())
	require.Len(t, agent.modelsFieldInfos, 1)
	assert.Equal(t, "kept/model", agent.modelsFieldInfos[0].ModelID)
}

func TestGooseClearContextRefreshesFromSession(t *testing.T) {
	agent, _ := newGooseAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "claude-sonnet-4"},
				"modes":  {"currentModeId": "approve"}
			}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "gpt-5.4"
	agent.permissionMode = "auto"
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndSecondary
	agent.refreshFromSession = agent.applySessionRefresh

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "claude-sonnet-4", agent.model)
	assert.Equal(t, "approve", agent.permissionMode)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "claude-sonnet-4", refresh.Model)
	assert.Equal(t, "approve", refresh.PermissionMode)
}
