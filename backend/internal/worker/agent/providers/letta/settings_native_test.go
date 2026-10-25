package letta

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestLettaEffortPatchPreservesNativeFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model    string
		effort   string
		provider string
		field    string
		want     string
	}{
		{"openai/gpt-5.4", "high", "openai", "reasoning", `{"summary":"detailed","reasoning_effort":"high"}`},
		{"openai/gpt-5.4", agent.EffortAuto, "openai", "reasoning", `{"summary":"detailed","reasoning_effort":null}`},
		{"anthropic/claude-opus-4-6", "xhigh", "anthropic", "effort", `"max"`},
		{"anthropic/claude-opus-4-8", "xhigh", "anthropic", "effort", `"xhigh"`},
		{"bedrock/claude-opus-4-6", "xhigh", "bedrock", "effort", `"max"`},
		{"anthropic/claude-opus-4-8", agent.EffortAuto, "anthropic", "effort", `null`},
		{"moonshotai/kimi", "high", "moonshot", "reasoning_effort", `"high"`},
		{"zai/glm", agent.EffortAuto, "zai", "reasoning_effort", `null`},
		{"openai-codex/gpt-5.4", "high", "chatgpt_oauth", "reasoning", `{"summary":"detailed","reasoning_effort":"high"}`},
		{"openai-codex/gpt-5.5", "minimal", "chatgpt_oauth", "reasoning", `{"summary":"detailed","reasoning_effort":"none"}`},
		{"openai/gpt-5.5", "max", "openai", "reasoning", `{"summary":"detailed","reasoning_effort":"xhigh"}`},
		{"private/model", "low", "openai", "reasoning", `{"summary":"detailed","reasoning_effort":"low"}`},
	} {
		for _, backend := range []string{"api", "local"} {
			t.Run(tc.model+"/"+tc.effort+"/"+backend, func(t *testing.T) {
				t.Parallel()
				var current, inherited map[string]json.RawMessage
				const existing = `{"max_tokens":9007199254740993,"zero":0,"negative":-3,"enabled":false,"reasoning":{"summary":"detailed"}}`
				require.NoError(t, json.Unmarshal([]byte(existing), &current))
				require.NoError(t, json.Unmarshal([]byte(`{"context_window_limit":200000}`), &inherited))
				patch, err := lettaEffortPatch(current, inherited, lettaModel{id: tc.model}, tc.effort, backend)
				require.NoError(t, err)
				assert.JSONEq(t, tc.want, string(patch[tc.field]))
				var provider string
				require.NoError(t, json.Unmarshal(patch["provider_type"], &provider))
				assert.Equal(t, tc.provider, provider)
				assert.Equal(t, "9007199254740993", string(patch["max_tokens"]))
				for _, key := range []string{"zero", "negative", "enabled"} {
					assert.Equal(t, current[key], patch[key])
				}
				assert.NotContains(t, patch, "context_window_limit", "keep inheritance instead of copying it into an override")
				after, err := json.Marshal(current)
				require.NoError(t, err)
				assert.JSONEq(t, existing, string(after), "do not mutate the retrieved settings")
				if backend == "local" {
					want := tc.effort
					if want == agent.EffortAuto {
						assert.Equal(t, "null", string(patch["reasoning_effort"]))
					} else {
						var actual string
						require.NoError(t, json.Unmarshal(patch["reasoning_effort"], &actual))
						assert.Equal(t, want, actual)
					}
				}
			})
		}
	}
}

func TestLettaReportedEffortUsesDocumentedFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input   string
		want    string
		present bool
		invalid bool
	}{
		{`{}`, agent.EffortAuto, false, false},
		{`{"reasoning_effort":"high"}`, "high", true, false},
		{`{"effort":"max"}`, "max", true, false},
		{`{"reasoning":{"reasoning_effort":"low"}}`, "low", true, false},
		{`{"reasoning":null}`, agent.EffortAuto, true, false},
		{`{"reasoning":{"reasoning_effort":null}}`, agent.EffortAuto, true, false},
		{`{"reasoning_effort":""}`, agent.EffortAuto, true, false},
		{`{"reasoning":{"reasoning":{"reasoning_effort":"high"}}}`, agent.EffortAuto, false, false},
		{`{"reasoning":{"summary":"detailed"}}`, agent.EffortAuto, false, false},
		{`{"reasoning":{"reasoning_effort":"low"},"effort":"medium","reasoning_effort":"high"}`, "low", true, false},
		{`{"reasoning":null,"effort":"medium","reasoning_effort":"high"}`, "medium", true, false},
		{`{"reasoning":{"reasoning_effort":null},"effort":null,"reasoning_effort":"high"}`, "high", true, false},
		{`{"reasoning_effort":7}`, "", false, true},
		{`{"reasoning":[]}`, "", false, true},
		{`{"reasoning":"high"}`, "", false, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			var settings map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(tc.input), &settings))
			actual, present, err := lettaReportedEffort(settings)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, actual)
			assert.Equal(t, tc.present, present)
		})
	}
}

