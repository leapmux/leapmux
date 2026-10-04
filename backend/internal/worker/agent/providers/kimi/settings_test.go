package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// lastProfile returns the agent_config of the newest profile write.
func lastProfile(t *testing.T, rig *kimiTestRig) map[string]any {
	t.Helper()
	posts := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/profile"))
	require.NotEmpty(t, posts)
	var body struct {
		Config map[string]any `json:"agent_config"`
	}
	require.NoError(t, json.Unmarshal(posts[len(posts)-1].Body, &body))
	return body.Config
}

func profileCount(rig *kimiTestRig) int {
	return len(rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/profile")))
}

func TestKimiOptionGroups(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	current := agent.CurrentOptions(rig.agent.OptionGroups())
	assert.Equal(t, "kimi-k2", current[agent.OptionIDModel], "the configured default model runs")
	assert.Equal(t, agent.EffortAuto, current[agent.OptionIDEffort])
	assert.Equal(t, contracts.KimiModeManual, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kimiSwarmOff, current[kimiOptionSwarmMode])

	var modelGroup []string
	for _, group := range rig.agent.OptionGroups() {
		if group.GetId() == agent.OptionIDModel {
			modelGroup = agenttest.OptionIDs(group.GetOptions())
		}
	}
	assert.Equal(t, []string{"kimi-k2", "kimi-text"}, modelGroup)

	snapshot := rig.agent.SettingsSnapshot()
	assert.True(t, snapshot.AppliedLive)
	assert.Equal(t, current, map[string]string(snapshot.SurfacedOptions))
}

func TestKimiStaticOptionGroups(t *testing.T) {
	t.Parallel()

	require.Len(t, kimiStaticOptionGroups, 2)
	permission := kimiStaticOptionGroups[0]
	assert.Equal(t, agent.OptionIDPermissionMode, permission.GetId())
	assert.Equal(t, []string{"manual", "yolo", "auto", "plan"}, agenttest.OptionIDs(permission.GetOptions()))
	assert.Equal(t, contracts.KimiDefaultMode, permission.GetDefaultValue())
	swarm := kimiStaticOptionGroups[1]
	assert.Equal(t, kimiOptionSwarmMode, swarm.GetId())
	assert.Equal(t, []string{"off", "on"}, agenttest.OptionIDs(swarm.GetOptions()))
}

func TestKimiLaunchSettings(t *testing.T) {
	t.Parallel()

	t.Run("a new session gets its whole profile", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(
			agent.OptionIDModel, "kimi-text", agent.OptionIDPermissionMode, contracts.KimiModeYolo, kimiOptionSwarmMode, kimiSwarmOn,
		)})
		config := lastProfile(t, rig)
		assert.Equal(t, map[string]any{"model": "kimi-text", "permission_mode": "yolo", "plan_mode": false, "swarm_mode": true}, config,
			"the server binds no model to a new session by itself; Auto sends no thinking level")
	})

	t.Run("a stated effort and plan mode", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDEffort, "high", agent.OptionIDPermissionMode, contracts.KimiModePlan)})
		config := lastProfile(t, rig)
		assert.Equal(t, "high", config["thinking"])
		assert.Equal(t, true, config["plan_mode"])
		assert.Equal(t, contracts.KimiDefaultMode, config["permission_mode"], "plan mode keeps the default permission mode under it")
		assert.Equal(t, contracts.KimiModePlan, agent.CurrentOptions(rig.agent.OptionGroups())[agent.OptionIDPermissionMode])
	})

	t.Run("an unlisted model falls back to the default", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDModel, "gone-model")})
		assert.Equal(t, "kimi-k2", lastProfile(t, rig)["model"])
	})
}

