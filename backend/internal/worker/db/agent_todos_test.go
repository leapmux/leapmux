package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/db"
)

// TestAgentTodosTaskIDIsUniquePerAgentWhenSet pins the two keys of
// agent_todos. A row is keyed by (agent_id, seq), and a provider task id identifies
// at most one row per agent; a snapshot row without an id carries an empty
// task id, which may repeat.
func TestAgentTodosTaskIDIsUniquePerAgentWhenSet(t *testing.T) {
	t.Parallel()

	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, sqlDB))
	for _, agentID := range []string{"a1", "a2"} {
		_, err := sqlDB.ExecContext(ctx, `INSERT INTO agents (id) VALUES (?)`, agentID)
		require.NoError(t, err)
	}
	insert := func(agentID string, seq int, taskID string) error {
		_, err := sqlDB.ExecContext(ctx,
			`INSERT INTO agent_todos (agent_id, seq, task_id, content, status) VALUES (?, ?, ?, 'todo', 1)`,
			agentID, seq, taskID)
		return err
	}

	require.NoError(t, insert("a1", 1, "t1"))
	assert.ErrorContains(t, insert("a1", 2, "t1"), "UNIQUE constraint failed: agent_todos.agent_id, agent_todos.task_id",
		"one agent holds a task id once")
	assert.NoError(t, insert("a2", 1, "t1"), "another agent may hold the same task id")
	assert.NoError(t, insert("a1", 3, ""), "a row without a task id is allowed")
	assert.NoError(t, insert("a1", 4, ""), "rows without a task id may repeat")
	assert.ErrorContains(t, insert("a1", 1, "t9"), "UNIQUE constraint failed: agent_todos.agent_id, agent_todos.seq",
		"one agent holds a seq once")
}
