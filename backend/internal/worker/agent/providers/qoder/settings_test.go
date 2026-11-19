package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestPermissionPolicyChangesTheOfferedModesWithoutATranscriptRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	modeIDs := func() []string {
		for _, group := range a.OptionGroups() {
			if group.Id == agent.OptionIDPermissionMode {
				var ids []string
				for _, option := range group.Options {
					ids = append(ids, option.Id)
				}
				return ids
			}
		}
		return nil
	}
	require.Contains(t, modeIDs(), "auto")
	a.HandleOutput([]byte(`{"type":"system","subtype":"permission_policy","session_id":"session-1","permissionPolicy":{"disabledModes":["auto","bypassPermissions"],"disabledModeMessages":{}}}`))
	assert.NotContains(t, modeIDs(), "auto")
	assert.NotContains(t, modeIDs(), "bypassPermissions")
	assert.Contains(t, modeIDs(), "default")
	assert.Empty(t, sink.Messages())
	a.HandleOutput([]byte(`{"type":"system","subtype":"permission_policy","session_id":"session-1","permissionPolicy":{"disabledModes":"bad"}}`))
	assert.NotContains(t, modeIDs(), "auto")
	a.HandleOutput([]byte(`{"type":"system","subtype":"permission_policy","session_id":"session-1","permissionPolicy":{"disabledModes":[]}}`))
	assert.Contains(t, modeIDs(), "auto")
	assert.Contains(t, modeIDs(), "bypassPermissions")
}

func TestQoderChangedEffortRequiresResumeRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options map[string]string
	}{
		{name: "effort alone", options: map[string]string{agent.OptionIDEffort: "low"}},
		{name: "effort with model", options: map[string]string{agent.OptionIDEffort: "low", agent.OptionIDModel: "mockprov/alternate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.opts = agent.Options{Options: map[string]string{
				agent.OptionIDEffort: "high",
				agent.OptionIDModel:  "mockprov/primary",
			}}
			a.effort = "high"
			a.model = "mockprov/primary"
			assert.Equal(t, agent.RestartRequiredSettings(tc.options), a.UpdateSettings(tc.options))
			assert.Equal(t, "mockprov/primary", a.model, "a restart request must not change the live model first")
		})
	}
}

func TestQoderUnchangedEffortConfirmsLive(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.opts = agent.Options{Options: map[string]string{agent.OptionIDEffort: "high"}}
	a.effort = "high"
	result := a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "high", *result.Settlements[agent.OptionIDEffort].Value)
}
