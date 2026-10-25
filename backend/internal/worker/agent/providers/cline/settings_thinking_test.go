package cline

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThinkingGroup(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for name, groups := range map[string][]*leapmuxv1.AvailableOptionGroup{
		"registration": Registration().OptionGroups,
		"runtime":      r.agent.OptionGroups(),
	} {
		t.Run(name, func(t *testing.T) {
			group := optionids.GroupByID(groups, "thinking")
			require.NotNil(t, group)
			assert.Equal(t, "Extended Thinking", group.GetLabel())
			assert.Equal(t, "auto", group.GetDefaultValue())
			assert.Equal(t, "auto", group.GetCurrentValue())
			assert.True(t, group.GetMutable())
			var choices []string
			for _, choice := range group.GetOptions() {
				choices = append(choices, choice.GetId())
			}
			assert.Equal(t, []string{"auto", "false", "true"}, choices)
		})
	}
	assert.Equal(t, "auto", r.agent.SettingsSnapshot().SurfacedOptions["thinking"])
}

func TestThinkingStartupConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		thinking string
		effort   string
	}{
		{name: "off retains low preference", thinking: "false", effort: "low"},
		{name: "on uses high", thinking: "true", effort: "high"},
		{name: "auto uses native defaults", thinking: "auto", effort: "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) {
				c.opts.Options = options("thinking", tc.thinking, agent.OptionIDEffort, tc.effort)
			})
			creates := r.hub.commandsNamed(commandSessionCreate)
			require.Len(t, creates, 1)
			var config, runtime map[string]any
			require.True(t, creates[0].field("sessionConfig", &config))
			require.True(t, creates[0].field("runtimeOptions", &runtime))
			assert.NotContains(t, runtime, "thinking", "Cline ignores thinking under runtimeOptions")
			assert.NotContains(t, runtime, "reasoningEffort", "Cline reads effort from sessionConfig")
			if tc.thinking == "auto" {
				assert.NotContains(t, config, "thinking")
				assert.NotContains(t, config, "reasoningEffort")
			} else {
				assert.Equal(t, tc.thinking == "true", config["thinking"])
				if tc.thinking == "true" {
					assert.Equal(t, tc.effort, config["reasoningEffort"])
				} else {
					assert.NotContains(t, config, "reasoningEffort")
				}
			}
			assert.Equal(t, tc.effort, r.agent.SettingsSnapshot().SurfacedOptions[agent.OptionIDEffort])
			assert.Equal(t, tc.thinking, r.agent.SettingsSnapshot().SurfacedOptions["thinking"])
		})
	}
}

func TestThinkingFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		settings clineSettings
		want     map[string]any
	}{
		{name: "zero settings"},
		{name: "auto effort", settings: clineSettings{effort: "auto"}},
		{name: "both auto", settings: clineSettings{thinking: "auto", effort: "auto"}},
		{name: "auto with max", settings: clineSettings{thinking: "auto", effort: "max"}, want: map[string]any{"thinking": true, "reasoningEffort": "max"}},
		{name: "omitted thinking with max", settings: clineSettings{effort: "max"}, want: map[string]any{"thinking": true, "reasoningEffort": "max"}},
		{name: "off with empty effort", settings: clineSettings{thinking: "false"}, want: map[string]any{"thinking": false}},
		{name: "off with auto", settings: clineSettings{thinking: "false", effort: "auto"}, want: map[string]any{"thinking": false}},
		{name: "off with low", settings: clineSettings{thinking: "false", effort: "low"}, want: map[string]any{"thinking": false}},
		{name: "on with empty effort", settings: clineSettings{thinking: "true"}, want: map[string]any{"thinking": true}},
		{name: "on with auto", settings: clineSettings{thinking: "true", effort: "auto"}, want: map[string]any{"thinking": true}},
		{name: "on with low", settings: clineSettings{thinking: "true", effort: "low"}, want: map[string]any{"thinking": true, "reasoningEffort": "low"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reasoningFields(tc.settings))
		})
	}
}

