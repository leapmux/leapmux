package cline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// updates returns the `updates` of every session.update_connection.
func updates(t *testing.T, hub *fakeHub) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, command := range hub.commandsNamed(commandUpdateConnection) {
		var u map[string]any
		require.True(t, command.field("updates", &u), "the fields travel inside `updates`")
		out = append(out, u)
	}
	return out
}

func TestOptionGroupsStateEveryAxis(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	current := agent.CurrentOptions(r.agent.OptionGroups())
	assert.Equal(t, testModel, current[agent.OptionIDModel])
	assert.Equal(t, agent.EffortAuto, current[agent.OptionIDEffort])
	assert.Equal(t, contracts.ClinePermissionModeAct, current[agent.OptionIDPermissionMode])
	snapshot := r.agent.SettingsSnapshot()
	assert.True(t, snapshot.AppliedLive)
	assert.Equal(t, current, map[string]string(snapshot.SurfacedOptions))
}

func TestTheConfiguredModelLeadsTheCatalog(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.selection = providerSelection{Provider: "openrouter", Model: "vendor/private-model"}
	})
	models := r.agent.sessionModels()
	require.Len(t, models, 1, "a provider the table lacks offers the configured model alone")
	assert.Equal(t, "vendor/private-model", models[0].Id)
	assert.True(t, models[0].IsDefault)
	assert.Equal(t, "vendor/private-model", agent.CurrentOptions(r.agent.OptionGroups())[agent.OptionIDModel])
}

func TestLaunchSettingsFallBack(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDModel, "not-a-model", agent.OptionIDEffort, "not-an-effort", agent.OptionIDPermissionMode, "yolo")
	})
	settings := r.agent.settings
	assert.Equal(t, testModel, settings.model, "an unknown model runs the configured one")
	assert.Equal(t, agent.EffortAuto, settings.effort)
	assert.Equal(t, contracts.ClinePermissionModeAct, settings.permissionMode)

	r = newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDModel, agent.DefaultModelSentinel)
	})
	assert.Equal(t, testModel, r.agent.settings.model, "the account default runs the configured model")

	r = newRig(t, func(c *rigConfig) { c.selection = providerSelection{Provider: "anthropic"} })
	assert.Equal(t, "claude-opus-5-5", r.agent.settings.model, "no configured model runs the provider's first")
}

func TestTheSessionStartsWithTheLaunchSettings(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDModel, "gpt-5.5", agent.OptionIDEffort, "high", agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	create := r.hub.commandsNamed(commandSessionCreate)[0]
	var config map[string]any
	require.True(t, create.field("sessionConfig", &config))
	assert.Equal(t, map[string]any{"providerId": testProvider, "modelId": "gpt-5.5"}, config)
	var runtime map[string]any
	require.True(t, create.field("runtimeOptions", &runtime))
	assert.Equal(t, sessionModePlan, runtime["mode"])
	assert.Equal(t, "high", runtime["reasoningEffort"])
	assert.Equal(t, true, runtime["thinking"])
	assert.Equal(t, true, runtime["enableSpawn"])
	assert.Equal(t, true, runtime["enableTeams"])
	contributions := runtime["clientContributions"].([]any)
	require.Len(t, contributions, 2, "Plan adds the plan tool beside the question executor")
	assert.Equal(t, contracts.ClineToolSwitchToActMode, contributions[1].(map[string]any)["name"])
	var policies map[string]any
	require.True(t, create.field("toolPolicies", &policies))
	assert.Equal(t, map[string]any{"*": map[string]any{"autoApprove": false}}, policies, "every tool asks the worker")
}

func TestAModelChangeAppliesAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.agent.UpdateSettings(options(agent.OptionIDModel, "gpt-5.5"))
	assert.Equal(t, []map[string]any{{"modelId": "gpt-5.5"}}, updates(t, r.hub))
	settlement := result.Settlements[agent.OptionIDModel]
	assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State)
	assert.Equal(t, "gpt-5.5", *settlement.Value)
	assert.Equal(t, "gpt-5.5", r.sink.LastSettingsRefresh().Model)
	assert.Empty(t, r.hub.commandsNamed(commandSessionDetach), "no rebuild")
}

func TestAnEffortChangeAppliesAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.UpdateSettings(options(agent.OptionIDEffort, "high"))
	r.agent.UpdateSettings(options(agent.OptionIDEffort, effortOff))
	assert.Equal(t, []map[string]any{
		{"reasoningEffort": "high", "thinking": true},
		{"thinking": false},
	}, updates(t, r.hub), "Off turns the reasoning off, which Cline's effort field refuses")
}

func TestAReturnToAutoEffortRebuildsTheSession(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.opts.Options = options(agent.OptionIDEffort, "high") })
	r.hub.store(r.sessionID(), []any{map[string]any{"role": "user", "content": "Hi."}})
	result := r.agent.UpdateSettings(options(agent.OptionIDEffort, agent.EffortAuto))
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
	require.Len(t, r.hub.commandsNamed(commandSessionDetach), 1)
	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 2)
	var runtime map[string]any
	require.True(t, creates[1].field("runtimeOptions", &runtime))
	assert.NotContains(t, runtime, "reasoningEffort", "Auto sends no reasoning setting")
	assert.NotContains(t, runtime, "thinking")
}

func TestAModeChangeRebuildsTheSessionWithItsConversation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.sessionID()
	r.hub.store(sessionID, []any{map[string]any{"role": "user", "content": "Hi."}})
	r.hub.storeCompaction(sessionID, map[string]any{"summary": "before"})
	result := r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan))
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDPermissionMode].State)

	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 2)
	rebuilt := creates[1]
	var config map[string]any
	require.True(t, rebuilt.field("sessionConfig", &config))
	assert.Equal(t, sessionID, config["sessionId"], "the session keeps its id")
	var messages []any
	require.True(t, rebuilt.field("initialMessages", &messages))
	assert.Len(t, messages, 1)
	var compaction map[string]any
	require.True(t, rebuilt.field("initialCompactionState", &compaction))
	assert.Equal(t, "before", compaction["summary"])
	var runtime map[string]any
	require.True(t, rebuilt.field("runtimeOptions", &runtime))
	assert.Equal(t, sessionModePlan, runtime["mode"])
	assert.Equal(t, sessionID, r.sessionID())
}

// configExtensionsOf reads runtimeOptions.configExtensions of one
// session.create, and reports whether the command states the field.
func configExtensionsOf(t *testing.T, create fakeCommand) ([]string, bool) {
	t.Helper()
	var runtime map[string]json.RawMessage
	require.True(t, create.field("runtimeOptions", &runtime))
	raw, present := runtime["configExtensions"]
	if !present {
		return nil, false
	}
	var extensions []string
	require.NoError(t, json.Unmarshal(raw, &extensions))
	return extensions, true
}

// In Plan and Act the session loads no code of the workspace or of the user:
// Cline runs hooks and plugins with no approval, so a mode that asks before a
// command must not load them. Auto-approve keeps Cline's own default.
func TestEachModeStatesTheExtensionsItLoads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode       string
		extensions []string
	}{
		{contracts.ClinePermissionModePlan, []string{"rules", "skills", "workflows"}},
		{contracts.ClinePermissionModeAct, []string{"rules", "skills", "workflows"}},
		{contracts.ClinePermissionModeAutoApprove, nil},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) {
				c.opts.Options = options(agent.OptionIDPermissionMode, tc.mode)
			})
			extensions, present := configExtensionsOf(t, r.hub.commandsNamed(commandSessionCreate)[0])
			assert.Equal(t, tc.extensions != nil, present, "only a mode that asks narrows the extensions")
			assert.Equal(t, tc.extensions, extensions)
			assert.NotContains(t, extensions, "plugins")
			assert.NotContains(t, extensions, "hooks")
		})
	}
}

