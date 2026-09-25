package ohmypi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// availableModels is omp 18.2.11's get_available_models response from probe s7,
// reduced to the fields the worker reads.
const availableModels = `{"models":[` +
	`{"id":"mock-model-2","name":"Mock Model Two","provider":"mock","reasoning":false,"thinking":null,"contextWindow":32000},` +
	`{"id":"mock-model","name":"Mock Model","provider":"mock","reasoning":true,"thinking":{"mode":"effort","efforts":["minimal","low","medium","high","xhigh"]},"contextWindow":128000}]}`

// withCatalog gives the rig's agent the probe's catalog.
func withCatalog(r *rig) *rig {
	r.agent.applyAvailableModels(json.RawMessage(availableModels))
	return r
}

// groupByID returns the option group with the given id, or nil.
func groupByID(groups []*leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOptionGroup {
	for _, group := range groups {
		if group.GetId() == id {
			return group
		}
	}
	return nil
}

func optionIDs(group *leapmuxv1.AvailableOptionGroup) []string {
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	return ids
}

func TestOptionGroupsWithACatalog(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	groups := r.agent.OptionGroups()

	model := groupByID(groups, agent.OptionIDModel)
	require.NotNil(t, model)
	assert.Equal(t, "mock/mock-model", model.GetCurrentValue())
	assert.Equal(t, []string{"mock/mock-model-2", "mock/mock-model"}, optionIDs(model), "a model is `<provider>/<id>`")

	effort := groupByID(groups, agent.OptionIDEffort)
	require.NotNil(t, effort)
	assert.Equal(t, ThinkingLevelLabel, effort.GetLabel())
	assert.Equal(t, "high", effort.GetCurrentValue())
	assert.Equal(t, []string{"auto", "xhigh", "high", "medium", "low", "minimal", "off"}, optionIDs(effort))

	approval := groupByID(groups, agent.OptionIDPermissionMode)
	require.NotNil(t, approval)
	assert.Equal(t, "write", approval.GetCurrentValue())
	assert.Equal(t, []string{"always-ask", "write", "yolo"}, optionIDs(approval))
}

func TestOptionGroupsWithoutACatalogReadBack(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	groups := r.agent.OptionGroups()
	model := groupByID(groups, agent.OptionIDModel)
	require.NotNil(t, model)
	assert.Equal(t, "mock/mock-model", model.GetCurrentValue())
	assert.NotNil(t, groupByID(groups, agent.OptionIDPermissionMode), "the approval mode is static")
}

func TestSettingsSnapshotConfirmsTheRunningValues(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	result := r.agent.SettingsSnapshot()
	assert.True(t, result.AppliedLive)
	assert.Equal(t, optionmap.Map{
		agent.OptionIDModel: "mock/mock-model", agent.OptionIDEffort: "high", agent.OptionIDPermissionMode: "write",
	}, result.ConfirmedOptions())
}

func TestUpdateSettingsSwitchesTheModelLive(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type != CommandSetModel {
			return nil
		}
		// omp's own order (probe s7): model_changed, then the clamped level, then the
		// response with the model it settled on.
		return &rigReply{
			Before: []string{`{"type":"model_changed"}`, `{"type":"thinking_level_changed"}`},
			Data:   json.RawMessage(`{"id":"mock-model-2","name":"Mock Model Two","provider":"mock","reasoning":false}`),
		}
	})

	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "mock/mock-model-2", agent.OptionIDEffort: "high"})
	assert.True(t, result.AppliedLive)
	commands := r.commandsOfType(CommandSetModel)
	require.Len(t, commands, 1)
	assert.Equal(t, "mock", commands[0].Payload["provider"])
	assert.Equal(t, "mock-model-2", commands[0].Payload["modelId"])
	assert.Equal(t, "mock/mock-model-2", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "off", result.ConfirmedOptions()[agent.OptionIDEffort], "a model that does not reason runs off")
	assert.Equal(t, "mock/mock-model-2", r.sink.LastSettingsRefresh().Model)
}

func TestUpdateSettingsRecordsTheClampedLevel(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.agent.Mu.Lock()
	r.agent.effectiveThinking = "high"
	r.agent.Mu.Unlock()
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type != CommandSetThinkingLevel {
			return nil
		}
		return &rigReply{Before: []string{`{"type":"thinking_level_changed","thinkingLevel":"xhigh"}`}}
	})
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "max"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "max", r.waitForCommand(CommandSetThinkingLevel, 1)[0].Payload["level"])
	assert.Equal(t, "xhigh", result.ConfirmedOptions()[agent.OptionIDEffort], "omp's settled level, not the request")
}

