package opencode

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleOpenCodeOutput_ConfigOptionUpdateRefreshesModelsGenerically(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	ag.SetModelForTest("anthropic/claude-sonnet-4")
	ag.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`
	ag.HandleOutput([]byte(input))

	require.Equal(t, "openai/gpt-5", ag.ModelForTest())
	require.Len(t, ag.AvailableModelsForTest(), 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", ag.AvailableModelsForTest()[0].GetId())
	assert.Equal(t, "openai/gpt-5", ag.AvailableModelsForTest()[1].GetId())
	assert.True(t, ag.AvailableModelsForTest()[1].IsDefault)
	// The `mode` config option carries the primary agent for OpenCode; a runtime
	// config_option_update syncs it alongside the model.
	assert.Equal(t, PrimaryAgentPlan, ag.CurrentPrimaryAgentForTest())
	// The runtime model + primary-agent switch is broadcast once so the frontend
	// reflects it, carrying the new primary-agent extra (OpenCode has no permission mode).
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, PrimaryAgentPlan, refresh.Options[agent.OptionIDPrimaryAgent])
}

// An idempotent config_option_update -- same current model AND same list as the
// agent already holds -- must trigger no broadcast at all (no settings write, no
// status refresh).
func TestHandleOpenCodeOutput_ConfigOptionUpdateNoBroadcastWhenUnchanged(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	a.SetModelForTest("openai/gpt-5")
	// Pre-seed the exact list the update carries so nothing actually changes.
	a.SetAvailableModelsForTest([]*agent.ModelInfo{
		{Id: "anthropic/claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
		{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true},
	})

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`
	a.HandleOutput([]byte(input))

	assert.Equal(t, "openai/gpt-5", a.ModelForTest())
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings write when nothing changed")
	assert.Equal(t, 0, sink.StatusActiveCount(), "no status refresh when the list is identical")
}

// A config_option_update that keeps the current model but changes the available
// list must broadcast a status refresh (so the picker updates) without a settings
// DB write.
func TestHandleOpenCodeOutput_ConfigOptionUpdateBroadcastsListChange(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	ag.SetModelForTest("openai/gpt-5")
	ag.SetAvailableModelsForTest([]*agent.ModelInfo{
		{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true},
	})

	// Same current model, but a new option appears in the list.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"openai/gpt-5","name":"GPT-5"},{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]}]}}}`
	ag.HandleOutput([]byte(input))

	assert.Equal(t, "openai/gpt-5", ag.ModelForTest())
	require.Len(t, ag.AvailableModelsForTest(), 2)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the list changed")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new option list is broadcast via a status refresh")
}

// buildConfigOptionSelect dedups by value (first occurrence wins) and skips hidden
// ids, mirroring buildACPModels so a server repeating or leaking a pseudo-agent id
// does not surface duplicate or internal picker options.
func TestBuildConfigOptionSelect_DedupsAndFilters(t *testing.T) {
	t.Parallel()

	options := []acp.ConfigOption{{
		ID: acp.ConfigOptionIDMode, CurrentValue: "build",
		Options: []acp.ConfigOptionValue{
			{Value: "build", Name: "Build"},
			{Value: "build", Name: "Build (dup)"},
			{Value: HiddenCompaction, Name: "Compaction"},
			{Value: "plan", Name: "Plan"},
		},
	}}

	built, current, ok := acp.BuildConfigOptionSelectForTest(options, IsHiddenPrimaryAgent)

	require.True(t, ok)
	assert.Equal(t, "build", current)
	require.Len(t, built, 2) // dup dropped, compaction filtered
	assert.Equal(t, "build", built[0].GetId())
	assert.Equal(t, "Build", built[0].GetName()) // first occurrence wins
	assert.Equal(t, "plan", built[1].GetId())
}

