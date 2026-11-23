package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeAssemblyModelChangeKeepsUnchangedEffort(t *testing.T) {
	t.Parallel()
	var selected openCodeModelRef
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/session/ses_1/model", r.URL.Path)
		var body struct {
			Model openCodeModelRef `json:"model"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		selected = body.Model
		w.WriteHeader(http.StatusNoContent)
	})
	a, _ := assemblyAgent(t, rpc)
	a.root.selectedModel = &openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "high"}
	a.root.catalog.models[1].Variants = a.root.catalog.models[0].Variants
	other := a.root.catalog.models[1]
	result := a.UpdateSettings(optionmap.Map{
		agent.OptionIDModel:  openCodeModelKey(openCodeModelRef{ProviderID: other.ProviderID, ID: other.ID}),
		agent.OptionIDEffort: "variant:high",
	})
	require.True(t, result.AppliedLive)
	assert.Equal(t, "high", selected.Variant)
	assert.Equal(t, "variant:high", result.SurfacedOptions[agent.OptionIDEffort])
}

func TestNativeAssemblyRestoresEffortFromNativeModelHistory(t *testing.T) {
	t.Parallel()
	var selected openCodeModelRef
	var historyReads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/ses_1/message" {
			historyReads.Add(1)
			assert.Equal(t, "model-switched", r.URL.Query().Get("type"))
			assert.Equal(t, "desc", r.URL.Query().Get("order"))
			_, _ = w.Write([]byte(`{"data":[{"id":"switch_1","type":"model-switched","model":{"providerID":"other","id":"model/special"},"previous":{"providerID":"provider","id":"model","variant":"low"}}],"cursor":{}}`))
			return
		}
		assert.Equal(t, "/api/session/ses_1/model", r.URL.Path)
		var body struct {
			Model openCodeModelRef `json:"model"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		selected = body.Model
		w.WriteHeader(http.StatusNoContent)
	})
	a, _ := assemblyAgent(t, rpc)
	a.root.selectedModel = &openCodeModelRef{ProviderID: "other", ID: "model/special"}
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "provider/model", agent.OptionIDEffort: "auto"})
	require.True(t, result.AppliedLive)
	assert.EqualValues(t, 1, historyReads.Load())
	assert.Equal(t, "low", selected.Variant)
	assert.Equal(t, "variant:low", result.SurfacedOptions[agent.OptionIDEffort])
}

func TestNativeAssemblyRefusesModelChangeWhenPreferenceReadFails(t *testing.T) {
	t.Parallel()
	var writes atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a, _ := assemblyAgent(t, rpc)
	a.root.selectedModel = &openCodeModelRef{ProviderID: "other", ID: "model/special"}
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "provider/model", agent.OptionIDEffort: "auto"})
	require.True(t, result.AppliedLive, "the failure must settle without restarting")
	assert.Equal(t, "other/model/special", result.SurfacedOptions[agent.OptionIDModel])
	assert.Zero(t, writes.Load())
}

func TestNativeAssemblyRoutesClassifiedNoticesThroughNotificationReduction(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	for _, kind := range []string{"session.compaction.started", "session.compaction.delta", "session.compaction.ended", "session.compaction.failed", "session.retry.scheduled"} {
		require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, kind, map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "text": "notice"})))
	}
	assert.Len(t, sink.PersistedNotifications(), 5)
	assert.Empty(t, sink.Messages())
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.compaction.ended", map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "text": "notice"})))
	assert.Len(t, sink.PersistedNotifications(), 5)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.future.metadata", map[string]any{"sessionID": "ses_1"})))
	assert.Len(t, sink.Messages(), 1)
}

func TestNativeAssemblyResolvesRetryAfterASuccessfulStep(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	for _, kind := range []string{"session.retry.scheduled", "session.step.ended", "session.step.ended", "session.execution.succeeded"} {
		require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, kind, map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "attempt": 1, "at": 0})))
	}
	assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.Notifications())
}