func TestUpdateSettingsKeepsALevelOmpDidNotMove(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.agent.Mu.Lock()
	r.agent.thinkingLevel = "low"
	r.agent.effectiveThinking = "off"
	r.agent.Mu.Unlock()
	// A model that does not reason clamps every level to "off", which is the level
	// it already runs, so omp announces nothing.
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "off", result.ConfirmedOptions()[agent.OptionIDEffort])
}

func TestUpdateSettingsLeavesAutoByRestarting(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDEffort: agent.EffortAuto})
	assert.False(t, result.AppliedLive, "no command unsets a level; the launch sends none")
	assert.Empty(t, r.commandsOfType(CommandSetThinkingLevel))
}

func TestUpdateSettingsFromAutoToALevelAppliesLive(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.agent.Mu.Lock()
	r.agent.thinkingLevel = agent.EffortAuto
	r.agent.effectiveThinking = "medium"
	r.agent.Mu.Unlock()
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type != CommandSetThinkingLevel {
			return nil
		}
		return &rigReply{Before: []string{`{"type":"thinking_level_changed","thinkingLevel":"low"}`}}
	})
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "low"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
}

func TestUpdateSettingsRestartsForAnApprovalMode(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: contracts.OhMyPiApprovalModeAlwaysAsk})
	assert.False(t, result.AppliedLive)
	assert.Contains(t, result.Settlements, agent.OptionIDPermissionMode)

	same := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: "write"})
	assert.True(t, same.AppliedLive, "the running mode needs no restart")
}

func TestUpdateSettingsRestartsWhenTheModelSwitchFails(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandSetModel {
			return &rigReply{Error: "Model not found: mock/does-not-exist"}
		}
		return nil
	})
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "mock/does-not-exist", agent.OptionIDEffort: "low"})
	assert.False(t, result.AppliedLive)
	assert.Empty(t, r.commandsOfType(CommandSetThinkingLevel), "nothing more is tried once the model failed")
	assert.Equal(t, "mock/mock-model", r.agent.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel], "the running values stay")
}

func TestUpdateSettingsRestoresAPartialApply(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.respond(func(command recordedCommand) *rigReply {
		switch command.Type {
		case CommandSetModel:
			return &rigReply{Data: json.RawMessage(`{"id":"mock-model-2","provider":"mock"}`)}
		case CommandSetThinkingLevel:
			return &rigReply{Error: "busy"}
		}
		return nil
	})
	result := r.agent.UpdateSettings(optionmap.Map{agent.OptionIDModel: "mock/mock-model-2", agent.OptionIDEffort: "low"})
	assert.False(t, result.AppliedLive)
	snapshot := r.agent.SettingsSnapshot().ConfirmedOptions()
	assert.Equal(t, "mock/mock-model", snapshot[agent.OptionIDModel], "no half-applied mix is observable before the restart")
	assert.Equal(t, "high", snapshot[agent.OptionIDEffort])
}

// A request that states the running values changes nothing, so it sends omp no
// command.
func TestUpdateSettingsWithTheRunningValuesSendsNothing(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	for _, options := range []optionmap.Map{
		{},
		{agent.OptionIDModel: "mock/mock-model", agent.OptionIDEffort: "high", agent.OptionIDPermissionMode: "write"},
	} {
		result := r.agent.UpdateSettings(options)
		assert.True(t, result.AppliedLive)
	}
	assert.Empty(t, r.commandsOfType(CommandSetModel))
	assert.Empty(t, r.commandsOfType(CommandSetThinkingLevel))
}

// omp answers set_model with the model it settled on, which can differ from the
// request: an alias, or a provider it resolved another way. The agent records
// omp's answer.
func TestApplyModelRecordsTheModelOmpSettledOn(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandSetModel {
			return &rigReply{Data: json.RawMessage(`{"id":"mock-model-2","provider":"mock","name":"Mock Model Two"}`)}
		}
		return nil
	})
	require.NoError(t, r.agent.applyModel("mock/two", r.agent.APITimeout()))
	assert.Equal(t, "two", r.waitForCommand(CommandSetModel, 1)[0].Payload["modelId"])
	r.agent.Mu.Lock()
	assert.Equal(t, "mock/mock-model-2", r.agent.model)
	r.agent.Mu.Unlock()

	// An answer that states no model leaves the request as the running model.
	r.respond(nil)
	require.NoError(t, r.agent.applyModel("mock/mock-model", r.agent.APITimeout()))
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	assert.Equal(t, "mock/mock-model", r.agent.model)
}