func TestKimiUpdateSettings(t *testing.T) {
	t.Parallel()

	t.Run("switches the model and the effort in one write", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDModel, "kimi-text")})
		before := profileCount(rig)
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-k2", agent.OptionIDEffort: "low"})
		assert.True(t, result.AppliedLive)
		require.Equal(t, before+1, profileCount(rig))
		assert.Equal(t, map[string]any{"model": "kimi-k2", "thinking": "low"}, lastProfile(t, rig))
		assert.Equal(t, optionmap.Map{agent.OptionIDModel: "kimi-k2", agent.OptionIDEffort: "low"}, result.ConfirmedOptions())
		assert.Equal(t, "kimi-k2", rig.sink.LastSettingsRefresh().Model)
	})

	t.Run("refuses what the catalog does not offer", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		before := profileCount(rig)
		result := rig.agent.UpdateSettings(optionmap.Map{
			agent.OptionIDModel: "gone-model", agent.OptionIDEffort: "ultra", agent.OptionIDPermissionMode: "reckless",
			kimiOptionSwarmMode: "maybe", "primaryAgent": "build",
		})
		assert.Equal(t, before, profileCount(rig), "nothing valid, nothing sent")
		assert.Empty(t, result.ConfirmedOptions())
		for _, key := range []string{agent.OptionIDModel, agent.OptionIDEffort, agent.OptionIDPermissionMode, kimiOptionSwarmMode, "primaryAgent"} {
			assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[key].State, key)
		}
	})

	t.Run("an effort the target model lacks is refused", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text", agent.OptionIDEffort: "high"})
		assert.Equal(t, optionmap.Map{agent.OptionIDModel: "kimi-text"}, result.ConfirmedOptions())
		assert.NotContains(t, lastProfile(t, rig), "thinking")
	})

	t.Run("plan mode on and off keeps the permission mode under it", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDPermissionMode, contracts.KimiModeYolo)})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: contracts.KimiModePlan})
		assert.Equal(t, map[string]any{"plan_mode": true}, lastProfile(t, rig))
		assert.Equal(t, contracts.KimiModePlan, result.ConfirmedOptions()[agent.OptionIDPermissionMode])

		result = rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: contracts.KimiModeAuto})
		assert.Equal(t, map[string]any{"plan_mode": false, "permission_mode": "auto"}, lastProfile(t, rig))
		assert.Equal(t, contracts.KimiModeAuto, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	})

	t.Run("switching back to Auto sends the configured level", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDEffort, "high")})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDEffort: agent.EffortAuto})
		assert.Equal(t, map[string]any{"thinking": "medium"}, lastProfile(t, rig), "the user's configuration states medium")
		assert.Equal(t, agent.EffortAuto, result.ConfirmedOptions()[agent.OptionIDEffort], "the axis reads Auto, not the level Auto stands for")
	})

	t.Run("an unchanged value writes nothing and still confirms", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		before := profileCount(rig)
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-k2", kimiOptionSwarmMode: kimiSwarmOff})
		assert.Equal(t, before, profileCount(rig))
		assert.Equal(t, optionmap.Map{agent.OptionIDModel: "kimi-k2", kimiOptionSwarmMode: kimiSwarmOff}, result.ConfirmedOptions())
	})

	t.Run("a refused write confirms nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Code: 50000, Msg: "no"})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		assert.Empty(t, result.ConfirmedOptions())
		assert.Equal(t, "kimi-k2", agent.CurrentOptions(rig.agent.OptionGroups())[agent.OptionIDModel])
	})

	t.Run("a read-back that fails confirms nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		refreshes := rig.sink.SettingsRefreshCount()
		rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/status"), fakeKapReply{HTTPStatus: 500, Code: 50000, Msg: "status down"})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		assert.True(t, result.AppliedLive)
		assert.Equal(t, map[string]any{"model": "kimi-text"}, lastProfile(t, rig), "the write went out")
		assert.Empty(t, result.ConfirmedOptions(), "nothing proves what the server runs now")
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
		assert.Equal(t, refreshes, rig.sink.SettingsRefreshCount(), "nothing unconfirmed is persisted")
	})

	t.Run("an empty value is no request", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		before := profileCount(rig)
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "", agent.OptionIDPermissionMode: contracts.KimiModeYolo})
		assert.NotContains(t, result.Settlements, agent.OptionIDModel)
		assert.Equal(t, optionmap.Map{agent.OptionIDPermissionMode: contracts.KimiModeYolo}, result.ConfirmedOptions())
		assert.Equal(t, before+1, profileCount(rig))
		assert.Equal(t, map[string]any{"permission_mode": "yolo"}, lastProfile(t, rig), "the empty model changes nothing")
	})

	t.Run("switching to Auto on a model that cannot think sends no level", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDEffort, "high")})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text", agent.OptionIDEffort: agent.EffortAuto})
		assert.Equal(t, map[string]any{"model": "kimi-text"}, lastProfile(t, rig), "a model that cannot think takes no level")
		assert.Equal(t, "kimi-text", result.ConfirmedOptions()[agent.OptionIDModel])
		assert.NotContains(t, result.SurfacedOptions, agent.OptionIDEffort, "the model has no effort axis")
	})

	t.Run("a value the server did not take stays unresolved", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		// The server takes the write and keeps its old model.
		rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/profile"), fakeKapReply{Data: map[string]any{}})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
		assert.Equal(t, "kimi-k2", result.SurfacedOptions[agent.OptionIDModel])
	})
}