func TestThinkingLaunchFallback(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "unknown", "TRUE", "0", " "} {
		t.Run("value="+value, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.opts.Options = options(OptionThinking, value) })
			assert.Equal(t, thinkingAuto, r.agent.SettingsSnapshot().SurfacedOptions[OptionThinking])
			var config map[string]any
			require.True(t, r.hub.commandsNamed(commandSessionCreate)[0].field("sessionConfig", &config))
			assert.NotContains(t, config, "thinking")
			assert.NotContains(t, config, "reasoningEffort")
		})
	}
}

func TestThinkingSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		current   string
		requested map[string]string
		want      string
		wire      map[string]any
		invalid   bool
	}{
		{name: "disable auto", current: "auto", requested: options("thinking", "false"), want: "false", wire: map[string]any{"thinking": false}},
		{name: "enable with retained effort", current: "false", requested: options("thinking", "true"), want: "true", wire: map[string]any{"thinking": true, "reasoningEffort": "low"}},
		{name: "disable on", current: "true", requested: options("thinking", "false"), want: "false", wire: map[string]any{"thinking": false}},
		{name: "explicit on already effective", current: "auto", requested: options("thinking", "true"), want: "true"},
		{name: "unchanged on", current: "true", requested: options("thinking", "true"), want: "true"},
		{name: "omitted", current: "false", want: "false"},
		{name: "empty", current: "false", requested: options("thinking", ""), want: "false"},
		{name: "unknown", current: "false", requested: options("thinking", "enabled"), want: "false", invalid: true},
		{name: "wrong case", current: "true", requested: options("thinking", "FALSE"), want: "true", invalid: true},
		{name: "whitespace", current: "false", requested: options("thinking", " "), want: "false", invalid: true},
		{name: "numeric zero", current: "true", requested: options("thinking", "0"), want: "true", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.opts.Options = options("thinking", tc.current, agent.OptionIDEffort, "low") })
			result := r.agent.UpdateSettings(tc.requested)
			assert.True(t, result.AppliedLive)
			assert.Equal(t, tc.want, result.SurfacedOptions[OptionThinking])
			assert.Equal(t, "low", result.SurfacedOptions[agent.OptionIDEffort])
			assert.Empty(t, r.hub.commandsNamed(commandSessionDetach))
			if tc.wire == nil {
				assert.Empty(t, updates(t, r.hub))
			} else {
				assert.Equal(t, []map[string]any{tc.wire}, updates(t, r.hub))
			}
			state := agent.OptionSettlementConfirmed
			if tc.invalid {
				state = agent.OptionSettlementUnresolved
			}
			assert.Equal(t, state, result.Settlements[OptionThinking].State)
			assert.Equal(t, tc.want, r.agent.SettingsSnapshot().SurfacedOptions[OptionThinking])
		})
	}
}

func TestThinkingEffortWhileOff(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.opts.Options = options("thinking", "false", agent.OptionIDEffort, "high") })
	result := r.agent.UpdateSettings(options(agent.OptionIDEffort, "low"))
	assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
	assert.Equal(t, "false", result.SurfacedOptions[OptionThinking])
	assert.Empty(t, updates(t, r.hub), "a local effort preference does not enable native thinking")
	result = r.agent.UpdateSettings(options("thinking", "true"))
	assert.Equal(t, "true", result.ConfirmedOptions()[OptionThinking])
	assert.Equal(t, []map[string]any{{"thinking": true, "reasoningEffort": "low"}}, updates(t, r.hub))
	assert.Equal(t, "low", result.SurfacedOptions[agent.OptionIDEffort])
}

