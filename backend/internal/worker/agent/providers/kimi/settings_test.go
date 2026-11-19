package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKimiReadsStatusFromNativeSubscription(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	fake.store("session_1", fakeKapSession{Model: "kimi-k2", Thinking: "high", Permission: "auto", PlanMode: true, Busy: true})
	fake.reply("GET "+kimiSessionPath("session_1", "/status"), fakeKapReply{HTTPStatus: 404, Msg: "the status route does not exist"})
	a, err := connectOffline(t, server.URL, fakeKapToken)
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	status, err := a.readStatus(ctx, "session_1")
	require.NoError(t, err)
	assert.True(t, status.Busy)
	assert.Equal(t, "kimi-k2", status.Model)
	assert.Equal(t, "high", status.ThinkingLevel)
	assert.Equal(t, "auto", status.Permission)
	assert.True(t, status.PlanMode)
	// Agent state can precede usage after the subscription response. A later
	// usage frame updates the same subscription without requiring another one.
	fake.push(t, map[string]any{
		"type": "usage", "session_id": "session_1", "agent_id": "main",
		"context_tokens": 0, "max_context_tokens": 4096,
	})
	_, err = a.stream.waitSessionStatus(ctx, "session_1", func(value kimiStatus) bool {
		return value.HasContextUsage && value.ContextTokens == 0 && value.MaxContextTokens == 4096
	})
	require.NoError(t, err)
	status, err = a.readStatus(ctx, "session_1")
	require.NoError(t, err)
	assert.Equal(t, kimiStatus{
		HasContextUsage: true,
		Busy:            true, Model: "kimi-k2", ThinkingLevel: "high", Permission: "auto", PlanMode: true,
		ContextTokens: 0, MaxContextTokens: 4096,
	}, status)
	fake.mu.Lock()
	subscriptions := len(fake.subscribes)
	fake.mu.Unlock()
	assert.Equal(t, 1, subscriptions)

	assert.Empty(t, fake.requestsTo("GET "+kimiSessionPath("session_1", "/status")))
}

