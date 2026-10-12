package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

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