func TestThinkingDefaultReset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		currentThinking string
		currentEffort   string
		thinking        string
		effort          string
		fields          map[string]any
	}{
		{name: "restore both defaults", currentThinking: "true", currentEffort: "high", thinking: "auto", effort: "auto"},
		{name: "restore thinking with selected effort", currentThinking: "true", currentEffort: "low", thinking: "auto", effort: "low", fields: map[string]any{"thinking": true, "reasoningEffort": "low"}},
		{name: "enable configured effort after off", currentThinking: "false", currentEffort: "auto", thinking: "true", effort: "auto", fields: map[string]any{"thinking": true}},
		{name: "enable configured effort from auto", currentThinking: "auto", currentEffort: "auto", thinking: "true", effort: "auto", fields: map[string]any{"thinking": true}},
		{name: "restore defaults after off", currentThinking: "false", currentEffort: "auto", thinking: "auto", effort: "auto"},
		{name: "reset effort while off", currentThinking: "false", currentEffort: "high", thinking: "false", effort: "auto", fields: map[string]any{"thinking": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) {
				c.opts.Options = options("thinking", tc.currentThinking, agent.OptionIDEffort, tc.currentEffort)
			})
			sessionID := r.sessionID()
			history := []any{map[string]any{"role": "user", "content": "Retain this conversation."}}
			r.hub.store(sessionID, history)
			r.hub.storeCompaction(sessionID, map[string]any{"summary": "Retain this summary."})
			result := r.agent.UpdateSettings(options("thinking", tc.thinking, agent.OptionIDEffort, tc.effort))
			assert.Equal(t, tc.thinking, result.ConfirmedOptions()[OptionThinking])
			assert.Equal(t, tc.effort, result.ConfirmedOptions()[agent.OptionIDEffort])
			assert.Empty(t, updates(t, r.hub), "a reset reloads defaults rather than sending null")
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
			assert.Equal(t, history, messages)
			assert.Equal(t, "Retain this summary.", compaction["summary"])
			for _, key := range []string{"thinking", "reasoningEffort"} {
				assert.NotContains(t, runtime, key)
				if value, present := tc.fields[key]; present {
					assert.Equal(t, value, config[key])
				} else {
					assert.NotContains(t, config, key)
				}
			}
			assert.Equal(t, sessionID, r.sessionID())
			assert.False(t, r.turnActive())
		})
	}
}

func TestThinkingDeferredReset(t *testing.T) {
	t.Parallel()
	for _, cancelReset := range []bool{false, true} {
		name := "apply after turn"
		if cancelReset {
			name = "replace pending reset with live off"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(c *rigConfig) { c.opts.Options = options("thinking", "true", agent.OptionIDEffort, "high") })
			r.hub.store(r.sessionID(), []any{})
			requestID := r.startTurn(t, "Hold the current turn.")
			result := r.agent.UpdateSettings(options("thinking", "auto", agent.OptionIDEffort, "auto"))
			assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[OptionThinking].State)
			assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
			assert.Equal(t, "true", result.SurfacedOptions[OptionThinking])
			assert.Equal(t, "high", result.SurfacedOptions[agent.OptionIDEffort])
			assert.Empty(t, r.hub.commandsNamed(commandSessionDetach))
			assert.Empty(t, updates(t, r.hub))
			assert.NotContains(t, r.sink.LastSettingsRefresh().Options, OptionThinking)
			assert.NotContains(t, r.sink.LastSettingsRefresh().Options, agent.OptionIDEffort)
			wantThinking, wantEffort, creates := "auto", "auto", 2
			if cancelReset {
				result = r.agent.UpdateSettings(options("thinking", "false", agent.OptionIDEffort, "low"))
				assert.Equal(t, "false", result.ConfirmedOptions()[OptionThinking])
				assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
				wantThinking, wantEffort, creates = "false", "low", 1
			} else {
				result = r.agent.UpdateSettings(options(agent.OptionIDModel, "gpt-5.5"))
				assert.Equal(t, "gpt-5.5", result.ConfirmedOptions()[agent.OptionIDModel])
			}
			r.endRun(t, requestID, contracts.ClineRunReasonCompleted)
			waitFor(t, func() bool { return !r.turnActive() }, "the pending reasoning change finishes")
			assert.Len(t, r.hub.commandsNamed(commandSessionCreate), creates)
			snapshot := r.agent.SettingsSnapshot().SurfacedOptions
			assert.Equal(t, wantThinking, snapshot[OptionThinking])
			assert.Equal(t, wantEffort, snapshot[agent.OptionIDEffort])
			if !cancelReset {
				assert.Equal(t, "gpt-5.5", snapshot[agent.OptionIDModel])
			}
		})
	}
}

