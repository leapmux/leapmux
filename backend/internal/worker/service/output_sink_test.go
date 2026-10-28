package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
)

// newTestRootOutputSink checks root source allocation without fabricating stored agents.
func newTestRootOutputSink(t testing.TB, output *OutputHandler, agentID string, provider leapmuxv1.AgentProvider) agent.ProviderServices {
	t.Helper()
	services, err := output.NewRootSink(agentID, provider)
	require.NoError(t, err)
	return services
}

// fixtureOutputGeneration gives raw storage fixtures an actual durable writer allocation.
func fixtureOutputGeneration(t testing.TB, queries *db.Queries, ownerID string) int64 {
	t.Helper()
	row, err := queries.GetAgentByID(context.Background(), ownerID)
	require.NoError(t, err)
	if row.OutputGeneration > 0 {
		return row.OutputGeneration
	}
	source, err := queries.AllocateAgentOutputGeneration(context.Background(), ownerID)
	require.NoError(t, err)
	return source.OutputGeneration
}

func writeBackgroundTaskFixture(t testing.TB, queries *db.Queries, task db.UpsertAgentBackgroundTaskParams) {
	t.Helper()
	task.OutputGeneration = fixtureOutputGeneration(t, queries, task.OwnerAgentID)
	rows, err := queries.UpsertAgentBackgroundTask(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}

func updateBackgroundTaskFixture(t testing.TB, queries *db.Queries, task db.UpdateAgentBackgroundTaskStatusParams) {
	t.Helper()
	task.OutputGeneration = fixtureOutputGeneration(t, queries, task.OwnerAgentID)
	rows, err := queries.UpdateAgentBackgroundTaskStatus(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}

func closeBackgroundTaskFixture(t testing.TB, queries *db.Queries, task db.CloseAgentBackgroundTaskParams) {
	t.Helper()
	task.OutputGeneration = fixtureOutputGeneration(t, queries, task.OwnerAgentID)
	rows, err := queries.CloseAgentBackgroundTask(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}