// A config_option_update that changes ONLY the available primary-agent list (same
// currentValue, no model) broadcasts a status refresh so the picker updates -- the
// primary-agent analogue of TestHandleOpenCodeOutput_ConfigOptionUpdateBroadcastsListChange
// for the model channel. Without it the new option never reaches the frontend until
// an unrelated change forces a status refresh.
func TestHandleOpenCodeOutput_ConfigOptionUpdatePrimaryAgentListOnlyBroadcasts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	agent.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)
	agent.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: "Build"},
		{Id: PrimaryAgentPlan, Name: "Plan"},
	})

	// Same currentValue ("build"), but "review" is added to the list; no model option.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"review","name":"Review"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 3)
	assert.Equal(t, PrimaryAgentBuild, agent.CurrentPrimaryAgentForTest(), "current primary agent unchanged")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new primary-agent option is broadcast via a status refresh")
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the list changed")
}

// A runtime config_option_update normalizes primary-agent option names the same way
// the handshake (buildPrimaryAgentOptions) does: a whitespace-only name is blanked so
// the id is title-cased, rather than the runtime path leaking the literal whitespace.
func TestHandleOpenCodeOutput_ConfigOptionUpdateNormalizesAgentName(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	agent.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)

	// "plan" reports a whitespace-only name; the runtime path must blank and
	// title-case the id, matching the handshake.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":" "}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 2)
	planOpt := agent.AvailablePrimaryAgentsForTest()[1]
	assert.Equal(t, PrimaryAgentPlan, planOpt.GetId())
	assert.Equal(t, "Plan", planOpt.GetName(), "whitespace-only name is normalized and title-cased")
}

// A config_option_update whose `mode` currentValue is a hidden pseudo-agent must NOT
// adopt it as the current primary agent: the hidden id is filtered from the picker,
// so adopting it would leave the picker showing a selection it can't offer.
func TestHandleOpenCodeOutput_ConfigOptionUpdateIgnoresHiddenCurrentAgent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	agent.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)
	agent.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: "Build"},
		{Id: PrimaryAgentPlan, Name: "Plan"},
	})

	// currentValue is the hidden "compaction" pseudo-agent.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"compaction","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"compaction","name":"Compaction"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, PrimaryAgentBuild, agent.CurrentPrimaryAgentForTest(),
		"a hidden currentValue is not adopted as the current primary agent")
	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 2, "compaction is filtered from the list")
}

// A runtime config_option_update that changes ONLY the primary agent (the `mode`
// select, no model) syncs currentPrimaryAgent and broadcasts the new selection,
// applying the hidden-agent filter to the rebuilt list. Mirrors how the
// permission-mode providers sync their mode.
func TestHandleOpenCodeOutput_ConfigOptionUpdateSyncsPrimaryAgent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	ag.SetModelForTest("anthropic/claude-sonnet-4")
	ag.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)
	ag.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "anthropic/claude-sonnet-4"}})

	// `mode` select changes build -> plan and lists a hidden pseudo-agent.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"compaction","name":"Compaction"}]}]}}}`
	ag.HandleOutput([]byte(input))

	assert.Equal(t, PrimaryAgentPlan, ag.CurrentPrimaryAgentForTest())
	// The hidden `compaction` pseudo-agent is filtered from the rebuilt list.
	require.Len(t, ag.AvailablePrimaryAgentsForTest(), 2)
	// The change is broadcast once, carrying the new primary-agent extra.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, PrimaryAgentPlan, sink.LastSettingsRefresh().Options[agent.OptionIDPrimaryAgent])
}

// A runtime config_option_update that DROPS the active primary agent from the rebuilt
// option list (and names no replacement currentValue) must re-seed the current to the
// default-or-first option rather than leave it pointing at a value absent from the list.
// Without the re-seed the picker would show a selection it can no longer offer. [S1]
func TestHandleOpenCodeOutput_ConfigOptionUpdateReseedsOrphanedCurrent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	ag.SetModelForTest("anthropic/claude-sonnet-4")
	ag.SetCurrentPrimaryAgentForTest(PrimaryAgentPlan)
	ag.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "anthropic/claude-sonnet-4"}})
	ag.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: "Build"},
		{Id: PrimaryAgentPlan, Name: "Plan"},
	})

	// The `mode` select drops the active "plan" and reports no current value.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"","options":[{"value":"build","name":"Build"},{"value":"review","name":"Review"}]}]}}}`
	ag.HandleOutput([]byte(input))

	// The orphaned "plan" is re-seeded to the default-or-first option ("build"), so the
	// current is always a member of the rebuilt list.
	assert.Equal(t, PrimaryAgentBuild, ag.CurrentPrimaryAgentForTest(),
		"the orphaned current re-seeds to the first available option")
	require.Len(t, ag.AvailablePrimaryAgentsForTest(), 2)
	assert.Equal(t, PrimaryAgentBuild, ag.AvailablePrimaryAgentsForTest()[0].GetId())
	assert.Equal(t, "review", ag.AvailablePrimaryAgentsForTest()[1].GetId())
	// The re-seed is a current change: persisted and broadcast once with the new selection.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, PrimaryAgentBuild, sink.LastSettingsRefresh().Options[agent.OptionIDPrimaryAgent])
}

