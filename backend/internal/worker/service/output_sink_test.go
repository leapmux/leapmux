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

func writeBackgroundTaskFixture(t testing.TB, queries *db.Queries, task db.UpsertAgentBackgroundTaskParams) {
	t.Helper()
	rows, err := queries.UpsertAgentBackgroundTask(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}

func updateBackgroundTaskFixture(t testing.TB, queries *db.Queries, task db.UpdateAgentBackgroundTaskStatusParams) {
	t.Helper()
	rows, err := queries.UpdateAgentBackgroundTaskStatus(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}

func closeBackgroundTaskFixture(t testing.TB, queries *db.Queries, task db.CloseAgentBackgroundTaskParams) {
	t.Helper()
	rows, err := queries.CloseAgentBackgroundTask(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}