func TestKimiAutoThinking(t *testing.T) {
	t.Parallel()

	enabled, disabled := true, false
	for _, tc := range []struct {
		name     string
		defaults kimiThinkingDefaults
		model    string
		want     string
	}{
		{"the configured level", kimiThinkingDefaults{Enabled: &enabled, Effort: "low"}, "ladder", "low"},
		{"a configured level the model lacks", kimiThinkingDefaults{Effort: "ultra"}, "ladder", kimiThinkingOn},
		{"thinking switched off", kimiThinkingDefaults{Enabled: &disabled, Effort: "low"}, "ladder", kimiThinkingOff},
		{"thinking off on a model that always thinks", kimiThinkingDefaults{Enabled: &disabled}, "always", kimiThinkingOn},
		{"no configuration", kimiThinkingDefaults{}, "ladder", kimiThinkingOn},
		{"a model that cannot think", kimiThinkingDefaults{Effort: "low"}, "plain", ""},
		{"a model the catalog lacks", kimiThinkingDefaults{Effort: "low"}, "unknown", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineKimiAgent(t, &agenttest.Sink{})
			a.catalog = buildKimiCatalog([]kimiModelItem{
				{Model: "ladder", Capabilities: []string{"thinking"}, SupportEfforts: []string{"low", "high"}},
				{Model: "always", Capabilities: []string{"always_thinking"}},
				{Model: "plain"},
			}, "")
			a.catalog.thinking = tc.defaults
			assert.Equal(t, tc.want, a.autoThinking(tc.model))
		})
	}
}

func TestKimiStatusUpdated(t *testing.T) {
	t.Parallel()

	t.Run("plan mode the model entered moves the permission axis", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "planMode": true})
		assert.Equal(t, contracts.KimiModePlan, rig.sink.PermissionMode())
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "planMode": false})
		assert.Equal(t, contracts.KimiModeManual, rig.sink.PermissionMode(), "leaving plan mode returns to the mode under it")
		assert.Equal(t, []string{contracts.KimiModePlan, contracts.KimiModeManual}, rig.sink.PermissionModes())
	})

	t.Run("a model, a level and the swarm the server moved are refreshed", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.settings.effort = "low"
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "model": "kimi-text", "thinkingEffort": "high", "swarmMode": true})
		refresh := rig.sink.LastSettingsRefresh()
		assert.Equal(t, "kimi-text", refresh.Model)
		assert.Equal(t, "high", refresh.Effort)
		assert.Equal(t, kimiSwarmOn, refresh.Options[kimiOptionSwarmMode])
		assert.Empty(t, rig.sink.PermissionModes(), "the permission axis did not move")
	})

	t.Run("Auto stays Auto whatever level the server runs", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "thinkingEffort": "high"})
		assert.Zero(t, rig.sink.SettingsRefreshCount())
		rig.agent.Mu.Lock()
		effort := rig.agent.settings.effort
		rig.agent.Mu.Unlock()
		assert.Equal(t, agent.EffortAuto, effort)
	})

	t.Run("a subagent's status is not the session's", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentStatusUpdated, "agentId": "agent-0", "planMode": true, "model": "kimi-text"})
		assert.Empty(t, rig.sink.PermissionModes())
		assert.Zero(t, rig.sink.SettingsRefreshCount())
	})

	t.Run("a step's usage updates the readout", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStepCompleted, "turnId": 0,
			"usage": map[string]any{"inputOther": 7, "output": 3, "inputCacheRead": 90, "inputCacheCreation": 0}})
		usage, ok := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		require.True(t, ok)
		fields, ok := usage.(map[string]any)
		require.True(t, ok)
		assert.NotEmpty(t, fields)

		before := rig.sink.SessionInfoCount()
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStepCompleted, "turnId": 0})
		assert.Equal(t, before, rig.sink.SessionInfoCount(), "a step with no usage broadcasts nothing")
	})

	t.Run("a subagent's step usage is not the session's readout", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		before := rig.sink.SessionInfoCount()
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStepCompleted, "agentId": "agent-0", "turnId": 0,
			"usage": map[string]any{"inputOther": 7, "output": 3}})
		assert.Equal(t, before, rig.sink.SessionInfoCount())
		rig.agent.Mu.Lock()
		usage := rig.agent.contextUsage
		rig.agent.Mu.Unlock()
		assert.Nil(t, usage)
	})
}