func TestLettaEffortResetClearsEveryNativeEffortRepresentation(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"openai/gpt-5.4", "anthropic/claude-opus-4-8", "zai/glm"} {
		for _, from := range []string{"conversation", "agent"} {
			t.Run(model+"/"+from, func(t *testing.T) {
				t.Parallel()
				var source map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(`{"reasoning":{"reasoning_effort":"high","summary":"detailed"},"effort":"high","reasoning_effort":"high","max_tokens":7000}`), &source))
				current, inherited := source, map[string]json.RawMessage(nil)
				if from == "agent" {
					current, inherited = nil, source
				}
				patch, err := lettaEffortPatch(current, inherited, lettaModel{id: model}, agent.EffortAuto, "local")
				require.NoError(t, err)
				assert.Equal(t, "null", string(patch["effort"]))
				assert.Equal(t, "null", string(patch["reasoning_effort"]))
				assert.JSONEq(t, `{"reasoning_effort":null,"summary":"detailed"}`, string(patch["reasoning"]))
				assert.Equal(t, `"high"`, string(source["effort"]), "leave the read snapshot immutable")
			})
		}
	}
}

func TestLettaNativeSettingsMergeCanonicalFieldsBeforeReadingEffort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base   string
		change string
		want   string
	}{
		{`{"reasoning":{"reasoning_effort":"high"}}`, `{"reasoning_effort":"low"}`, "high"},
		{`{"reasoning_effort":"high"}`, `{"reasoning":{"reasoning_effort":"low"}}`, "low"},
		{`{"reasoning_effort":"high"}`, `{"reasoning":null}`, "high"},
		{`{"reasoning":{"reasoning_effort":"high"}}`, `{"reasoning":{"summary":"detailed"}}`, agent.EffortAuto},
		{`{"reasoning":{"reasoning_effort":"high"},"effort":"high"}`, `{"reasoning":null,"effort":null,"reasoning_effort":null}`, agent.EffortAuto},
	} {
		t.Run(tc.base+tc.change, func(t *testing.T) {
			t.Parallel()
			_, a := openedAgent(t)
			configureLettaEffortTest(a)
			base := &lettaSettingsRecord{Model: "openai/one"}
			conversation := &lettaSettingsRecord{}
			require.NoError(t, json.Unmarshal([]byte(tc.base), &base.ModelSettings))
			require.NoError(t, json.Unmarshal([]byte(tc.change), &conversation.ModelSettings))
			require.NoError(t, a.recordNativeModelSettings(base, conversation))
			assert.Equal(t, tc.want, a.settings.reasoningLevel)
		})
	}
}

func TestLettaNativeEffortNormalization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model    string
		provider string
		effort   string
		want     string
	}{
		{"", "openai", "minimal", "minimal"},
		{"openai/gpt-5", "openai", "minimal", "minimal"},
		{"openai/gpt-5.1", "openai", "minimal", "none"},
		{"openai/gpt-5.1-mini", "openai", "minimal", "none"},
		{"openai/gpt-5.10", "openai", "minimal", "minimal"},
		{" OPENAI/GPT-5.5-fast ", "openai", "max", "xhigh"},
		{"private/route/gpt-5.6-sol", "openai", "minimal", "none"},
		{"openai/gpt-5.6-sol", "openai", "max", "max"},
		{"openai/gpt-6.1-sol", "openai", "minimal", "minimal"},
		{"anthropic/claude-opus-4-6", "anthropic", "xhigh", "max"},
		{"bedrock/claude-opus-4-8", "bedrock", "xhigh", "xhigh"},
		{"anthropic/claude-sonnet-5", "anthropic", "high", "high"},
		{"gpt-5.5", "xai", "minimal", "minimal"},
		{"gpt-5.5", "moonshot", "max", "max"},
		{"gpt-5.5", "google_ai", "minimal", "minimal"},
	} {
		t.Run(tc.model+"/"+tc.provider+"/"+tc.effort, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, lettaNativeEffort(tc.model, tc.provider, tc.effort))
		})
	}
}