// A runtime config_option_update carrying an unmapped option surfaces it as a
// mutable option group (after the mapped primary-agent group) and persists its
// value into extra_settings alongside the primary agent.
func TestHandleOpenCodeOutput_ConfigOptionUpdateSurfacesGenericGroup(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	ag.SetModelForTest("openai/gpt-5")
	ag.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)

	// No model/mode change; a new thought_level axis appears.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`
	ag.HandleOutput([]byte(input))

	// The option group is surfaced after the mapped primary-agent group.
	groups := ag.OptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, agent.OptionIDPrimaryAgent, groups[0].GetId())
	assert.Equal(t, "thoughtLevel", groups[1].GetId())
	assert.Equal(t, "Thought Level", groups[1].GetLabel())

	// A option value change persists via a settings refresh (not a bare status
	// refresh), carrying both the primary agent and the option value.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().Options
	assert.Equal(t, PrimaryAgentBuild, extras[agent.OptionIDPrimaryAgent])
	assert.Equal(t, "high", extras["thoughtLevel"])
}

// An option list-only change (same currentValue, a new option) broadcasts a status
// refresh, not a settings DB write -- the option analogue of the model/mode list
// channels.
func TestHandleOpenCodeOutput_ConfigOptionUpdateGenericListOnlyBroadcasts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	agent.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)
	// Pre-seed a surfaced option (thoughtLevel: low, options {low,high}).
	agent.OptionsForTest().SetGroupsForTest([]*leapmuxv1.AvailableOptionGroup{{
		Id:    "thoughtLevel",
		Label: "Thought Level",
		Options: acp.BuildOptionValuesForTest(acp.ConfigOption{ID: "thoughtLevel", CurrentValue: "low",
			Options: []acp.ConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}}, nil),
	}})
	agent.OptionsForTest().SetValuesForTest(map[string]string{"thoughtLevel": "low"})

	// Same current value ("low"), but "max" is added; no model/mode option.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","name":"Thought Level","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"},{"value":"max","name":"Max"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.OptionsForTest().GroupsForTest()[0].GetOptions(), 3)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the option list changed")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new config option is broadcast via a status refresh")
}

