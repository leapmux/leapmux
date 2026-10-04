package db_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentTurnEndClaimsKeepExactAgentSessionAndKeyScopes(t *testing.T) {
	t.Parallel()
	store, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	for _, id := range []string{"agent-a", "agent-b"} {
		_, err := store.ExecContext(t.Context(), "INSERT INTO agents (id) VALUES (?)", id)
		require.NoError(t, err)
	}
	queries := db.New(store)
	for _, claim := range []db.ClaimAgentTurnEndParams{
		{AgentID: "agent-a", AgentSessionID: "session-a", IdempotencyKey: "turn:0"},
		{AgentID: "agent-a", AgentSessionID: "session-b", IdempotencyKey: "turn:0"},
		{AgentID: "agent-b", AgentSessionID: "session-a", IdempotencyKey: "turn:0"},
		{AgentID: "agent-a", AgentSessionID: "session-a", IdempotencyKey: "turn:1"},
		{AgentID: "agent-a", IdempotencyKey: "turn:0"},
		{AgentID: "agent-a", AgentSessionID: "한😀", IdempotencyKey: "turn:한😀"},
		{AgentID: "agent-a", AgentSessionID: "native", IdempotencyKey: strings.Repeat("한😀", 10_000)},
	} {
		lookup := db.HasAgentTurnEndParams(claim)
		stored, err := queries.HasAgentTurnEnd(t.Context(), lookup)
		require.NoError(t, err)
		assert.False(t, stored)
		rows, err := queries.ClaimAgentTurnEnd(t.Context(), claim)
		require.NoError(t, err)
		assert.Equal(t, int64(1), rows)
		rows, err = queries.ClaimAgentTurnEnd(t.Context(), claim)
		require.NoError(t, err)
		assert.Zero(t, rows)
		stored, err = queries.HasAgentTurnEnd(t.Context(), lookup)
		require.NoError(t, err)
		assert.True(t, stored)
	}
	for _, claim := range []db.ClaimAgentTurnEndParams{
		{AgentID: "agent-a", AgentSessionID: "session-a"},
		{AgentID: "missing-agent", IdempotencyKey: "turn:0"},
	} {
		_, err := queries.ClaimAgentTurnEnd(t.Context(), claim)
		require.Error(t, err)
	}
	_, err = store.ExecContext(t.Context(), "INSERT INTO agent_turn_ends (agent_id, agent_session_id, idempotency_key) VALUES (?, ?, NULL)", "agent-a", "native")
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = queries.ClaimAgentTurnEnd(ctx, db.ClaimAgentTurnEndParams{AgentID: "agent-a", IdempotencyKey: "cancelled"})
	require.ErrorIs(t, err, context.Canceled)
	_, err = queries.HasAgentTurnEnd(ctx, db.HasAgentTurnEndParams{AgentID: "agent-a", IdempotencyKey: "cancelled"})
	require.ErrorIs(t, err, context.Canceled)
	_, err = store.ExecContext(t.Context(), "DELETE FROM agents WHERE id = ?", "agent-a")
	require.NoError(t, err)
	var count int
	require.NoError(t, store.QueryRowContext(t.Context(), "SELECT count(*) FROM agent_turn_ends WHERE agent_id = ?", "agent-a").Scan(&count))
	assert.Zero(t, count)
	require.NoError(t, store.QueryRowContext(t.Context(), "SELECT count(*) FROM agent_turn_ends WHERE agent_id = ?", "agent-b").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestConcurrentAgentTurnEndClaimsChooseOneWriter(t *testing.T) {
	t.Parallel()
	store, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	_, err = store.ExecContext(t.Context(), "INSERT INTO agents (id) VALUES (?)", "agent")
	require.NoError(t, err)
	queries := db.New(store)
	type outcome struct {
		rows int64
		err  error
	}
	results := make(chan outcome, 16)
	var group sync.WaitGroup
	for range cap(results) {
		group.Go(func() {
			rows, err := queries.ClaimAgentTurnEnd(context.Background(), db.ClaimAgentTurnEndParams{AgentID: "agent", AgentSessionID: "native", IdempotencyKey: "turn:1"})
			results <- outcome{rows: rows, err: err}
		})
	}
	group.Wait()
	close(results)
	var writers int64
	for result := range results {
		require.NoError(t, result.err)
		writers += result.rows
	}
	assert.Equal(t, int64(1), writers)
}