func TestLettaEffortPatchRejectsMalformedMetadata(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{"provider_type":7}`, `{"provider_type":""}`, `{"reasoning":[]}`, `{"reasoning":false}`} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			var settings map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(input), &settings))
			patch, err := lettaEffortPatch(settings, nil, lettaModel{id: "openai/one"}, "high", "local")
			require.Error(t, err)
			assert.Nil(t, patch)
		})
	}
}

func TestLettaNativeEffortAliasUsesTheAdvertisedTier(t *testing.T) {
	t.Parallel()
	for _, offersMax := range []bool{false, true} {
		_, a := openedAgent(t)
		model := lettaModel{id: "anthropic/claude-opus-4-6", variants: []lettaModelVariant{{id: "claude-xhigh", effort: "xhigh", providerType: "anthropic"}}}
		if offersMax {
			model.variants = append(model.variants, lettaModelVariant{id: "claude-max", effort: "max", providerType: "anthropic"})
		}
		a.catalog.models = []lettaModel{model}
		require.NoError(t, a.recordNativeModelSettings(&lettaSettingsRecord{
			Model: model.id, ModelSettings: map[string]json.RawMessage{"provider_type": json.RawMessage(`"anthropic"`), "effort": json.RawMessage(`"max"`)},
		}, nil))
		want := "xhigh"
		if offersMax {
			want = "max"
		}
		assert.Equal(t, want, a.settings.reasoningLevel)
	}
}

func TestLettaNativeOpenAIEffortAliasesUseTheCanonicalTier(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{"reasoning_effort":"minimal"}`, `{"reasoning":{"reasoning_effort":"none"},"reasoning_effort":"minimal"}`} {
		_, a := openedAgent(t)
		model := lettaModel{id: "openai-codex/gpt-5.5", variants: []lettaModelVariant{
			{id: "gpt-5.5-minimal", effort: "minimal", providerType: "chatgpt_oauth"},
			{id: "gpt-5.5-none", effort: "none", providerType: "chatgpt_oauth"},
		}}
		a.catalog.models = []lettaModel{model}
		var settings map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(input), &settings))
		require.NoError(t, a.recordNativeModelSettings(&lettaSettingsRecord{Model: model.id, ModelSettings: settings}, nil))
		assert.Equal(t, "none", a.settings.reasoningLevel)
	}
}