// Act and Auto-approve load different extensions, and Cline fixes them at
// session.create, so a move between them builds the session again.
func TestAMoveBetweenActAndAutoApproveRebuildsWithTheOtherExtensions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.sessionID()
	r.hub.store(sessionID, []any{map[string]any{"role": "user", "content": "Hi."}})
	result := r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove))
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDPermissionMode].State)
	require.Len(t, r.hub.commandsNamed(commandSessionDetach), 1)
	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 2)
	_, present := configExtensionsOf(t, creates[1])
	assert.False(t, present, "Auto-approve takes Cline's default extensions")
	assert.Equal(t, contracts.ClinePermissionModeAutoApprove, r.agent.settings.permissionMode)

	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAct))
	creates = r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 3)
	extensions, present := configExtensionsOf(t, creates[2])
	assert.True(t, present)
	assert.Equal(t, []string{"rules", "skills", "workflows"}, extensions, "the return to Act drops the hooks and the plugins again")
	assert.Equal(t, sessionID, r.sessionID())
}

// The mode descriptions state what each mode runs without asking, and that the
// modes that ask run no hook or plugin.
func TestTheModeDescriptionsStateWhatRunsWithoutAsking(t *testing.T) {
	t.Parallel()
	describe := func(mode string) string {
		for _, def := range permissionModes {
			if def.Id == mode {
				return def.Description
			}
		}
		t.Fatalf("no mode %s", mode)
		return ""
	}
	for _, mode := range []string{contracts.ClinePermissionModePlan, contracts.ClinePermissionModeAct} {
		description := describe(mode)
		assert.Contains(t, description, "web fetch", "%s asks before a web fetch", mode)
		assert.Contains(t, description, "hooks and plugins do not run", mode)
	}
	assert.Contains(t, describe(contracts.ClinePermissionModeAutoApprove), "hooks and plugins")
}

func TestAModeChangeDuringATurnWaitsForItsEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	requestID := r.startTurn(t, "Hello.")
	r.hub.store(r.sessionID(), []any{})
	result := r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan, agent.OptionIDModel, "gpt-5.5"))
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDPermissionMode].State, "the mode waits for the turn")
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDModel].State, "the model applies at once")
	assert.Empty(t, r.hub.commandsNamed(commandSessionDetach), "no rebuild inside a turn")
	refresh := r.sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.5", refresh.Model)
	assert.Empty(t, refresh.PermissionMode, "the waiting mode keeps the value the user asked for")

	r.emit(contracts.ClineEventRunCompleted, map[string]any{"reason": "completed"})
	r.hub.reply(requestID, fakeReply{})
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandSessionCreate)) == 2 }, "the turn's end rebuilds the session")
	waitFor(t, func() bool { return !r.turnActive() }, "the rebuild releases the input queue")
	assert.Equal(t, contracts.ClinePermissionModePlan, r.agent.settings.permissionMode)
	assert.Contains(t, r.sink.PermissionModes(), contracts.ClinePermissionModePlan, "the applied mode is reported")
}

func TestAnInvalidValueChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.agent.UpdateSettings(options(agent.OptionIDModel, "not-a-model", agent.OptionIDPermissionMode, "yolo"))
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDPermissionMode].State)
	assert.Empty(t, r.hub.commandsNamed(commandUpdateConnection))
	assert.Equal(t, testModel, r.agent.settings.model)
	assert.Equal(t, testModel, r.sink.LastSettingsRefresh().Model, "the stored value is corrected to the one that runs")
}

func TestAnEffortTheNewModelLacksFallsBackToAuto(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.selection = providerSelection{Provider: "anthropic", Model: "claude-opus-5"}
		c.opts.Options = options(agent.OptionIDEffort, "xhigh")
	})
	r.hub.store(r.sessionID(), []any{})
	r.agent.UpdateSettings(options(agent.OptionIDModel, "claude-haiku-4-5"))
	assert.Equal(t, agent.EffortAuto, r.agent.settings.effort)
	assert.Equal(t, "claude-haiku-4-5", r.agent.settings.model)
}

