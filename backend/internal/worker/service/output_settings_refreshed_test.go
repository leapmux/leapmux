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

type refreshTestFixture struct {
	svc  *Context
	sink agent.OutputSink
	mock *mockResponseWriter
}

func newRefreshTestFixture(t *testing.T, seed db.UpdateAgentAllSettingsParams) refreshTestFixture {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Model:         seed.Model,
		Effort:        seed.Effort,
	}))
	seed.ID = "agent-1"
	require.NoError(t, svc.Queries.UpdateAgentAllSettings(ctx, seed))

	w, mock := newTestWatcher("ch-1")
	svc.Watchers.WatchAgent("agent-1", w)

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	return refreshTestFixture{svc: svc, sink: sink, mock: mock}
}

// TestPersistSettingsRefresh_SkipsWhenUnchanged verifies that a refresh
// matching the persisted row does no DB write and fires no StatusChange
// event. Refresh runs at startup and after every UpdateSettings, and the
// common case is that the CLI confirms values we already stored — skipping
// the no-op path avoids redundant DB churn and avoids waking every
// connected frontend to re-render identical settings.
func TestPersistSettingsRefresh_SkipsWhenUnchanged(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		Effort:         "high",
		PermissionMode: "default",
		ExtraSettings:  `{"output_style":"default"}`,
	})

	f.sink.PersistSettingsRefresh("opus", "high", "default", map[string]string{"output_style": "default"})

	assert.Equal(t, int64(0), f.mock.streamCount.Load(),
		"a refresh that matches the DB should not broadcast")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "opus", dbAgent.Model, "DB untouched on no-op refresh")
	assert.Equal(t, "high", dbAgent.Effort)
	assert.Equal(t, "default", dbAgent.PermissionMode)
}

// TestPersistSettingsRefresh_WritesAndBroadcastsOnChange verifies the
// positive path: when at least one field changes, the row is rewritten and
// a StatusChange event reaches watchers.
func TestPersistSettingsRefresh_WritesAndBroadcastsOnChange(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		Effort:         "auto",
		PermissionMode: "default",
	})

	// CLI resolves "auto" → "high" and reports it back.
	f.sink.PersistSettingsRefresh("opus", "high", "default", nil)

	assert.Equal(t, int64(1), f.mock.streamCount.Load(),
		"a refresh that changes effort should broadcast exactly once")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", dbAgent.Effort, "effort should be persisted")
}