func TestLettaConversationResetOverridesLegacyAgentConfiguration(t *testing.T) {
	t.Parallel()
	_, a := openedAgent(t)
	base := &lettaSettingsRecord{Model: "openai/one", LLMConfig: map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"high"`)}}
	conversation := &lettaSettingsRecord{ModelSettings: map[string]json.RawMessage{"reasoning": json.RawMessage(`null`)}}
	require.NoError(t, a.recordNativeModelSettings(base, conversation))
	assert.Equal(t, agent.EffortAuto, a.settings.reasoningLevel)
}

func TestLettaSettingsTimeoutDiscardsTheLateReply(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	fake, a := newFakeAppServerWithClock(t, &agenttest.Sink{}, clock)
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	configureLettaEffortTest(a)
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, "update_model")
	defer trap.Close()
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDModel: "openai/two"}) }()
	command := fake.nextCommand(t)
	require.Equal(t, "update_model", command["type"])
	ctx := testutil.DeadlineContext(t)
	deadline := testutil.WaitForTimer(t, ctx, trap)
	clock.Advance(deadline).MustWait(ctx)
	result := waitModeResult(t, returned)
	assert.False(t, result.AppliedLive)
	assert.Empty(t, result.ConfirmedOptions())
	requestID, ok := command["request_id"].(string)
	require.True(t, ok)
	assert.False(t, a.rpcReplies.IsPendingForTest(requestID))
	raw, err := json.Marshal(map[string]any{
		"type": "update_model_response", "request_id": requestID, "success": true,
		"runtime": command["runtime"], "model_handle": "openai/two", "model_settings": map[string]any{"reasoning_effort": "high"},
	})
	require.NoError(t, err)
	a.handleFrame(raw)
	assert.Equal(t, "openai/one", a.settings.model)
	assert.Equal(t, "low", a.settings.reasoningLevel)
}

func TestLettaRuntimeSettingsRejectForeignRecords(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"agent", "conversation", "conversation owner"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			_, a := openedAgent(t)
			configureLettaEffortTest(a)
			waiter := newRuntimeWaiter()
			a.runtimeReady = waiter
			base := map[string]any{"id": "agent-local-1", "model": "foreign-model"}
			conversation := map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"}
			switch invalid {
			case "agent":
				base["id"] = "foreign"
			case "conversation":
				conversation["id"] = "foreign"
			case "conversation owner":
				conversation["agent_id"] = "foreign"
			}
			raw, err := json.Marshal(map[string]any{
				"type": "runtime_start_response", "success": true,
				"runtime":      map[string]string{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
				"agent":        base,
				"conversation": conversation,
			})
			require.NoError(t, err)
			a.handleFrame(raw)
			require.Error(t, waiter.wait(a.Context(), a.Clock(), a.ProcessDone(), lettaSendWait))
			assert.Equal(t, "openai/one", a.settings.model)
			assert.Equal(t, "low", a.settings.reasoningLevel)
		})
	}
}

func answerLettaEffortRead(t *testing.T, fake *fakeAppServer, model string, settings map[string]any) {
	t.Helper()
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
	base := fake.nextCommand(t)
	require.Equal(t, "agent_retrieve", base["type"])
	replyLettaSettings(t, fake, base, map[string]any{"agent": map[string]any{"id": "agent-local-1", "model": model, "model_settings": map[string]any{"provider_type": "openai", "reasoning_effort": "low"}}})
	read := fake.nextCommand(t)
	require.Equal(t, "conversation_retrieve", read["type"])
	replyLettaSettings(t, fake, read, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": settings}})
}

func TestLettaEffortRejectsAnUnavailableTierBeforeWriting(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	configureLettaEffortTest(a)
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "unavailable"}) }()
	answerLettaEffortRead(t, fake, "openai/one", nil)
	ctx := testutil.DeadlineContext(t)
	select {
	case command := <-fake.commands:
		t.Fatalf("an unavailable effort reached a native mutation: %v", command["type"])
	case result := <-returned:
		assert.True(t, result.AppliedLive)
		assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
	case <-ctx.Done():
		t.Fatal("the unavailable effort did not settle")
	}
}

func TestLettaConcurrentEffortUpdatesPreserveTheLatestRead(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	configureLettaEffortTest(a)
	first := make(chan agent.SettingsApplyResult, 1)
	go func() { first <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"}) }()
	answerLettaEffortRead(t, fake, "openai/one", map[string]any{"max_tokens": 9000})
	write := fake.nextCommand(t)
	require.Equal(t, "conversation_update", write["type"])
	second := make(chan agent.SettingsApplyResult, 1)
	go func() { second <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "low"}) }()
	settings := map[string]any{"provider_type": "openai", "max_tokens": 9000, "reasoning_effort": "high", "native_marker": "first update"}
	replyLettaSettings(t, fake, write, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": settings}})
	assert.Equal(t, "high", waitModeResult(t, first).ConfirmedOptions()[agent.OptionIDEffort])
	answerLettaEffortRead(t, fake, "openai/one", settings)
	write = fake.nextCommand(t)
	require.Equal(t, "conversation_update", write["type"])
	body, ok := write["body"].(map[string]any)
	require.True(t, ok)
	patch, ok := body["model_settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "first update", patch["native_marker"])
	assert.Equal(t, "low", patch["reasoning_effort"])
	replyLettaSettings(t, fake, write, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": patch}})
	assert.Equal(t, "low", waitModeResult(t, second).ConfirmedOptions()[agent.OptionIDEffort])
	assert.Equal(t, "low", a.settings.reasoningLevel)
}

func TestLettaRepeatedEffortKeepsTheConfirmedSettings(t *testing.T) {
	t.Parallel()
	for _, effort := range []string{"low", agent.EffortAuto, ""} {
		t.Run(effort, func(t *testing.T) {
			t.Parallel()
			fake, a := openedAgent(t)
			configureLettaEffortTest(a)
			want := effort
			if want == "" {
				want = agent.EffortAuto
			}
			a.settings.reasoningLevel = want
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: effort}) }()
			ctx := testutil.DeadlineContext(t)
			select {
			case command := <-fake.commands:
				t.Fatalf("an unchanged effort started a native settings transaction: %v", command["type"])
			case result := <-returned:
				assert.True(t, result.AppliedLive)
				assert.Equal(t, want, result.ConfirmedOptions()[agent.OptionIDEffort])
			case <-ctx.Done():
				t.Fatal("the unchanged effort did not settle")
			}
		})
	}
}

func TestLettaEffortRejectsInvalidReadSnapshots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		step   int
		fields map[string]any
	}{
		{"unknown backend", 0, map[string]any{"backend": "unknown"}},
		{"missing agent", 1, map[string]any{}},
		{"foreign agent", 1, map[string]any{"agent": map[string]any{"id": "foreign"}}},
		{"missing conversation", 2, map[string]any{}},
		{"foreign conversation", 2, map[string]any{"conversation": map[string]any{"id": "foreign", "agent_id": "agent-local-1"}}},
		{"foreign owner", 2, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "foreign"}}},
		{"malformed settings", 2, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": []any{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, a := openedAgent(t)
			configureLettaEffortTest(a)
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"}) }()
			steps := []map[string]any{
				{"backend": "local"},
				{"agent": map[string]any{"id": "agent-local-1", "model": "openai/one"}},
				{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"}},
			}
			for i := 0; i <= tc.step; i++ {
				command := fake.nextCommand(t)
				require.Equal(t, []string{"app_server_info", "agent_retrieve", "conversation_retrieve"}[i], command["type"])
				fields := steps[i]
				if i == tc.step {
					fields = tc.fields
				}
				replyLettaSettings(t, fake, command, fields)
			}
			ctx := testutil.DeadlineContext(t)
			select {
			case command := <-fake.commands:
				t.Fatalf("an invalid snapshot reached another native request: %v", command["type"])
			case result := <-returned:
				assert.False(t, result.AppliedLive)
				assert.Empty(t, result.ConfirmedOptions())
				assert.Equal(t, "low", a.settings.reasoningLevel)
			case <-ctx.Done():
				t.Fatal("the invalid settings snapshot did not settle")
			}
		})
	}
}

func TestLettaDefaultConversationUpdatesOnlyItsAgentSettings(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	configureLettaEffortTest(a)
	a.conversationID = "default"
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() { returned <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"}) }()
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
	read := fake.nextCommand(t)
	require.Equal(t, "agent_retrieve", read["type"])
	replyLettaSettings(t, fake, read, map[string]any{"agent": map[string]any{
		"id": "agent-local-1", "model": "openai/one", "model_settings": map[string]any{"provider_type": "openai", "max_tokens": 7000, "reasoning_effort": "low"},
	}})
	write := fake.nextCommand(t)
	require.Equal(t, "agent_update", write["type"])
	assert.Equal(t, "agent-local-1", write["agent_id"])
	assert.NotContains(t, write, "conversation_id")
	body, ok := write["body"].(map[string]any)
	require.True(t, ok)
	patch, ok := body["model_settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, json.Number("7000"), patch["max_tokens"])
	assert.Equal(t, "high", patch["reasoning_effort"])
	replyLettaSettings(t, fake, write, map[string]any{"agent": map[string]any{"id": "agent-local-1", "model": "openai/one", "model_settings": patch}})
	assert.Equal(t, "high", waitModeResult(t, returned).ConfirmedOptions()[agent.OptionIDEffort])
}

func TestLettaInputWaitsForTheSettingsTransaction(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	configureLettaEffortTest(a)
	updated := make(chan agent.SettingsApplyResult, 1)
	go func() { updated <- a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"}) }()
	info := fake.nextCommand(t)
	require.Equal(t, "app_server_info", info["type"])
	sent := make(chan error, 1)
	go func() { sent <- a.SendInput("Use the newly selected effort.", nil) }()
	replyLettaSettings(t, fake, info, map[string]any{"backend": "local"})
	read := fake.nextCommand(t)
	require.Equal(t, "agent_retrieve", read["type"], "input must not enter the native settings transaction")
	replyLettaSettings(t, fake, read, map[string]any{"agent": map[string]any{"id": "agent-local-1", "model": "openai/one"}})
	read = fake.nextCommand(t)
	require.Equal(t, "conversation_retrieve", read["type"])
	replyLettaSettings(t, fake, read, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1"}})
	write := fake.nextCommand(t)
	require.Equal(t, "conversation_update", write["type"])
	replyLettaSettings(t, fake, write, map[string]any{"conversation": map[string]any{"id": "local-conv-1", "agent_id": "agent-local-1", "model_settings": map[string]any{"reasoning_effort": "high"}}})
	input := fake.nextCommand(t)
	require.Equal(t, "input", input["type"])
	assert.Equal(t, "high", waitModeResult(t, updated).ConfirmedOptions()[agent.OptionIDEffort])
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-sent:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("input did not proceed after the native settings reply")
	}
}
