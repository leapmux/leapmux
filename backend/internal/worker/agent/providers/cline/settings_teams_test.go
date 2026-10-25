package cline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestAgentTeamsGroup(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for name, groups := range map[string][]*leapmuxv1.AvailableOptionGroup{
		"registration": Registration().OptionGroups,
		"runtime":      r.agent.OptionGroups(),
	} {
		t.Run(name, func(t *testing.T) {
			group := optionids.GroupByID(groups, "agentTeams")
			require.NotNil(t, group)
			assert.Equal(t, "Agent Teams", group.GetLabel())
			assert.Equal(t, "true", group.GetDefaultValue())
			assert.Equal(t, "true", group.GetCurrentValue())
			assert.True(t, group.GetMutable())
			var choices []string
			for _, choice := range group.GetOptions() {
				choices = append(choices, choice.GetId())
			}
			assert.Equal(t, []string{"false", "true"}, choices)
		})
	}
	assert.Equal(t, "true", r.agent.SettingsSnapshot().SurfacedOptions["agentTeams"])
}

func TestAgentTeamsStartupConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options []string
		enabled bool
	}{
		{name: "default", enabled: true},
		{name: "explicit on", options: []string{"agentTeams", "true"}, enabled: true},
		{name: "off", options: []string{"agentTeams", "false"}},
		{name: "empty", options: []string{"agentTeams", ""}, enabled: true},
		{name: "unknown", options: []string{"agentTeams", "enabled"}, enabled: true},
		{name: "wrong case", options: []string{"agentTeams", "FALSE"}, enabled: true},
		{name: "whitespace", options: []string{"agentTeams", " "}, enabled: true},
		{name: "numeric zero", options: []string{"agentTeams", "0"}, enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.opts.Options = options(tc.options...) })
			creates := r.hub.commandsNamed(commandSessionCreate)
			require.Len(t, creates, 1)
			var runtime map[string]any
			require.True(t, creates[0].field("runtimeOptions", &runtime))
			assert.Equal(t, tc.enabled, runtime["enableTeams"])
			assert.Equal(t, true, runtime["enableSpawn"], "ordinary subagents stay independent")
		})
	}
}

func TestAgentTeamsUnchangedAndInvalidSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		current   string
		requested map[string]string
		invalid   bool
	}{
		{name: "unchanged on", current: "true", requested: options("agentTeams", "true")},
		{name: "unchanged off", current: "false", requested: options("agentTeams", "false")},
		{name: "omitted", current: "false", requested: options()},
		{name: "empty", current: "false", requested: options("agentTeams", "")},
		{name: "unknown", current: "false", requested: options("agentTeams", "enabled"), invalid: true},
		{name: "wrong case", current: "true", requested: options("agentTeams", "FALSE"), invalid: true},
		{name: "whitespace", current: "false", requested: options("agentTeams", " "), invalid: true},
		{name: "numeric zero", current: "true", requested: options("agentTeams", "0"), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.opts.Options = options("agentTeams", tc.current) })
			result := r.agent.UpdateSettings(tc.requested)
			assert.Equal(t, tc.current, result.SurfacedOptions["agentTeams"])
			assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1)
			assert.Empty(t, r.hub.commandsNamed(commandSessionDetach))
			assert.Empty(t, updates(t, r.hub))
			state := agent.OptionSettlementConfirmed
			if tc.invalid {
				state = agent.OptionSettlementUnresolved
			}
			assert.Equal(t, state, result.Settlements["agentTeams"].State)
		})
	}
}

func TestAgentTeamsRebuild(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		current string
		next    string
		enabled bool
	}{
		{name: "disable", current: "true", next: "false"},
		{name: "enable", current: "false", next: "true", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) {
				c.opts.Options = options(
					"agentTeams", tc.current,
					OptionThinking, thinkingOff,
					agent.OptionIDEffort, "high",
					agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan,
				)
			})
			sessionID := r.sessionID()
			history := []any{map[string]any{"role": "user", "content": "Retain the team-setting conversation."}}
			r.hub.store(sessionID, history)
			r.hub.storeCompaction(sessionID, map[string]any{"summary": "Retain the team-setting summary."})
			result := r.agent.UpdateSettings(options("agentTeams", tc.next))
			assert.Equal(t, tc.next, result.ConfirmedOptions()["agentTeams"])
			assert.Empty(t, updates(t, r.hub), "Cline has no live team-setting update")
			require.Len(t, r.hub.commandsNamed(commandSessionDetach), 1)
			creates := r.hub.commandsNamed(commandSessionCreate)
			require.Len(t, creates, 2)
			var config, runtime, compaction map[string]any
			var messages []any
			require.True(t, creates[1].field("sessionConfig", &config))
			require.True(t, creates[1].field("runtimeOptions", &runtime))
			require.True(t, creates[1].field("initialMessages", &messages))
			require.True(t, creates[1].field("initialCompactionState", &compaction))
			assert.Equal(t, sessionID, config["sessionId"])
			assert.Equal(t, false, config["thinking"])
			assert.NotContains(t, config, "reasoningEffort")
			assert.Equal(t, history, messages)
			assert.Equal(t, "Retain the team-setting summary.", compaction["summary"])
			assert.Equal(t, tc.enabled, runtime["enableTeams"])
			assert.Equal(t, true, runtime["enableSpawn"])
			assert.Equal(t, sessionModePlan, runtime["mode"])
			assert.Equal(t, sessionID, r.sessionID())
			assert.False(t, r.turnActive())
			snapshot := r.agent.SettingsSnapshot().SurfacedOptions
			assert.Equal(t, testModel, snapshot[agent.OptionIDModel])
			assert.Equal(t, "high", snapshot[agent.OptionIDEffort])
			assert.Equal(t, thinkingOff, snapshot[OptionThinking])
			assert.Equal(t, contracts.ClinePermissionModePlan, snapshot[agent.OptionIDPermissionMode])
		})
	}
}

