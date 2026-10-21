package qoder

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type qoderInitializeStdin struct {
	agent       *Agent
	reply       json.RawMessage
	beforeReply []byte
	request     map[string]any
}

func (s *qoderInitializeStdin) Write(raw []byte) (int, error) {
	var frame struct {
		RequestID string         `json:"request_id"`
		Request   map[string]any `json:"request"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return 0, err
	}
	s.request = frame.Request
	if len(s.beforeReply) > 0 {
		s.agent.HandleOutput(s.beforeReply)
	}
	response, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": frame.RequestID, "response": s.reply}})
	if err != nil {
		return 0, err
	}
	s.agent.HandleOutput(response)
	return len(raw), nil
}

func (*qoderInitializeStdin) Close() error { return nil }

func qoderModelIDs(a *Agent) []string {
	var ids []string
	for _, group := range a.OptionGroups() {
		if group.Id == agent.OptionIDModel {
			for _, option := range group.Options {
				ids = append(ids, option.Id)
			}
		}
	}
	return ids
}

func TestQoderInitializeAsksForModelUpdates(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	a.opts.HomeDir = t.TempDir()
	stdin := &qoderInitializeStdin{agent: a, reply: json.RawMessage(`{"models":[]}`)}
	a.SetStdinForTest(stdin)
	require.NoError(t, a.initializeStream())
	assert.Equal(t, map[string]any{"subtype": "initialize", "supportsAvailableModelsUpdate": true}, stdin.request)
}

func TestQoderCatalogListsOnlyEnabledModels(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	a.opts.HomeDir = t.TempDir()
	a.model = "disabled-current"
	// Source: .tmp/probe/qoder/real/run1/logs/frames.jsonl, initialize reply at frame 4.
	raw, err := os.ReadFile("testdata/initialize_models.json")
	require.NoError(t, err)
	a.SetStdinForTest(&qoderInitializeStdin{agent: a, reply: raw})
	require.NoError(t, a.initializeStream())
	assert.Equal(t, []string{"qfmodel", "zhipu-intl/glm5.3-cp"}, qoderModelIDs(a))
}

func TestQoderCatalogEmptyDoesNotRestoreConfiguredModels(t *testing.T) {
	t.Parallel()
	a := configuredQoderAgent(t, `{"providers":{"local":{"models":[{"model":"disabled"}]}}}`)
	a.model = "local/disabled"
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"local/disabled","isEnabled":false}],"currentModel":"local/disabled"}`))
	assert.Empty(t, qoderModelIDs(a))
}

func TestQoderModelsUpdateRefreshesTheSettings(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.opts.HomeDir = t.TempDir()
	raw := []byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"enabled","displayName":"Enabled","isEnabled":true},{"value":"disabled","isEnabled":false}],"currentModel":"enabled"}`)
	a.HandleOutput(raw)
	assert.Equal(t, []string{"enabled"}, qoderModelIDs(a))
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "enabled", sink.LastSettingsRefresh().Model)
	a.HandleOutput(raw)
	assert.Equal(t, 1, sink.SettingsRefreshCount())
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"next","isEnabled":true}],"currentModel":"next"}`))
	assert.Equal(t, 2, sink.SettingsRefreshCount())
	assert.Equal(t, []string{"next"}, qoderModelIDs(a))
	assert.Empty(t, sink.Messages())
}

func TestQoderInitializeKeepsANewerReportedCatalog(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	a.opts.HomeDir = t.TempDir()
	a.SetStdinForTest(&qoderInitializeStdin{
		agent:       a,
		reply:       json.RawMessage(`{"models":[{"value":"old","isEnabled":true}]}`),
		beforeReply: []byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"new","isEnabled":true}],"currentModel":"new"}`),
	})
	require.NoError(t, a.initializeStream())
	assert.Equal(t, []string{"new"}, qoderModelIDs(a))
}

func TestQoderCatalogValidatesEnabledChoices(t *testing.T) {
	t.Parallel()
	got := qoderEnabledModels([]qoderModelEntry{
		{Value: "duplicate", IsEnabled: false},
		{Value: "duplicate", DisplayName: "  ", IsEnabled: true},
		{Value: "duplicate", DisplayName: "Second", IsEnabled: true},
		{Value: "", IsEnabled: true},
		{Value: "  ", IsEnabled: true},
		{Value: "unspecified"},
	})
	assert.Equal(t, []qoderModel{{id: "duplicate", displayName: "duplicate"}}, got)
	assert.Empty(t, qoderEnabledModels(nil))
}

func TestQoderCatalogMalformedUpdateKeepsTheCurrentChoices(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.opts.HomeDir = t.TempDir()
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"valid","isEnabled":true}],"currentModel":"valid"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"invalid","isEnabled":"true"}],"currentModel":"invalid"}`))
	assert.Equal(t, []string{"valid"}, qoderModelIDs(a))
	assert.Equal(t, "valid", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, 1, sink.SettingsRefreshCount())
}

func TestInitDisplayLabelDoesNotReplaceTheModelID(t *testing.T) {
	t.Parallel()
	for _, current := range []string{"mockprov/primary", ""} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.model = current
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","model":"Model label","session_id":"session-1"}`))
			assert.Equal(t, current, a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
			a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"mockprov/next","displayName":"Model label","isEnabled":true}],"currentModel":"mockprov/next"}`))
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","model":"Model label","session_id":"session-1"}`))
			options := a.SettingsSnapshot().ConfirmedOptions()
			require.Equal(t, "mockprov/next", options[agent.OptionIDModel])
			stdin := &modelControlStdin{agent: a}
			a.SetStdinForTest(stdin)
			result := a.UpdateSettings(map[string]string{agent.OptionIDModel: options[agent.OptionIDModel]})
			require.True(t, result.AppliedLive)
			wire, err := json.Marshal(stdin.request())
			require.NoError(t, err)
			assert.JSONEq(t, `{"subtype":"set_model","model":"mockprov/next"}`, string(wire))
		})
	}
}