// TestOptionStateStructureGen_TracksStructuralFoldsForLiveBroadcast guards the building block of
// the live-UpdateSettings broadcast decision: structureGen bumps on EVERY fold that changes the
// group-set structure (a group surfacing or dropping) and NOT on a pure current-value change. Live
// UpdateSettings compares the generation before/after its writes instead of diffing the b.options.
// groups slice -- which the reader goroutine can reassign concurrently -- so it never spuriously
// attributes a reader's change to itself nor (the suppression bug) misses its OWN structural change
// when a concurrent reader fold reverts the structure back: each fold moves the counter, so a
// before != after holds even across a net-zero surface-then-drop.
func TestOptionStateStructureGen_TracksStructuralFoldsForLiveBroadcast(t *testing.T) {
	t.Parallel()

	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
	ag.SetModelForTest("openai/gpt-5")
	ag.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true}})

	gen0 := ag.OptionsForTest().StructureGenForTest()

	// Fold 1: surface a new thoughtLevel group -- a structural change.
	ag.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`))
	gen1 := ag.OptionsForTest().StructureGenForTest()
	require.Len(t, ag.OptionsForTest().GroupsForTest(), 1)
	assert.Greater(t, gen1, gen0, "surfacing a new group is a structural fold")

	// Fold 2: change ONLY the current value (same group, same option set) -- not structural.
	ag.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`))
	gen2 := ag.OptionsForTest().StructureGenForTest()
	assert.Equal(t, "low", ag.OptionsForTest().GroupsForTest()[0].GetCurrentValue(), "the value-only fold landed")
	assert.Equal(t, gen1, gen2, "a pure current-value change is not a structural fold")

	// Fold 3: a complete payload that no longer carries thoughtLevel drops the group -- structural,
	// and it returns the set to its original (empty) structure. The generation must STILL move, so a
	// live UpdateSettings that surfaced the group sees before != after even though a concurrent
	// reader fold reverted the structure (the net-comparison suppression this fix closes).
	ag.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`))
	gen3 := ag.OptionsForTest().StructureGenForTest()
	assert.Empty(t, ag.OptionsForTest().GroupsForTest(), "the dropped group is gone")
	assert.Greater(t, gen3, gen2, "dropping a group is a structural fold even when it restores the prior structure")
}

// Membership-varies at the agent level: a complete config_option_update for a different
// model that no longer carries a previously-surfaced option (the new model doesn't
// support it) drops the option AND deletes it from the persisted extras --
// BroadcastSettingsRefresh replaces stored extras wholesale, and the dropped option rides
// along as "" so it is removed, not kept.
func TestHandleOpenCodeOutput_ConfigOptionUpdateDropsNoLongerApplicableGeneric(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	ag.SetModelForTest("openai/gpt-5")
	ag.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)
	ag.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true}})
	ag.OptionsForTest().SetGroupsForTest([]*leapmuxv1.AvailableOptionGroup{{
		Id:    "thoughtLevel",
		Label: "Thought Level",
		Options: acp.BuildOptionValuesForTest(acp.ConfigOption{ID: "thoughtLevel", CurrentValue: "high",
			Options: []acp.ConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}}, nil),
	}})
	ag.OptionsForTest().SetValuesForTest(map[string]string{"thoughtLevel": "high"})
	// Mirror what applyOptionGroupsLocked records in production when an option
	// surfaces with a value, so the drop emits a delete for it.
	ag.OptionsForTest().MarkSurfacedForTest("thoughtLevel")

	// A complete config_option_update switches to a model that no longer carries the
	// thoughtLevel option -- per the verified ACP contract this is the complete current
	// set, so the absent option no longer applies and must be dropped.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"openai/gpt-5","name":"GPT-5"},{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]}]}}}`
	ag.HandleOutput([]byte(input))

	require.Equal(t, "anthropic/claude-sonnet-4", ag.ModelForTest())
	assert.Empty(t, ag.OptionsForTest().GroupsForTest(), "the no-longer-applicable option is dropped")
	_, live := ag.OptionsForTest().ValuesForTest()["thoughtLevel"]
	assert.False(t, live, "the dropped option is gone from the live values")
	// The model switch persists; the dropped option is deleted from extras (rides as "").
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().Options
	assert.Equal(t, PrimaryAgentBuild, extras[agent.OptionIDPrimaryAgent])
	assert.Equal(t, "", extras["thoughtLevel"], "the dropped option is deleted from extras, not kept")
}

// A settings key that was never surfaced as a config option (no handshake
// reported it) is structurally ignored by UpdateSettings: applyOptionUpdates
// only writes ids present in genericOptionValues, so no set_config_option RPC fires
// for an unknown key and the write still succeeds. (A surfaced config option, by
// contrast, IS writable -- see TestACPConfigOption_MutableUpdateRoundTrips.)
func TestPrimaryAgentUpdateSettings_IgnoresUnknownExtraKey(t *testing.T) {
	t.Parallel()

	a, requests := newOpenCodeAgentForRPC(t)

	ok := a.UpdateSettings(map[string]string{
		agent.OptionIDModel: "openai/gpt-5",
		"thoughtLevel":      "high",
	})

	require.True(t, ok.AppliedLive)
	recorded := requests()
	require.Len(t, recorded, 1, "only the model RPC fires; the unsurfaced extra key sends nothing")
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acp.ConfigOptionIDModel, recorded[0].Params["configId"])
}