func TestNeedsRebuild(t *testing.T) {
	t.Parallel()
	act := clineSettings{model: "m", effort: agent.EffortAuto, permissionMode: contracts.ClinePermissionModeAct}
	plan := act
	plan.permissionMode = contracts.ClinePermissionModePlan
	auto := act
	auto.permissionMode = contracts.ClinePermissionModeAutoApprove
	high := act
	high.effort = "high"
	assert.True(t, act.needsRebuild(plan, act.configExtensions()))
	assert.True(t, plan.needsRebuild(auto, plan.configExtensions()))
	assert.True(t, act.needsRebuild(auto, act.configExtensions()), "Auto-approve loads other extensions")
	assert.True(t, auto.needsRebuild(act, auto.configExtensions()))
	assert.False(t, act.needsRebuild(high, act.configExtensions()))
	assert.True(t, high.needsRebuild(act, high.configExtensions()))

	// A move between Act and Auto-approve during a turn applies the policy at
	// once, and the runtime keeps the extensions of the mode that built it.
	assert.True(t, act.needsRebuild(act, auto.configExtensions()), "the runtime still loads Auto-approve's extensions")
	assert.False(t, act.needsRebuild(auto, auto.configExtensions()), "the runtime already loads Auto-approve's extensions")
}

func TestReasoningFields(t *testing.T) {
	t.Parallel()
	assert.Nil(t, reasoningFields(""))
	assert.Nil(t, reasoningFields(agent.EffortAuto))
	assert.Equal(t, map[string]any{"thinking": false}, reasoningFields(effortOff))
	assert.Equal(t, map[string]any{"thinking": true, "reasoningEffort": "max"}, reasoningFields("max"))
}

// A move from Auto-approve to Act during a turn asks before the next call at
// once, and builds the session without hooks and plugins when the turn ends:
// Cline cannot change a running session's extensions.
func TestAMoveToActDuringATurnAsksAtOnceAndDropsTheExtensionsAtItsEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
	})
	requestID := r.startTurn(t, "Hello.")
	r.hub.store(r.sessionID(), []any{})
	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAct))
	r.feed(t, contracts.ClineEventApprovalRequested, approval("act-now", "editor"))
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "Act asks before the next call of the running turn")
	assert.Empty(t, r.hub.commandsNamed(commandSessionDetach), "no rebuild inside a turn")

	r.endRun(t, requestID, "completed")
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandSessionCreate)) == 2 }, "the turn's end builds the session again")
	extensions, present := configExtensionsOf(t, r.hub.commandsNamed(commandSessionCreate)[1])
	assert.True(t, present)
	assert.Equal(t, []string{"rules", "skills", "workflows"}, extensions, "the new runtime loads no hook and no plugin")
	waitFor(t, func() bool { return !r.turnActive() }, "the rebuild releases the input queue")
}

// A rebuild between two turns holds the input queue: a message sent while the
// session is detached and built again waits as for a running turn.
func TestASendDuringARebuildIsRefusedAsBusy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.store(r.sessionID(), []any{})
	detached := make(chan string, 1)
	r.hub.handle(commandSessionDetach, func(c fakeCommand) fakeReply {
		detached <- c.RequestID
		return fakeReply{Hold: true}
	})
	updated := make(chan struct{})
	go func() {
		defer close(updated)
		r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan))
	}()
	detachID := <-detached
	err := r.agent.SendInput("Hi.", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy, "the message waits for the rebuild")
	var busy *agent.AgentBusyError
	require.ErrorAs(t, err, &busy)
	assert.False(t, busy.ActiveTurnSteerable, "no run takes a steer during the rebuild")
	require.ErrorIs(t, r.agent.SteerInput("Hi.", nil), agent.ErrAgentBusy)
	require.NoError(t, r.agent.Interrupt())
	assert.Empty(t, r.hub.commandsNamed(commandRunAbort), "the detached session has no run to abort")
	assert.Empty(t, r.hub.commandsNamed(commandSessionSendInput), "nothing reaches the detached runtime")
	r.hub.reply(detachID, fakeReply{})
	<-updated
	assert.False(t, r.turnActive(), "the rebuild releases the input queue")
	require.NoError(t, r.agent.SendInput("Hi.", nil), "the next message runs on the new runtime")
}