func TestKimiReportedZeroContextClearsPreviousUsage(t *testing.T) {
	t.Parallel()
	a := newOfflineKimiAgent(t, &agenttest.Sink{})
	a.applyStatus(kimiStatus{ContextTokens: 9, MaxContextTokens: 100})
	a.applyStatus(kimiStatus{HasContextUsage: true})
	assert.EqualValues(t, 0, a.contextUsage[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 0, a.contextUsage[contracts.ContextUsageFieldContextWindow])
}

func TestKimiNativeAgentStateUpdatesLiveOptions(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.catalog = buildKimiCatalog([]kimiModelItem{{Model: "kimi-k2"}, {Model: "kimi-text"}}, "kimi-k2")
	rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","model":"kimi-text","thinking":"off","permission_mode":"auto","plan_mode":true,"swarm_mode":true,"tower_mode":false}`))
	current := agent.CurrentOptions(rig.agent.OptionGroups())
	assert.Equal(t, "kimi-text", current[agent.OptionIDModel])
	assert.Equal(t, contracts.KimiModePlan, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kimiSwarmOn, current[kimiOptionSwarmMode])
	rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","model":"kimi-k2","thinking":"high","permission_mode":"yolo","plan_mode":false,"swarm_mode":false,"tower_mode":false}`))
	current = agent.CurrentOptions(rig.agent.OptionGroups())
	assert.Equal(t, "kimi-k2", current[agent.OptionIDModel])
	assert.Equal(t, contracts.KimiModeYolo, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kimiSwarmOff, current[kimiOptionSwarmMode])
}

func TestKimiNativeUsageUpdatesContextWithoutChangingModes(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.applyStatus(kimiStatus{PlanMode: true, SwarmMode: true, ContextTokens: 9, MaxContextTokens: 100})
	raw := []byte(`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":0,"max_context_tokens":4096}`)
	rig.agent.HandleOutput(raw)
	value, exists := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, exists)
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 0, usage[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 4096, usage[contracts.ContextUsageFieldContextWindow])
	current := agent.CurrentOptions(rig.agent.OptionGroups())
	assert.Equal(t, contracts.KimiModePlan, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kimiSwarmOn, current[kimiOptionSwarmMode])
	before := rig.sink.SessionInfoCount()
	rig.agent.HandleOutput(raw)
	assert.Equal(t, before, rig.sink.SessionInfoCount(), "an unchanged native usage snapshot adds no broadcast")
}

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
		assert.Equal(t, map[string]any{"model": "kimi-text", "thinking": "off"}, lastProfile(t, rig),
			"the refused level is not sent; the model switch clears the level the old model ran")
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

	t.Run("a model switch on Auto clears the level the old model ran", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		assert.Equal(t, map[string]any{"model": "kimi-text", "thinking": "off"}, lastProfile(t, rig),
			"the server keeps the previous model's level unless the switch restates it")
		assert.Equal(t, optionmap.Map{agent.OptionIDModel: "kimi-text"}, result.ConfirmedOptions())
	})

	t.Run("a model switch on Auto restates the configured level", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDModel, "kimi-text")})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-k2"})
		assert.Equal(t, map[string]any{"model": "kimi-k2", "thinking": "medium"}, lastProfile(t, rig),
			"Auto stands for the level the user's configuration states")
		assert.Equal(t, optionmap.Map{agent.OptionIDModel: "kimi-k2"}, result.ConfirmedOptions())
	})

	t.Run("a model switch that keeps an explicit level restates nothing", func(t *testing.T) {
		t.Parallel()
		// The worker sends the model and the kept level together, because the new model
		// offers the level. The server keeps the level across the switch, so the write
		// states the model alone: the clearing of Auto must not touch a level that the
		// user chose.
		fake, server := newFakeKap(t)
		fake.models = append(fake.models, map[string]any{
			"model": "kimi-k3", "display_name": "Kimi K3", "max_context_size": 262144,
			"capabilities": []string{"thinking"}, "support_efforts": []string{"low", "medium", "high"},
		})
		rig := connectKimiTestRig(t, fake, server.URL, agent.Options{Options: options(agent.OptionIDEffort, "high")})
		before := profileCount(rig)
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-k3", agent.OptionIDEffort: "high"})
		require.Equal(t, before+1, profileCount(rig))
		assert.Equal(t, map[string]any{"model": "kimi-k3"}, lastProfile(t, rig),
			"the server keeps the level across the switch, and the new model takes it")
		assert.Equal(t, optionmap.Map{agent.OptionIDModel: "kimi-k3", agent.OptionIDEffort: "high"}, result.ConfirmedOptions())
		session, ok := fake.session(rig.sessionID())
		require.True(t, ok)
		assert.Equal(t, "high", session.Thinking, "the server still runs the level that the user kept")
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

	t.Run("a closed state stream confirms nothing", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		refreshes := rig.sink.SettingsRefreshCount()
		rig.agent.stream.close()
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text"})
		assert.True(t, result.AppliedLive)
		assert.Equal(t, map[string]any{"model": "kimi-text", "thinking": "off"}, lastProfile(t, rig), "the write went out")
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

	t.Run("switching to Auto on a model that cannot think clears the level", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{Options: options(agent.OptionIDEffort, "high")})
		result := rig.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "kimi-text", agent.OptionIDEffort: agent.EffortAuto})
		assert.Equal(t, map[string]any{"model": "kimi-text", "thinking": "off"}, lastProfile(t, rig),
			"a model that cannot think takes no level, and the previous model's level must not leak")
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
	t.Run("native plan mode moves the permission axis", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","permission_mode":"manual","plan_mode":true,"swarm_mode":false,"tower_mode":false}`))
		assert.Equal(t, contracts.KimiModePlan, rig.sink.PermissionMode())
		rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","permission_mode":"manual","plan_mode":false,"swarm_mode":false,"tower_mode":false}`))
		assert.Equal(t, contracts.KimiModeManual, rig.sink.PermissionMode())
		assert.Equal(t, []string{contracts.KimiModePlan, contracts.KimiModeManual}, rig.sink.PermissionModes())
	})
	t.Run("native model effort and swarm changes refresh their axes", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.settings.effort = "low"
		rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","model":"kimi-text","thinking":"high","permission_mode":"manual","plan_mode":false,"swarm_mode":true,"tower_mode":false}`))
		refresh := rig.sink.LastSettingsRefresh()
		assert.Equal(t, "kimi-text", refresh.Model)
		assert.Equal(t, "high", refresh.Effort)
		assert.Equal(t, kimiSwarmOn, refresh.Options[kimiOptionSwarmMode])
		assert.Empty(t, rig.sink.PermissionModes())
	})
	t.Run("Auto remains Auto when the native thinking level changes", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"main","status":"idle","thinking":"high","permission_mode":"manual","plan_mode":false,"swarm_mode":false,"tower_mode":false}`))
		assert.Zero(t, rig.sink.SettingsRefreshCount())
		rig.agent.Mu.Lock()
		effort := rig.agent.settings.effort
		rig.agent.Mu.Unlock()
		assert.Equal(t, agent.EffortAuto, effort)
	})
	t.Run("a linked child's native state does not change the session", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		adoptNativeKimiChild(t, rig.agent, "agent-0")
		rig.agent.HandleOutput([]byte(`{"type":"agent","compacting":false,"session_id":"session_1","agent_id":"agent-0","status":"running","model":"kimi-text","thinking":"high","permission_mode":"auto","plan_mode":true,"swarm_mode":true,"tower_mode":false}`))
		assert.Empty(t, rig.sink.PermissionModes())
		assert.Zero(t, rig.sink.SettingsRefreshCount())
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

func TestKimiPublicStepUsageUsesTheNewestOwnedMainRequest(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":120,"max_context_tokens":262144}`))
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	readout := func() map[string]any {
		t.Helper()
		value, found := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		require.True(t, found)
		fields, valid := value.(map[string]any)
		require.True(t, valid)
		return fields
	}
	usage := func(turn string, index int, input, output string) string {
		return fmt.Sprintf(`{"type":"turn.updated","turn_id":%q,"step":{"index":%d,"outcome":"completed","started_at":"2026-11-18T00:00:00Z","usage":{"input_other":%s,"output":%s,"input_cache_read":90,"input_cache_creation":0}}}`, turn, index, input, output)
	}
	facts := usage("t0", 2, "7", "3")
	rig.conversationFrame(t, facts)
	fields := readout()
	assert.EqualValues(t, 7, fields[contracts.ContextUsageFieldInputTokens])
	assert.EqualValues(t, 3, fields[contracts.ContextUsageFieldOutputTokens])
	assert.EqualValues(t, 90, fields[contracts.ContextUsageFieldCacheReadInputTokens])
	assert.EqualValues(t, 0, fields[contracts.ContextUsageFieldCacheCreationInputTokens])
	assert.EqualValues(t, 120, fields[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 262144, fields[contracts.ContextUsageFieldContextWindow])

	before := rig.sink.SessionInfoCount()
	rig.conversationFrame(t, facts)
	rig.conversationFrame(t, usage("t0", 1, "37", "8"))
	rig.conversationFrame(t, usage("t0", 3, "5.5", "1"))
	rig.conversationFrame(t, usage("t0", 4, "9007199254740992", "1"))
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"t0","step":{"index":5,"outcome":"completed","started_at":"2026-11-18T00:00:00Z"}}`)
	adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.started","turn":{"turn_id":"child-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationAgentFrame(t, "agent-0", usage("child-turn", 6, "63", "9"))
	assert.Equal(t, before, rig.sink.SessionInfoCount())
	assert.Equal(t, fields, readout())

	// Native end headers carry the request summaries even when their live updates
	// were absent. Their aggregate describes the whole turn.
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","usage":{"input_other":31,"output":8,"input_cache_read":90,"input_cache_creation":2},"steps":[{"index":2,"outcome":"completed","started_at":"2026-11-18T00:00:00Z","usage":{"input_other":7,"output":3,"input_cache_read":90,"input_cache_creation":0}},{"index":6,"outcome":"completed","started_at":"2026-11-18T00:00:01Z","usage":{"input_other":24,"output":5,"input_cache_read":0,"input_cache_creation":2}}]}}`)
	fields = readout()
	assert.EqualValues(t, 24, fields[contracts.ContextUsageFieldInputTokens])
	assert.EqualValues(t, 5, fields[contracts.ContextUsageFieldOutputTokens])
	assert.EqualValues(t, 2, fields[contracts.ContextUsageFieldCacheCreationInputTokens])
	rows := rig.sink.Messages()
	require.Len(t, rows, 1)
	stored, present := metadataField(t, rows[0], contracts.SessionInfoKeyContextUsage)
	require.True(t, present)
	assert.JSONEq(t, `{"context_tokens":120,"context_window":262144,"input_tokens":24,"output_tokens":5,"cache_creation_input_tokens":2,"cache_read_input_tokens":0}`, string(stored))

	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:02Z","steps":[]}}`)
	rig.conversationFrame(t, usage("t1", 1, "51", "7"))
	before = rig.sink.SessionInfoCount()
	rig.conversationFrame(t, usage("t0", 7, "99", "8"))
	assert.Equal(t, before, rig.sink.SessionInfoCount())
	assert.EqualValues(t, 51, readout()[contracts.ContextUsageFieldInputTokens])
}
