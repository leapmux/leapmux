//go:build unix

// Depends on helpers in claude_test.go / goose_test.go that spawn /bin/sh.

package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

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
	agent.reapplySettings = agent.reapplyModelAndMode
	agent.refreshFromSession = agent.refreshCursorFromSession

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

// Cursor (a permission-mode provider) carries a surfaced generic option through its
// ClearContext refresh. Regression guard for the parity fix that routed
// refreshCursorFromSession through the shared extras merge instead of passing nil
// extras -- previously a Cursor generic would be dropped on a context clear.
func TestCursorClearContextRefreshesGenericOptions(t *testing.T) {
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
	agent.reapplySettings = agent.reapplyModelAndMode
	agent.refreshFromSession = agent.refreshCursorFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The generic group is surfaced after the mapped permission-mode group.
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, OptionGroupKeyPermissionMode, groups[0].GetKey())
	assert.Equal(t, "thoughtLevel", groups[1].GetKey())

	// The refresh now carries the generic value (previously dropped: Cursor passed nil).
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "auto", refresh.Model)
	assert.Equal(t, "agent", refresh.PermissionMode)
	assert.Equal(t, "high", refresh.ExtraSettings["thoughtLevel"])
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
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "openai/gpt-5", agent.model)
	assert.Equal(t, "plan", agent.currentPrimaryAgent)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, "plan", refresh.ExtraSettings[OptionGroupKeyPrimaryAgent])
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
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The hidden pseudo-agent is dropped; the stored "build" (re-pushed by reapply)
	// is kept rather than overwritten with "compaction".
	assert.Equal(t, "build", agent.currentPrimaryAgent,
		"a hidden pseudo-agent must not be adopted as the current primary agent")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "build", sink.LastSettingsRefresh().ExtraSettings[OptionGroupKeyPrimaryAgent],
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
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	assert.Equal(t, "code", agent.currentPrimaryAgent)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "anthropic/claude-sonnet-4", refresh.Model)
	assert.Equal(t, "code", refresh.ExtraSettings[OptionGroupKeyPrimaryAgent])
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
	agent.availableModels = []*leapmuxv1.AvailableModel{{Id: "stale/model"}}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

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
		{Id: "build", Name: "Build", IsDefault: true},
		{Id: "plan", Name: "Plan"},
	}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The stale handshake list is replaced by the new session's native modes channel.
	require.Len(t, agent.availablePrimaryAgents, 3)
	assert.Equal(t, "review", agent.availablePrimaryAgents[2].GetId(),
		"the native modes channel refreshes the primary-agent list on ClearContext")
	// The reported current ("plan") is adopted against the refreshed list.
	assert.Equal(t, "plan", agent.currentPrimaryAgent)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().ExtraSettings[OptionGroupKeyPrimaryAgent])
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
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// "plan" is gone from the rebuilt list, so the current re-seeds to the first option.
	require.Len(t, agent.availablePrimaryAgents, 2)
	assert.Equal(t, "build", agent.currentPrimaryAgent,
		"a stored agent dropped from the new session re-seeds to the first available option")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "build", sink.LastSettingsRefresh().ExtraSettings[OptionGroupKeyPrimaryAgent])
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
		{Id: "agent", Name: "Agent", IsDefault: true},
		{Id: "plan", Name: "Plan"},
	}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

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
// primary-agent extras key: refreshModelAndPrimaryAgentFromSession passes
// primaryAgentExtras(""), i.e. nil, so PersistSettingsRefresh keeps the stored
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
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.NotContains(t, refresh.ExtraSettings, OptionGroupKeyPrimaryAgent,
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
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

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
		{Id: "build", Name: "Build", IsDefault: true},
		{Id: "plan", Name: "Plan"},
	}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The configOptions override wins over the modes-channel "build".
	assert.Equal(t, "plan", agent.currentPrimaryAgent)
	// The available list is rebuilt from the new session, surfacing "review".
	require.Len(t, agent.availablePrimaryAgents, 3)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().ExtraSettings[OptionGroupKeyPrimaryAgent])
}

// On ClearContext the new session's unmapped config options are surfaced as
// read-only generic groups and their values ride along in the settings-refresh
// extras, next to (without clobbering) the primaryAgent key. This is the
// ClearContext seam of generic surfacing, mirroring the handshake and runtime seams.
func TestOpenCodeClearContextRefreshesGenericOptions(t *testing.T) {
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
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

	_, ok := agent.ClearContext()
	require.True(t, ok)

	// The generic group is surfaced after the mapped primary-agent group.
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, OptionGroupKeyPrimaryAgent, groups[0].GetKey())
	assert.Equal(t, "thoughtLevel", groups[1].GetKey())

	// The refresh carries both the primary agent and the generic value.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().ExtraSettings
	assert.Equal(t, "build", extras[OptionGroupKeyPrimaryAgent], "the primaryAgent key is not clobbered")
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
	agent.availableModels = []*leapmuxv1.AvailableModel{{Id: "kept/model"}}
	agent.modelsFieldInfos = []acpModelInfo{{ModelID: "kept/model"}}
	sink := &testSink{}
	agent.sink = sink
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent
	agent.refreshFromSession = agent.refreshModelAndPrimaryAgentFromSession

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
	agent.reapplySettings = agent.reapplyModelAndPermissionMode
	agent.refreshFromSession = agent.refreshModelAndPermissionModeFromSession

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
