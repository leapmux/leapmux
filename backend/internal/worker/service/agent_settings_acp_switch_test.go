//go:build unix

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// An ACP provider owns no model-dependent effort catalog, so resetEffortToAutoIfUnsupported
// leaves its effort alone. The browser sends only the model when the user changes only the
// model. The running provider must still receive the effort that the row stores, because
// a server can reset its own effort when it changes the model, and the provider writes the
// stored value again. This test pins that premise of the provider tests.
func TestUpdateAgentSettings_ModelSwitchGivesTheStoredEffortToARunningACPAgent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE
	row := db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:  "leapmux/model-a",
			agent.OptionIDEffort: "high",
		}),
	}
	require.NoError(t, svc.Queries.CreateAgent(ctx, row))
	_, err := svc.Agents.StartAgentWith(ctx,
		agent.Options{AgentID: row.ID, AgentProvider: provider, WorkingDir: row.WorkingDir},
		svc.Output.NewSink(row.ID, provider),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			return newRunningTestAgent(), nil
		})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(row.ID) })

	var applied OptionMap
	svc.updateAgentSettingsFn = func(_ string, options OptionMap) agent.SettingsApplyResult {
		applied = options.Clone()
		return agent.ConfirmedSettings(options)
	}

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  row.ID,
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: "leapmux/model-b"}},
	}, w)

	require.Empty(t, w.errors)
	assert.Equal(t, "leapmux/model-b", applied[agent.OptionIDModel])
	assert.Equal(t, "high", applied[agent.OptionIDEffort],
		"the update that reaches the provider carries the stored effort, not the model alone")
	stored, err := svc.Queries.GetAgentByID(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "high", parseOptions(stored.Options)[agent.OptionIDEffort], "the row keeps the stored effort")
}