func TestKimiApplyStatus(t *testing.T) {
	t.Parallel()
	a := newOfflineKimiAgent(t, &agenttest.Sink{})

	a.applyStatus(kimiStatus{ThinkingLevel: "high", PlanMode: true, SwarmMode: true})
	assert.Equal(t, kimiSettings{model: "kimi-k2", effort: agent.EffortAuto, permission: "manual", planMode: true, swarmMode: true}, a.settings,
		"an empty model and permission keep theirs, and Auto stays Auto")
	assert.Nil(t, a.contextUsage, "a status with no context states no readout")

	a.settings.effort = "low"
	a.applyStatus(kimiStatus{Model: "kimi-text", ThinkingLevel: "high", Permission: "yolo", ContextTokens: 5, MaxContextTokens: 10})
	assert.Equal(t, kimiSettings{model: "kimi-text", effort: "high", permission: "yolo"}, a.settings,
		"the plan and swarm switches always take the status's value")
	assert.EqualValues(t, 5, a.contextUsage[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 10, a.contextUsage[contracts.ContextUsageFieldContextWindow])
}

func TestKimiFoldStatus(t *testing.T) {
	t.Parallel()

	t.Run("reports only the axes that moved", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineKimiAgent(t, sink)
		a.foldStatus(kimiStatus{Model: "kimi-k2", Permission: "manual"})
		assert.Zero(t, sink.SettingsRefreshCount())
		assert.Empty(t, sink.PermissionModes())
		assert.Zero(t, sink.SessionInfoCount(), "a status with no context broadcasts nothing")

		a.foldStatus(kimiStatus{Model: "kimi-text", Permission: "manual", PlanMode: true, ContextTokens: 7})
		assert.Equal(t, []string{contracts.KimiModePlan}, sink.PermissionModes())
		require.Equal(t, 1, sink.SettingsRefreshCount())
		refresh := sink.LastSettingsRefresh()
		assert.Equal(t, "kimi-text", refresh.Model)
		assert.Empty(t, refresh.Effort, "Auto stays Auto")
		assert.Empty(t, refresh.Options, "the swarm switch did not move")
		usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		require.True(t, ok)
		fields, ok := usage.(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 7, fields[contracts.ContextUsageFieldContextTokens])
	})
}

func TestKimiSettingsFromOptions(t *testing.T) {
	t.Parallel()

	base := kimiSettings{model: "a", effort: "low", permission: "yolo", planMode: true, swarmMode: true}
	assert.Equal(t, base, kimiSettingsFromOptions(base, nil), "an empty option set keeps every axis")
	assert.Equal(t, kimiSettings{model: "b", effort: "high", permission: "auto", swarmMode: false},
		kimiSettingsFromOptions(base, options(agent.OptionIDModel, "b", agent.OptionIDEffort, "high",
			agent.OptionIDPermissionMode, "auto", kimiOptionSwarmMode, "off")))
	plan := kimiSettingsFromOptions(kimiSettings{permission: "yolo"}, options(agent.OptionIDPermissionMode, "plan"))
	assert.Equal(t, kimiSettings{permission: "yolo", planMode: true}, plan)
	assert.True(t, kimiSettingsFromOptions(kimiSettings{}, options(kimiOptionSwarmMode, "on")).swarmMode)
	assert.True(t, kimiSettingsFromOptions(kimiSettings{swarmMode: true}, options(kimiOptionSwarmMode, "bogus")).swarmMode,
		"an unknown swarm value keeps the axis")
}

func TestKimiProfileConfig(t *testing.T) {
	t.Parallel()
	a := newOfflineKimiAgent(t, &agenttest.Sink{})

	want := kimiSettings{model: "a", effort: "high", permission: "yolo", planMode: true}
	assert.Equal(t, map[string]any{"model": "a", "thinking": "high", "permission_mode": "yolo", "plan_mode": true, "swarm_mode": false},
		a.profileConfig(want, nil), "a first profile states every axis")
	assert.Empty(t, a.profileConfig(want, &want), "no difference, no write")
	auto := kimiSettings{model: "a", effort: agent.EffortAuto}
	assert.NotContains(t, a.profileConfig(auto, nil), "thinking", "Auto sends no level")
}
