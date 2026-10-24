package letta

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestLettaStartupAppliesExplicitEffort(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	options := newTestOptions()
	options.Options[agent.OptionIDModel] = "openai/one"
	options.Options[agent.OptionIDEffort] = "high"
	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(options) }()
	start := fake.nextCommand(t)
	require.Equal(t, "runtime_start", start["type"])
	replyLettaSettings(t, fake, start, map[string]any{
		"runtime":      map[string]string{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
		"agent":        map[string]any{"id": "agent-local-1", "model": "openai/one"},
		"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"},
	})
	catalog := fake.nextCommand(t)
	require.Equal(t, "list_models", catalog["type"])
	replyLettaSettings(t, fake, catalog, map[string]any{"entries": []any{
		map[string]any{"id": "one-high", "handle": "openai/one", "updateArgs": map[string]any{"reasoning_effort": "high"}},
	}})
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-returned:
		t.Fatalf("startup returned before applying its explicit effort: %v", err)
	case info := <-fake.commands:
		require.Equal(t, "app_server_info", info["type"])
		replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
	case <-ctx.Done():
		t.Fatal("startup never applied its explicit effort")
	}
	base := fake.nextCommand(t)
	require.Equal(t, "agent_retrieve", base["type"])
	replyLettaSettings(t, fake, base, map[string]any{"agent": map[string]any{"id": "agent-local-1", "model": "openai/one"}})
	read := fake.nextCommand(t)
	require.Equal(t, "conversation_retrieve", read["type"])
	replyLettaSettings(t, fake, read, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"}})
	write := fake.nextCommand(t)
	require.Equal(t, "conversation_update", write["type"])
	body, ok := write["body"].(map[string]any)
	require.True(t, ok)
	settings, ok := body["model_settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "high", settings["reasoning_effort"])
	replyLettaSettings(t, fake, write, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": settings}})
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("startup did not return after confirming its effort")
	}
	assert.Equal(t, "high", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDEffort])
}

func configureLettaEffortTest(a *Agent) {
	a.settings.model = "openai/one"
	a.settings.reasoningLevel = "low"
	a.catalog.models = []lettaModel{{id: "openai/one", displayName: "One", variants: []lettaModelVariant{
		{id: "one-low", effort: "low", providerType: "openai"},
		{id: "one-high", effort: "high", providerType: "openai"},
	}}}
}

func replyLettaSettings(t *testing.T, fake *fakeAppServer, command map[string]any, fields map[string]any) {
	t.Helper()
	fields["type"] = command["type"].(string) + "_response"
	fields["request_id"] = command["request_id"]
	if _, present := fields["success"]; !present {
		fields["success"] = true
	}
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	fake.replies <- raw
}

func TestLettaEffortPreservesNativeSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		backend string
		effort  string
		applied string
	}{
		{"local high", "local", "high", "high"},
		{"API high", "api", "high", "high"},
		{"native normalizes the request", "local", "high", "low"},
		{"local reset", "local", agent.EffortAuto, agent.EffortAuto},
		{"API reset", "api", "", agent.EffortAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, a := openedAgent(t)
			configureLettaEffortTest(a)
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: tc.effort}) }()
			info := fake.nextCommand(t)
			require.Equal(t, "app_server_info", info["type"])
			replyLettaSettings(t, fake, info, map[string]any{"backend": tc.backend})
			base := fake.nextCommand(t)
			require.Equal(t, "agent_retrieve", base["type"])
			assert.Equal(t, "agent-local-1", base["agent_id"])
			replyLettaSettings(t, fake, base, map[string]any{"agent": map[string]any{
				"id": "agent-local-1", "model": "openai/one",
				"model_settings": map[string]any{"provider_type": "openai", "max_tokens": 7000, "reasoning": map[string]any{"reasoning_effort": "low", "summary": "detailed"}},
			}})
			read := fake.nextCommand(t)
			require.Equal(t, "conversation_retrieve", read["type"])
			assert.Equal(t, "local-conv-1", read["conversation_id"])
			existing := map[string]any{"provider_type": "openai", "max_tokens": 9000, "verbosity": "low", "custom": map[string]any{"zero": 0, "enabled": false}, "context_window_limit": 200000}
			replyLettaSettings(t, fake, read, map[string]any{"conversation": map[string]any{
				"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": existing,
			}})
			write := fake.nextCommand(t)
			require.Equal(t, "conversation_update", write["type"])
			assert.Equal(t, "local-conv-1", write["conversation_id"])
			body, ok := write["body"].(map[string]any)
			require.True(t, ok)
			assert.NotContains(t, body, "model")
			assert.NotContains(t, body, "context_window_limit")
			settings, ok := body["model_settings"].(map[string]any)
			require.True(t, ok)
			for key, value := range existing {
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				actual, err := json.Marshal(settings[key])
				require.NoError(t, err)
				assert.JSONEq(t, string(raw), string(actual), "preserve %s", key)
			}
			var effort any = tc.effort
			if tc.effort == "" || tc.effort == agent.EffortAuto {
				effort = nil
			}
			assert.Equal(t, map[string]any{"reasoning_effort": effort, "summary": "detailed"}, settings["reasoning"])
			if tc.backend == "local" {
				assert.Contains(t, settings, "reasoning_effort")
				assert.Equal(t, effort, settings["reasoning_effort"])
			} else {
				assert.NotContains(t, settings, "reasoning_effort")
			}
			select {
			case <-returned:
				t.Fatal("the requested effort was confirmed before the native update reply")
			default:
			}
			var nativeEffort any = tc.applied
			if tc.applied == agent.EffortAuto {
				nativeEffort = nil
			}
			settings["reasoning"] = map[string]any{"reasoning_effort": nativeEffort, "summary": "detailed"}
			if tc.backend == "local" {
				settings["reasoning_effort"] = nativeEffort
			}
			replyLettaSettings(t, fake, write, map[string]any{"conversation": map[string]any{
				"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": settings,
			}})
			result := waitModeResult(t, returned)
			assert.True(t, result.AppliedLive)
			assert.Equal(t, tc.applied, result.ConfirmedOptions()[agent.OptionIDEffort])
			assert.Equal(t, "openai/one", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
		})
	}
}

func TestLettaEffortRejectsUnconfirmedWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		fields  map[string]any
		refused bool
	}{
		{"missing success", map[string]any{"success": nil}, false},
		{"native refusal", map[string]any{"success": false, "error": "effort refused"}, true},
		{"missing record", map[string]any{}, false},
		{"foreign conversation", map[string]any{"conversation": map[string]any{"id": "foreign", "agent_id": "agent-local-1", "model_settings": map[string]any{"reasoning_effort": "high"}}}, false},
		{"foreign agent", map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "foreign", "model_settings": map[string]any{"reasoning_effort": "high"}}}, false},
		{"missing settings", map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"}}, false},
		{"missing effort", map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": map[string]any{"provider_type": "openai"}}}, false},
		{"invalid effort", map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": map[string]any{"reasoning_effort": 7}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, a := openedAgent(t)
			configureLettaEffortTest(a)
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"}) }()
			info := fake.nextCommand(t)
			require.Equal(t, "app_server_info", info["type"])
			replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
			base := fake.nextCommand(t)
			require.Equal(t, "agent_retrieve", base["type"])
			replyLettaSettings(t, fake, base, map[string]any{"agent": map[string]any{"id": "agent-local-1", "model": "openai/one", "model_settings": map[string]any{"provider_type": "openai", "reasoning_effort": "low"}}})
			read := fake.nextCommand(t)
			require.Equal(t, "conversation_retrieve", read["type"])
			replyLettaSettings(t, fake, read, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"}})
			write := fake.nextCommand(t)
			require.Equal(t, "conversation_update", write["type"])
			replyLettaSettings(t, fake, write, tc.fields)
			result := waitModeResult(t, returned)
			assert.Equal(t, tc.refused, result.AppliedLive)
			assert.NotEqual(t, "high", result.ConfirmedOptions()[agent.OptionIDEffort])
			assert.Equal(t, "low", a.settings.reasoningLevel)
		})
	}
}

func TestLettaRestoresNativeModelSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		conversation string
		want         string
	}{
		{"inherits agent effort", `{}`, "high"},
		{"conversation overrides agent", `{"model":"openai/one","model_settings":{"reasoning":{"reasoning_effort":"low"}}}`, "low"},
		{"explicit reset overrides agent", `{"model_settings":{"reasoning":null,"reasoning_effort":null}}`, agent.EffortAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, a := openedAgent(t)
			configureLettaEffortTest(a)
			a.settings.model = "stale-model"
			a.settings.reasoningLevel = "stale-effort"
			var conversation map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.conversation), &conversation))
			conversation["id"] = "local-conv-1"
			conversation["agent_id"] = "agent-local-1"
			raw, err := json.Marshal(map[string]any{
				"type": "runtime_start_response", "success": true,
				"runtime":      map[string]string{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
				"agent":        map[string]any{"id": "agent-local-1", "model": "openai/one", "model_settings": map[string]any{"provider_type": "openai", "reasoning": map[string]any{"reasoning_effort": "high"}}},
				"conversation": conversation,
			})
			require.NoError(t, err)
			a.handleFrame(raw)
			assert.Equal(t, "openai/one", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
			assert.Equal(t, tc.want, a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDEffort])
		})
	}
}