// A move from Auto-approve to Act and back during one turn leaves the runtime
// as it was built: the user's last choice needs no new runtime, so the turn's
// end builds none.
func TestAMoveToActAndBackDuringATurnBuildsNoNewRuntime(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
	})
	requestID := r.startTurn(t, "Hello.")
	r.hub.store(r.sessionID(), []any{})
	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAct))
	r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove))
	r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
	waitFor(t, func() bool { return !r.turnActive() }, "the turn ends")
	assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1, "the runtime already loads Auto-approve's extensions")
	assert.Empty(t, r.hub.commandsNamed(commandSessionDetach))
	r.agent.Mu.Lock()
	mode, pending := r.agent.settings.permissionMode, r.agent.modeRebuild
	r.agent.Mu.Unlock()
	assert.Equal(t, contracts.ClinePermissionModeAutoApprove, mode)
	assert.Nil(t, pending)
}

// A change during a turn joins the change that waits for the turn's end, so the
// rebuild at the end keeps it: a model picked after the plan's approval runs in
// Act mode.
func TestAChangeDuringATurnJoinsTheChangeThatWaitsForItsEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) {
		c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan)
	})
	requestID := r.startTurn(t, "Plan it.")
	r.hub.store(r.sessionID(), []any{})
	r.agent.setPlanExitMode(contracts.ClinePermissionModeAct)
	r.agent.UpdateSettings(options(agent.OptionIDModel, "gpt-5.5"))
	r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
	waitFor(t, func() bool { return len(r.hub.commandsNamed(commandSessionCreate)) == 2 }, "the turn's end builds the session in Act mode")
	create := r.hub.commandsNamed(commandSessionCreate)[1]
	var config struct {
		ModelID string `json:"modelId"`
	}
	require.True(t, create.field("sessionConfig", &config))
	assert.Equal(t, "gpt-5.5", config.ModelID, "the rebuild keeps the model picked during the turn")
	var runtime struct {
		Mode string `json:"mode"`
	}
	require.True(t, create.field("runtimeOptions", &runtime))
	assert.Equal(t, sessionModeAct, runtime.Mode)
}

// A change that Cline refuses does not apply: the settings keep the value that
// runs, the settlement stays unresolved, and the stored value is corrected to
// the one that runs.
func TestAChangeThatClineRefusesKeepsTheValueThatRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.handle(commandUpdateConnection, func(fakeCommand) fakeReply {
		return fakeReply{Code: "model_unavailable", Message: "no"}
	})
	result := r.agent.UpdateSettings(options(agent.OptionIDModel, "gpt-5.5", agent.OptionIDEffort, "high"))
	require.Len(t, r.hub.commandsNamed(commandUpdateConnection), 1, "one update carries both axes")
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
	assert.Equal(t, testModel, r.agent.settings.model)
	assert.Equal(t, agent.EffortAuto, r.agent.settings.effort)
	assert.Equal(t, testModel, r.sink.LastSettingsRefresh().Model)
}

// A rebuild between two turns that Cline refuses builds the session again with
// the settings it had, and releases the input queue: the agent keeps a session
// in its old mode.
func TestARefusedRebuildBetweenTurnsKeepsTheOldMode(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.sessionID()
	r.hub.store(sessionID, []any{})
	creates := 0
	r.hub.handle(commandSessionCreate, func(command fakeCommand) fakeReply {
		creates++
		if creates == 1 {
			return fakeReply{Code: "invalid_config", Message: "no"}
		}
		return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": command.SessionID}}}
	})
	result := r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan))
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDPermissionMode].State)
	assert.Equal(t, 2, creates, "the old settings build the session again")
	// The rig's first session, the refused rebuild, and the restore.
	all := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, all, 3)
	var refused, restored map[string]any
	require.True(t, all[1].field("runtimeOptions", &refused))
	assert.Equal(t, sessionModePlan, refused["mode"], "the rebuild asked for the new mode")
	require.True(t, all[2].field("runtimeOptions", &restored))
	assert.Equal(t, sessionModeAct, restored["mode"], "the restore takes the old mode")
	assert.Equal(t, contracts.ClinePermissionModeAct, r.agent.settings.permissionMode)
	assert.Equal(t, sessionID, r.sessionID())
	assert.False(t, r.turnActive(), "the failed rebuild releases the input queue")
	assert.Equal(t, contracts.ClinePermissionModeAct, r.sink.LastSettingsRefresh().PermissionMode, "the stored value is the one that runs")
	require.NoError(t, r.agent.SendInput("Hi.", nil), "the session in its old mode takes the next message")
}

