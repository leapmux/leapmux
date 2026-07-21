package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestAllDatetimeColumnsStoreCanonicalLayout drives every worker-DB write path
// that binds a Go-side time into a DATETIME column and asserts -- via the
// schema-discovered sqlitedb.FindNonCanonicalDatetimes walk -- that every
// stored value is the canonical 24-char strftime('%Y-%m-%dT%H:%M:%fZ') layout.
// Binds use a deliberately non-UTC fixed zone: a raw time.Time bind would
// store modernc's driver layout with the zone's offset (space at byte 11,
// '+09:00' suffix), splitting a column into two layouts whose raw-string
// compares (the closed_at/deleted_at cleanup sweeps) silently diverge.
func TestAllDatetimeColumnsStoreCanonicalLayout(t *testing.T) {
	sqlDB, queries := setupTestDB(t)
	ctx := context.Background()

	// Non-UTC on purpose; see the doc comment.
	zone := time.FixedZone("UTC+9", 9*60*60)
	now := time.Now().In(zone)

	// agents: created_at DEFAULT + closed_at via CloseAgent's strftime.
	require.NoError(t, queries.CreateAgent(ctx, gendb.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    "/tmp",
		HomeDir:       "/home",
		Title:         "agent",
		Options:       "{}",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, queries.CloseAgent(ctx, "agent-1"))

	// messages.created_at is Go-bound on every persisted chat message.
	_, err := queries.CreateMessage(ctx, gendb.CreateMessageParams{
		ID:                 "msg-1",
		AgentID:            "agent-1",
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:            []byte("hello"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SpanLines:          "[]",
		AgentProvider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:          now,
	})
	require.NoError(t, err)

	// terminals: UpsertTerminal binds closed_at directly -- the title-update
	// path re-binds a roundtripped non-NULL value, so exercise that shape on
	// term-1 and leave it untouched (a subsequent CloseTerminal would
	// overwrite the bound value and hide the layout it stored). term-2 covers
	// the CloseTerminal strftime path.
	require.NoError(t, queries.UpsertTerminal(ctx, gendb.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/home",
		Shell:       "/bin/zsh",
		Title:       "terminal",
		Cols:        80,
		Rows:        24,
		Screen:      []byte{},
		ClosedAt:    sql.NullTime{Time: now, Valid: true},
	}))
	require.NoError(t, queries.UpsertTerminal(ctx, gendb.UpsertTerminalParams{
		ID:          "term-2",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/home",
		Shell:       "/bin/zsh",
		Title:       "terminal-2",
		Cols:        80,
		Rows:        24,
		Screen:      []byte{},
	}))
	require.NoError(t, queries.CloseTerminal(ctx, "term-2"))

	// worktrees: deleted_at via DeleteWorktree's strftime.
	require.NoError(t, queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           "wt-1",
		WorktreePath: "/tmp/wt1",
		RepoRoot:     "/repo",
		BranchName:   "main",
	}))
	require.NoError(t, queries.DeleteWorktree(ctx, "wt-1"))

	// auto_continue_schedules.due_at is Go-bound on every upsert.
	require.NoError(t, queries.UpsertAutoContinueSchedule(ctx, gendb.UpsertAutoContinueScheduleParams{
		AgentID:       "agent-1",
		Reason:        "rate_limit",
		Content:       "continue",
		DueAt:         now.Add(time.Hour),
		SourcePayload: []byte("{}"),
	}))

	// agent_todos.updated_at via UpsertAgentTodo's strftime.
	require.NoError(t, queries.UpsertAgentTodo(ctx, gendb.UpsertAgentTodoParams{
		AgentID: "agent-1",
		RowKey:  "todo-1",
		Seq:     1,
		TaskID:  "task-1",
		Content: "do the thing",
		Status:  "pending",
	}))

	offenders, walked, err := sqlitedb.FindNonCanonicalDatetimes(ctx, sqlDB, "goose_db_version")
	require.NoError(t, err)
	require.Positive(t, walked, "walk discovered no DATETIME columns; the discovery query is broken")
	assert.Empty(t, offenders,
		"non-canonical timestamp value(s) on disk -- a write path is missing its strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', ...) wrap:\n  %s",
		strings.Join(offenders, "\n  "))
}