func TestNativeAssemblyResolvesRetryBeforeToolContent(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	for _, kind := range []string{"session.retry.scheduled", "session.step.started", "session.step.first-token"} {
		require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, kind, map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "attempt": 1, "at": 0})))
	}
	assert.Empty(t, sink.Messages(), "step metadata must not split the consecutive notification history")
	assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.Notifications())
}

func TestNativeAssemblyMalformedStepCannotResolveRetry(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.retry.scheduled", map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "attempt": 1, "at": 0})))
	require.Error(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.step.ended", map[string]any{"sessionID": "ses_1"})))
	assert.True(t, a.root.retryResolution.Pending())
	assert.Empty(t, sink.Notifications())
}

func assemblyAgent(t *testing.T, rpc openCodeRPC) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	sink := &agenttest.ControlSink{}
	services := agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a := &Agent{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: ctx, Cancel: cancel}), sink: services, rpc: rpc, catalog: openCodeCatalogFixture(t), runs: make(map[string]*openCodeRun), nativeCancel: cancel}
	controls, err := newOpenCodeControls(ctx, rpc, a.requestRecovery)
	require.NoError(t, err)
	a.controls = controls
	run, err := a.newRun(openCodeSessionInfo{ID: "ses_1", Agent: "build"}, services)
	require.NoError(t, err)
	a.root = run
	a.runs[run.id] = run
	services.AdoptNativeSession(run.id)
	t.Cleanup(func() {
		cancel()
		a.closeNativeShellReaders()
		controls.close()
		a.dispatch.Lock()
		defer a.dispatch.Unlock()
		assert.NoError(t, a.finishNativeOutputs(agent.MessageCompletionInterrupted))
	})
	return a, sink
}
func assemblyEvent(t *testing.T, kind string, data any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": "evt_" + kind, "created": 0, "type": kind, "data": data})
	require.NoError(t, err)
	return raw
}

func TestNativeAssemblySettlesPermissionByRequestID(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("native settlement must not send a reply")
		w.WriteHeader(http.StatusInternalServerError)
	})
	a, sink := assemblyAgent(t, rpc)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "permission.asked", map[string]any{"sessionID": "ses_1", "id": "per_1", "action": "shell", "resources": []string{}})))
	require.Len(t, sink.PublishedControls(), 1)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "permission.replied", map[string]any{"sessionID": "ses_1", "requestID": "per_1", "reply": "once"})))
	assert.Equal(t, []string{openCodeControlKey("permission", "ses_1", "per_1")}, sink.CanceledControls())
}

func TestNativeAssemblyEndsShutdownInterruptionWhileTheServerRemainsAlive(t *testing.T) {
	t.Parallel()
	for _, stopping := range []bool{false, true} {
		t.Run(fmt.Sprint(stopping), func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
			a, sink := assemblyAgent(t, rpc)
			a.root.setNativeActivity(true, false)
			if stopping {
				a.NoteIntentionalStop()
			}
			require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.execution.interrupted", map[string]any{"sessionID": "ses_1", "reason": "shutdown"})))
			if stopping {
				assert.Empty(t, sink.Messages())
				return
			}
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.True(t, rows[0].TurnEnd)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
			assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
			assert.False(t, a.root.PublishTurnActive().Active)
		})
	}
}

func TestNativeAssemblyRefreshesMovedSession(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/session/ses_1", r.URL.Path)
		reads.Add(1)
		_, err := w.Write([]byte(`{"data":{"id":"ses_1","agent":"plan"}}`))
		assert.NoError(t, err)
	})
	a, _ := assemblyAgent(t, rpc)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.moved", map[string]any{"sessionID": "ses_1", "location": map[string]any{"directory": "/project"}})))
	assert.EqualValues(t, 1, reads.Load())
	a.root.mu.Lock()
	defer a.root.mu.Unlock()
	assert.Equal(t, "plan", a.root.selectedAgentID)
}

