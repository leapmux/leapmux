package droid

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Droid is not a provider that the Worker resets the effort for: its effort ladder belongs to the
// native model, and the native session keeps the effort over a model switch when the new model
// offers it, and takes the default of the new model when it does not (the session controller
// clamps through the model config). So the Worker sends the merged options of a model-only edit,
// and the provider sends the model alone. The effort that the native `settings_updated` event
// reports, clamped or not, is the effort that the provider confirms.
func TestUpdateSettingsModelSwitchSendsNoEffortAndConfirmsTheNativeClamp(t *testing.T) {
	t.Parallel()
	writer := &droidRequestWriter{requests: make(chan droidEnvelope, 1)}
	sink := &agenttest.Sink{}
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "droid-model-switch-test", Stdin: writer, Ctx: context.Background(), ProcessDone: processDone,
		}),
		sink: agent.NewProviderServices(sink), sessionID: "native-session",
		settings: droidSettings{model: "custom:Droid-0", reasoningEffort: "none", permissionMode: "default", interactionMode: "auto", autonomyLevel: "off"},
		catalog: droidCatalog{models: []droidModel{
			{id: "custom:Droid-0", efforts: []string{"none"}},
			{id: "claude-fable-5.1", efforts: []string{"low", "medium", "high"}},
		}},
	}
	t.Cleanup(func() {
		close(processDone)
		a.Process.Stop()
	})
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	result := make(chan agent.SettingsApplyResult, 1)
	go func() {
		result <- a.UpdateSettings(map[string]string{
			agent.OptionIDModel: "claude-fable-5.1", agent.OptionIDEffort: "none", agent.OptionIDPermissionMode: "default",
		})
	}()
	var request droidEnvelope
	select {
	case request = <-writer.requests:
	case <-deadline.C:
		t.Fatal("Droid did not receive the settings request")
	}
	var params map[string]any
	require.NoError(t, json.Unmarshal(request.Params, &params))
	assert.Equal(t, map[string]any{"modelId": "claude-fable-5.1"}, params, "an inherited effort and an unchanged mode are not sent")

	event, err := json.Marshal(map[string]any{
		"type": "settings_updated", "requestId": request.ID,
		"settings": map[string]any{"modelId": "claude-fable-5.1", "reasoningEffort": "medium", "interactionMode": "auto", "autonomyLevel": "off"},
	})
	require.NoError(t, err)
	notification := newDroidEnvelope(droidTypeNotification)
	notification.Method = droidMethodSessionNotif
	notification.Params, err = json.Marshal(map[string]any{"sessionId": "native-session", "notification": json.RawMessage(event)})
	require.NoError(t, err)
	line, err := notification.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{}`)
	line, err = reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)

	select {
	case applied := <-result:
		assert.True(t, applied.AppliedLive)
		assert.Equal(t, "claude-fable-5.1", applied.ConfirmedOptions()[agent.OptionIDModel])
		assert.Equal(t, "medium", applied.ConfirmedOptions()[agent.OptionIDEffort], "the native session clamped none to the default of the new model")
		assert.Equal(t, "default", applied.ConfirmedOptions()[agent.OptionIDPermissionMode])
	case <-deadline.C:
		t.Fatal("Droid did not settle the settings request")
	}
	assert.Equal(t, "medium", sink.LastSettingsRefresh().Effort, "the stored row learns the clamped effort")
}