func TestApplyModelFindsTheProviderInTheCatalog(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	require.NoError(t, r.agent.applyModel("mock-model-2", r.agent.APITimeout()))
	command := r.waitForCommand(CommandSetModel, 1)[0]
	assert.Equal(t, "mock", command.Payload["provider"])
	assert.Equal(t, "mock-model-2", command.Payload["modelId"])

	assert.ErrorContains(t, r.agent.applyModel("unknown-model", r.agent.APITimeout()), "states no provider")
}

func TestThinkingLevelChanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		current   string
		frame     string
		want      string
		effective string
		published bool
	}{
		{name: "a clamped level", current: "high", frame: `{"type":"thinking_level_changed","thinkingLevel":"xhigh"}`, want: "xhigh", effective: "xhigh", published: true},
		{name: "a model with no thinking", current: "high", frame: `{"type":"thinking_level_changed"}`, want: "off", effective: "off", published: true},
		{name: "omp's own auto", current: "high", frame: `{"type":"thinking_level_changed","thinkingLevel":"high","configured":"auto"}`, want: agent.EffortAuto, effective: "high", published: true},
		{name: "the level it already runs", current: "high", frame: `{"type":"thinking_level_changed","thinkingLevel":"high"}`, want: "high", effective: "high"},
		{name: "the reader's Auto stays", current: agent.EffortAuto, frame: `{"type":"thinking_level_changed","thinkingLevel":"medium"}`, want: agent.EffortAuto, effective: "medium"},
		{name: "a garbled frame changes nothing", current: "high", frame: `{"type":"thinking_level_changed","thinkingLevel":7}`, want: "high", effective: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.agent.Mu.Lock()
			r.agent.thinkingLevel = tc.current
			r.agent.Mu.Unlock()
			r.emit(tc.frame)
			r.agent.Mu.Lock()
			got, effective := r.agent.thinkingLevel, r.agent.effectiveThinking
			r.agent.Mu.Unlock()
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.effective, effective)
			if tc.published {
				assert.Equal(t, tc.want, r.sink.LastSettingsRefresh().Effort)
				assert.Equal(t, "write", r.sink.LastSettingsRefresh().PermissionMode, "every axis rides along")
			} else {
				assert.Zero(t, r.sink.SettingsRefreshCount())
			}
		})
	}
}

func TestConfigUpdateFoldsASlashCommandBack(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"config_update","model":{"id":"mock-model-2","provider":"mock"},"thinkingLevel":"low"}`)
	refresh := r.sink.LastSettingsRefresh()
	assert.Equal(t, "mock/mock-model-2", refresh.Model)
	assert.Equal(t, "low", refresh.Effort)

	count := r.sink.SettingsRefreshCount()
	r.emit(`{"type":"config_update","model":{"id":"mock-model-2","provider":"mock"},"thinkingLevel":"low"}`)
	assert.Equal(t, count, r.sink.SettingsRefreshCount(), "an unchanged report publishes nothing")
}

// While the reader runs on Auto, a level that a slash command set is omp's own
// business: it moves the effective level, and the reader's choice stays Auto.
func TestConfigUpdateKeepsTheReadersAuto(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Mu.Lock()
	r.agent.thinkingLevel = agent.EffortAuto
	r.agent.Mu.Unlock()

	r.emit(`{"type":"config_update","thinkingLevel":"low"}`)
	assert.Zero(t, r.sink.SettingsRefreshCount(), "nothing the reader chose moved")
	r.agent.Mu.Lock()
	assert.Equal(t, agent.EffortAuto, r.agent.thinkingLevel)
	assert.Equal(t, "low", r.agent.effectiveThinking)
	r.agent.Mu.Unlock()

	r.emit(`{"type":"config_update","model":{"id":"mock-model-2","provider":"mock"},"thinkingLevel":"high"}`)
	refresh := r.sink.LastSettingsRefresh()
	assert.Equal(t, "mock/mock-model-2", refresh.Model)
	assert.Equal(t, agent.EffortAuto, refresh.Effort)
}

func TestConfigUpdateWithNothingToFoldChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"config_update"}`,
		`{"type":"config_update","model":{"id":"","provider":"mock"}}`,
		`{"type":"config_update","model":7}`,
	)
	assert.Zero(t, r.sink.SettingsRefreshCount())
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	assert.Equal(t, "mock/mock-model", r.agent.model)
	assert.Equal(t, "high", r.agent.thinkingLevel)
}