func TestThinkingRefusal(t *testing.T) {
	t.Parallel()
	t.Run("live update", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, func(c *rigConfig) { c.opts.Options = options("thinking", "false", agent.OptionIDEffort, "low") })
		r.hub.handle(commandUpdateConnection, func(fakeCommand) fakeReply {
			return fakeReply{Code: "invalid_config", Message: "Native thinking refused."}
		})
		result := r.agent.UpdateSettings(options("thinking", "true", agent.OptionIDEffort, "high"))
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[OptionThinking].State)
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
		assert.Equal(t, "false", result.SurfacedOptions[OptionThinking])
		assert.Equal(t, "low", result.SurfacedOptions[agent.OptionIDEffort])
	})
	t.Run("rebuild restores previous configuration", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, func(c *rigConfig) { c.opts.Options = options("thinking", "false", agent.OptionIDEffort, "low") })
		r.hub.store(r.sessionID(), []any{})
		attempts := 0
		r.hub.handle(commandSessionCreate, func(command fakeCommand) fakeReply {
			attempts++
			if attempts == 1 {
				return fakeReply{Code: "invalid_config", Message: "Native reset refused."}
			}
			return fakeReply{Payload: map[string]any{"session": map[string]any{"sessionId": command.SessionID}}}
		})
		result := r.agent.UpdateSettings(options("thinking", "auto", agent.OptionIDEffort, "auto"))
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[OptionThinking].State)
		assert.Equal(t, "false", result.SurfacedOptions[OptionThinking])
		assert.Equal(t, "low", result.SurfacedOptions[agent.OptionIDEffort])
		creates := r.hub.commandsNamed(commandSessionCreate)
		require.Len(t, creates, 3)
		var config map[string]any
		require.True(t, creates[2].field("sessionConfig", &config))
		assert.Equal(t, false, config["thinking"])
		assert.NotContains(t, config, "reasoningEffort")
		assert.False(t, r.turnActive())
		require.NoError(t, r.agent.SendInput("Continue in the restored session.", nil))
	})
}

func TestThinkingModelAndModeChanges(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *rigConfig) { c.opts.Options = options("thinking", "false", agent.OptionIDEffort, "high") })
	r.hub.store(r.sessionID(), []any{})
	result := r.agent.UpdateSettings(options(agent.OptionIDPermissionMode, contracts.ClinePermissionModePlan))
	assert.Equal(t, "false", result.SurfacedOptions[OptionThinking])
	assert.Equal(t, "high", result.SurfacedOptions[agent.OptionIDEffort])
	creates := r.hub.commandsNamed(commandSessionCreate)
	require.Len(t, creates, 2)
	var config map[string]any
	require.True(t, creates[1].field("sessionConfig", &config))
	assert.Equal(t, false, config["thinking"])
	result = r.agent.UpdateSettings(options(agent.OptionIDModel, "gpt-5.5"))
	assert.Equal(t, []map[string]any{{"modelId": "gpt-5.5"}}, updates(t, r.hub))
	assert.Equal(t, "false", result.SurfacedOptions[OptionThinking])
	assert.Equal(t, "high", result.SurfacedOptions[agent.OptionIDEffort])
	result = r.agent.UpdateSettings(options(agent.OptionIDModel, "gpt-4.1"))
	assert.Equal(t, "false", result.SurfacedOptions[OptionThinking])
	assert.Nil(t, optionids.GroupByID(r.agent.OptionGroups(), agent.OptionIDEffort))
	r.agent.Mu.Lock()
	effort := r.agent.settings.effort
	r.agent.Mu.Unlock()
	assert.Equal(t, agent.EffortAuto, effort)
}
