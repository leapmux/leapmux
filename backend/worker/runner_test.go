package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func setupTestDB(t *testing.T) *db.Queries {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = workerdb.Migrate(sqlDB)
	require.NoError(t, err)

	return db.New(sqlDB)
}

func TestBuildTabSync_Empty(t *testing.T) {
	queries := setupTestDB(t)
	sync := buildTabSync(queries)

	require.NotNil(t, sync)
	assert.Empty(t, sync.GetTabs())
}

func TestBuildTabSync_AgentsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Insert agents directly into the DB (simulating persisted state).
	err := queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
	})
	require.NoError(t, err)

	err = queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-2",
		WorkspaceID: "ws-2",
		WorkingDir:  "/tmp",
	})
	require.NoError(t, err)

	// Close agent-2 to verify closed agents are still included.
	err = queries.CloseAgent(ctx, "agent-2")
	require.NoError(t, err)

	sync := buildTabSync(queries)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 2)

	// Collect tabs into a map for order-independent assertion.
	tabMap := make(map[string]*leapmuxv1.WorkspaceTabEntry)
	for _, tab := range sync.GetTabs() {
		tabMap[tab.GetTabId()] = tab
	}

	agent1Tab := tabMap["agent-1"]
	require.NotNil(t, agent1Tab)
	assert.Equal(t, "ws-1", agent1Tab.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, agent1Tab.GetTabType())

	agent2Tab := tabMap["agent-2"]
	require.NotNil(t, agent2Tab)
	assert.Equal(t, "ws-2", agent2Tab.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, agent2Tab.GetTabType())
}

func TestBuildTabSync_TerminalsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	err := queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("screen data"),
	})
	require.NoError(t, err)

	sync := buildTabSync(queries)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 1)

	tab := sync.GetTabs()[0]
	assert.Equal(t, "ws-1", tab.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, tab.GetTabType())
	assert.Equal(t, "term-1", tab.GetTabId())
}

func TestBuildTabSync_MixedAgentsAndTerminals(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Add an agent and a terminal.
	err := queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
	})
	require.NoError(t, err)

	err = queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("data"),
	})
	require.NoError(t, err)

	sync := buildTabSync(queries)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 2)

	// Verify both types are present.
	types := make(map[leapmuxv1.TabType]int)
	for _, tab := range sync.GetTabs() {
		types[tab.GetTabType()]++
	}
	assert.Equal(t, 1, types[leapmuxv1.TabType_TAB_TYPE_AGENT])
	assert.Equal(t, 1, types[leapmuxv1.TabType_TAB_TYPE_TERMINAL])
}