func TestAgentTeamsDeferredChange(t *testing.T) {
	t.Parallel()
	t.Run("apply after the turn", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.hub.store(r.sessionID(), []any{})
		requestID := r.startTurn(t, "Hold the team-setting turn.")
		result := r.agent.UpdateSettings(options("agentTeams", "false", agent.OptionIDModel, "gpt-5.5"))
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements["agentTeams"].State)
		assert.Equal(t, "true", result.SurfacedOptions["agentTeams"])
		assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDModel].State)
		assert.Empty(t, r.hub.commandsNamed(commandSessionDetach))
		assert.Equal(t, []map[string]any{{"modelId": "gpt-5.5"}}, updates(t, r.hub))
		assert.NotContains(t, r.sink.LastSettingsRefresh().Options, "agentTeams")
		r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
		creates := r.hub.commandsNamed(commandSessionCreate)
		require.Len(t, creates, 2)
		var config, runtime map[string]any
		require.True(t, creates[1].field("sessionConfig", &config))
		require.True(t, creates[1].field("runtimeOptions", &runtime))
		assert.Equal(t, "gpt-5.5", config["modelId"])
		assert.Equal(t, false, runtime["enableTeams"])
		assert.Equal(t, "false", r.agent.SettingsSnapshot().SurfacedOptions["agentTeams"])
	})

	t.Run("cancel before the turn ends", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		requestID := r.startTurn(t, "Hold the canceled team-setting turn.")
		r.agent.UpdateSettings(options("agentTeams", "false"))
		result := r.agent.UpdateSettings(options("agentTeams", "true"))
		assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements["agentTeams"].State)
		r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
		assert.Len(t, r.hub.commandsNamed(commandSessionCreate), 1)
		assert.Empty(t, r.hub.commandsNamed(commandSessionDetach))
		assert.Equal(t, "true", r.agent.SettingsSnapshot().SurfacedOptions["agentTeams"])
	})
}

func TestAgentTeamsRefusedRebuild(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.hub.store(r.sessionID(), []any{})
	attempts := 0
	r.hub.handle(commandSessionCreate, func(command fakeCommand) fakeReply {
		attempts++
		if attempts == 1 {
			return fakeReply{Code: "invalid_config", Message: "Native team setting refused."}
		}
		return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": command.SessionID}}}
	})
	result := r.agent.UpdateSettings(options("agentTeams", "false"))
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements["agentTeams"].State)
	assert.Equal(t, "true", result.SurfacedOptions["agentTeams"])
	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 3)
	var refused, restored map[string]any
	require.True(t, creates[1].field("runtimeOptions", &refused))
	require.True(t, creates[2].field("runtimeOptions", &restored))
	assert.Equal(t, false, refused["enableTeams"])
	assert.Equal(t, true, restored["enableTeams"])
	assert.False(t, r.turnActive())
	require.NoError(t, r.agent.SendInput("Continue with the restored team setting.", nil))
}

func TestAgentTeamsPreservesTeammateState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.sessionID()
	r.hub.store(root, []any{})
	r.feed(t, contracts.ClineEventTeamProgress, teamProgressPayload(root, contracts.ClineTeamRunEventRunStarted, "run_team_setting", "researcher"))
	before, ok := r.sink.BackgroundTask(teamRowPrefix + "run_team_setting")
	require.True(t, ok)
	notices := r.sink.NotificationCount()
	result := r.agent.UpdateSettings(options("agentTeams", "false"))
	assert.Equal(t, "false", result.ConfirmedOptions()["agentTeams"])
	require.Len(t, r.hub.commandsNamed(commandSessionCreate), 2)
	after, ok := r.sink.BackgroundTask(teamRowPrefix + "run_team_setting")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, after.Status)
	assert.Equal(t, before.ChildAgentID, after.ChildAgentID)
	assert.NotEmpty(t, after.ChildAgentID)
	assert.Equal(t, notices, r.sink.NotificationCount())
	r.agent.Mu.Lock()
	run := r.agent.team.runs["run_team_setting"]
	r.agent.Mu.Unlock()
	require.NotNil(t, run)
	assert.False(t, run.ended)
}