func TestNativeAssemblySettlesSessionAndGlobalForms(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"ses_1", "global"} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("native settlement must not send a reply")
				w.WriteHeader(http.StatusInternalServerError)
			})
			a, sink := assemblyAgent(t, rpc)
			form := map[string]any{"id": "frm_1", "sessionID": owner, "fields": []map[string]any{{"key": "value", "type": "string"}}, "metadata": map[string]any{"kind": "mcp-elicitation"}}
			require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "form.created", map[string]any{"form": form})))
			require.Len(t, sink.PublishedControls(), 1)
			require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "form.replied", map[string]any{"sessionID": owner, "id": "frm_1", "answer": map[string]any{}})))
			assert.Equal(t, []string{openCodeControlKey("form", owner, "frm_1")}, sink.CanceledControls())
		})
	}
}

func TestNativeAssemblyRecoversMixedOutputInNativeContentOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	services := agent.NewProviderServices(sink)
	text, tools := newOpenCodeTextStreams("ses_1", services), newOpenCodeToolStreams("ses_1", services)
	message := storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"reasoning","text":"think","time":{"completed":0}},{"type":"tool","id":"call_1","name":"read","state":{"status":"completed","input":{},"content":[{"type":"text","text":"file"}]}},{"type":"text","text":"answer"}],"time":{"completed":1}}`)
	snapshot, err := readOpenCodeAssistantSnapshot(message)
	require.NoError(t, err)
	require.NoError(t, recoverOpenCodeAssistantOutput(message.ID, snapshot, text, tools))
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Contains(t, string(rows[0].Content), `"kind":"reasoning"`)
	assert.False(t, rows[1].Closing)
	assert.True(t, rows[2].Closing)
	assert.Contains(t, string(rows[3].Content), `"text":"answer"`)
	require.NoError(t, recoverOpenCodeAssistantOutput(message.ID, snapshot, text, tools))
	assert.Len(t, sink.Messages(), 4)
}

func TestNativeAssemblyCompleteSettingsKeepModelInheritance(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"none", "automatic approval", "agent"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/api/session/ses_1/agent", r.URL.Path)
				w.WriteHeader(http.StatusNoContent)
			})
			a, sink := assemblyAgent(t, rpc)
			a.root.catalog.agents[1].Model = &openCodeModelRef{ProviderID: "other", ID: "model/special"}
			options := a.SettingsSnapshot().ConfirmedOptions()
			expectedCalls, expectedRefreshes := int32(0), 0
			switch change {
			case "automatic approval":
				options[AutoApproveGroup.Id] = "true"
				expectedRefreshes = 1
			case "agent":
				options[agent.OptionIDPrimaryAgent] = "plan"
				expectedCalls, expectedRefreshes = 1, 1
			}
			originalOptions := maps.Clone(options)
			result := a.UpdateSettings(options)
			require.True(t, result.AppliedLive)
			assert.Equal(t, expectedCalls, calls.Load())
			assert.Nil(t, a.root.selectedModel)
			assert.Equal(t, expectedRefreshes, sink.SettingsRefreshCount())
			if change == "agent" {
				assert.Equal(t, "other/model/special", result.ConfirmedOptions()[agent.OptionIDModel])
			}
			if change == "automatic approval" {
				assert.True(t, a.controls.autoApproveEnabled())
			}
			assert.Equal(t, originalOptions, options, "the facade must not mutate the caller's options")
		})
	}
}

func TestOpenCodeRetireRunResetsSpansAndProgress(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	a.root.setNativeActivity(true, false)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.tool.input.started", map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "id": "call_retire", "name": "shell"})))
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.tool.called", map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_1", "id": "call_retire", "input": map[string]any{"command": "sleep 30"}, "executed": false})))
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.execution.succeeded", map[string]any{"sessionID": "ses_1"})))
	require.NotContains(t, sink.TurnLifecycle(), "reset_spans")

	a.dispatch.Lock()
	err := a.retireRunLocked(a.root, agent.MessageCompletionInterrupted)
	a.dispatch.Unlock()
	require.NoError(t, err)
	assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}
