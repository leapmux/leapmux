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

type droidRequestWriter struct {
	requests chan droidEnvelope
}

func (w *droidRequestWriter) Write(data []byte) (int, error) {
	var request droidEnvelope
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	w.requests <- request
	return len(data), nil
}

func (*droidRequestWriter) Close() error { return nil }

func TestUpdateSettingsSendsNativeFieldsAndConfirmsTheReply(t *testing.T) {
	t.Parallel()
	writer := &droidRequestWriter{requests: make(chan droidEnvelope, 1)}
	sink := &agenttest.Sink{}
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "droid-settings-test", Stdin: writer, Ctx: context.Background(), ProcessDone: processDone,
		}),
		sink: agent.NewProviderServices(sink), sessionID: "native-session",
		settings: droidSettings{model: "custom:Droid-0", reasoningEffort: "none", permissionMode: "default"},
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
			agent.OptionIDModel: "claude-fable-5.1", agent.OptionIDEffort: "high",
		})
	}()
	var request droidEnvelope
	select {
	case request = <-writer.requests:
	case <-deadline.C:
		t.Fatal("Droid did not receive the settings request")
	}
	assert.Equal(t, droidMethodUpdateSessionSettings, request.Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(request.Params, &params))
	assert.Equal(t, "claude-fable-5.1", params["modelId"])
	assert.Equal(t, "high", params["reasoningEffort"])
	assert.NotContains(t, params, "settings")
	assert.NotContains(t, params, "sessionId")

	event, err := json.Marshal(map[string]any{
		"type": "settings_updated", "requestId": request.ID,
		"settings": map[string]any{"modelId": "claude-fable-5.1", "reasoningEffort": "high", "interactionMode": "auto", "autonomyLevel": "off"},
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
		assert.Equal(t, "high", applied.ConfirmedOptions()[agent.OptionIDEffort])
	case <-deadline.C:
		t.Fatal("Droid did not settle the settings request")
	}
}

func TestNativeSpecExitPersistsTheNewMode(t *testing.T) {
	t.Parallel()
	fixture := newTestAgent(t)
	a := fixture.agent
	a.settings = droidSettings{
		model: "custom:Droid-0", reasoningEffort: "none",
		interactionMode: "spec", autonomyLevel: "off", permissionMode: "spec",
	}
	a.HandleOutput([]byte(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"s-1","notification":{"type":"settings_updated","settings":{"modelId":"custom:Droid-0","reasoningEffort":"none","interactionMode":"auto","autonomyLevel":"off"}}}}`))
	require.Equal(t, 1, fixture.sink.SettingsRefreshCount())
	assert.Equal(t, "default", fixture.sink.LastSettingsRefresh().PermissionMode)
	assert.Equal(t, "default", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDPermissionMode])
}