// A get_state that reports another session -- an extension replaced it -- moves
// the resume handle.
func TestAModelChangeThatReportsANewSessionMovesTheHandle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type != CommandGetState {
			return nil
		}
		return &rigReply{Data: json.RawMessage(`{"model":{"id":"mock-model","provider":"mock"},"thinkingLevel":"high","sessionId":"s-new","sessionFile":"/sessions/s-new.jsonl"}`)}
	})
	r.emit(`{"type":"model_changed"}`)
	waitFor(t, func() bool { return r.sink.SessionIDCount() > 0 })
	assert.Equal(t, "/sessions/s-new.jsonl", r.sink.LastSessionID())
	assert.Zero(t, r.sink.SettingsRefreshCount(), "the model and the level did not move")
}

// A read that fails leaves the refresher free for the next frame.
func TestAFailedStateRefreshAllowsTheNextOne(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandGetState {
			return &rigReply{Error: "busy"}
		}
		return nil
	})
	r.emit(`{"type":"model_changed"}`)
	r.waitForCommand(CommandGetState, 1)
	waitFor(t, func() bool {
		r.agent.stateRefresh.mu.Lock()
		defer r.agent.stateRefresh.mu.Unlock()
		return !r.agent.stateRefresh.running
	})
	assert.Zero(t, r.sink.SettingsRefreshCount())

	r.emit(`{"type":"model_changed"}`)
	r.waitForCommand(CommandGetState, 2)
}

func TestAModelChangeRefreshesTheState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type != CommandGetState {
			return nil
		}
		return &rigReply{Data: json.RawMessage(`{"model":{"id":"mock-model-2","provider":"mock"},"sessionId":"01a0cf77-9ae4-72d8-9a42-665c431d3beb","sessionFile":"/sessions/2026-09-23T18-11-57-284Z_01a0cf77-9ae4-72d8-9a42-665c431d3beb.jsonl"}`)}
	})
	r.emit(`{"type":"model_changed"}`)
	waitFor(t, func() bool {
		return r.sink.SettingsRefreshCount() > 0 && r.sink.LastSettingsRefresh().Model == "mock/mock-model-2"
	})
	assert.Equal(t, "off", r.sink.LastSettingsRefresh().Effort, "the state states no level for a model that does not reason")
	assert.Zero(t, r.sink.SessionIDCount(), "the session did not change")
}

func TestStateRefresherCoalesces(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	release := make(chan struct{})
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandGetState {
			<-release
		}
		return nil
	})
	r.emit(`{"type":"model_changed"}`, `{"type":"model_changed"}`, `{"type":"model_changed"}`)
	r.waitForCommand(CommandGetState, 1)
	close(release)
	r.waitForCommand(CommandGetState, 2)
	waitFor(t, func() bool {
		r.agent.stateRefresh.mu.Lock()
		defer r.agent.stateRefresh.mu.Unlock()
		return !r.agent.stateRefresh.running
	})
	assert.Len(t, r.commandsOfType(CommandGetState), 2, "the frames during a read ask for ONE more read")

	r.agent.stateRefresh.stop()
	r.emit(`{"type":"model_changed"}`)
	assert.Len(t, r.commandsOfType(CommandGetState), 2, "a stopped refresher reads nothing")
}

// A stop during a read drops the read that a frame asked for meanwhile: the
// stopped agent has no settings left to refresh.
func TestAStopDropsTheQueuedStateRefresh(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	release := make(chan struct{})
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandGetState {
			<-release
		}
		return nil
	})
	r.emit(`{"type":"model_changed"}`)
	r.waitForCommand(CommandGetState, 1)
	r.emit(`{"type":"model_changed"}`)

	r.agent.stateRefresh.stop()
	close(release)
	waitFor(t, func() bool {
		r.agent.stateRefresh.mu.Lock()
		defer r.agent.stateRefresh.mu.Unlock()
		return !r.agent.stateRefresh.running
	})
	assert.Len(t, r.commandsOfType(CommandGetState), 1)
}
