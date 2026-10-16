package qoder

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitDisplayLabelDoesNotReplaceTheModelID(t *testing.T) {
	t.Parallel()
	for _, current := range []string{"mockprov/primary", ""} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.model = current
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","model":"Model label","session_id":"session-1"}`))
			assert.Equal(t, current, a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
			a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"mockprov/next","displayName":"Model label"}],"currentModel":"mockprov/next"}`))
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