// A request that states nothing changes nothing, and confirms every value.
func TestAnEmptyRequestConfirmsEveryValue(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.agent.UpdateSettings(options())
	for key, settlement := range result.Settlements {
		assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State, key)
	}
	assert.Empty(t, r.hub.commandsNamed(commandUpdateConnection))
	assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1)
}

// Each step of a rebuild can fail. A failure before the detach leaves the
// runtime as it was; a failure after it restores the runtime with the settings
// that it had; a compaction state that cannot be read leaves the conversation
// alone to carry the session.
func TestRebuildSessionFailsSafely(t *testing.T) {
	t.Parallel()
	plan := clineSettings{model: testModel, effort: agent.EffortAuto, permissionMode: contracts.ClinePermissionModePlan}
	refuse := func(fakeCommand) fakeReply { return fakeReply{Code: "internal_error", Message: "no"} }
	t.Run("the conversation cannot be read", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		err := r.agent.rebuildSession(plan)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the conversation to rebuild")
		assert.Empty(t, r.hub.commandsNamed(commandSessionDetach), "nothing detaches")
		assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1)
	})
	t.Run("the detach fails", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.hub.store(r.sessionID(), []any{})
		r.hub.handle(commandSessionDetach, refuse)
		err := r.agent.rebuildSession(plan)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "detach the session to rebuild")
		assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1, "no second runtime of the session starts")
		assert.Equal(t, contracts.ClinePermissionModeAct, r.agent.settings.permissionMode)
	})
	t.Run("the compaction state cannot be read", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.hub.store(r.sessionID(), []any{map[string]any{"role": "user", "content": "Hi."}})
		r.hub.handle(commandSessionCompactionGet, refuse)
		require.NoError(t, r.agent.rebuildSession(plan))
		creates := r.hub.commandsNamed(commandSessionCreate)
		require.Len(t, creates, 2)
		var messages []any
		assert.True(t, creates[1].field("initialMessages", &messages), "the conversation carries the session")
		var compaction any
		assert.False(t, creates[1].field("initialCompactionState", &compaction))
		assert.Equal(t, contracts.ClinePermissionModePlan, r.agent.settings.permissionMode)
	})
	t.Run("the rebuild and the restore fail", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.hub.store(r.sessionID(), []any{})
		r.hub.handle(commandSessionCreate, refuse)
		err := r.agent.rebuildSession(plan)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rebuild the session")
		assert.Contains(t, err.Error(), "restore it")
		assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 3, "the first session, the rebuild and the restore")
		assert.Equal(t, contracts.ClinePermissionModeAct, r.agent.settings.permissionMode)
	})
	t.Run("the rebuild fails and the restore holds", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, func(c *rigConfig) {
			c.opts.Options = options(agent.OptionIDPermissionMode, contracts.ClinePermissionModeAutoApprove)
		})
		r.hub.store(r.sessionID(), []any{})
		creates := 0
		r.hub.handle(commandSessionCreate, func(command fakeCommand) fakeReply {
			creates++
			if creates == 1 {
				return fakeReply{Code: "invalid_config", Message: "no"}
			}
			return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": command.SessionID}}}
		})
		require.Error(t, r.agent.rebuildSession(plan))
		r.agent.Mu.Lock()
		loaded := r.agent.loadedExtensions
		r.agent.Mu.Unlock()
		assert.Nil(t, loaded, "the restored runtime loads the extensions of Auto-approve again")
		_, present := configExtensionsOf(t, r.hub.commandsNamed(commandSessionCreate)[2])
		assert.False(t, present)
	})
}
