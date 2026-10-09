package db_test

import (
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func newNotificationEntryStore(t *testing.T) *sql.DB {
	t.Helper()
	store, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	for _, agentID := range []string{"root", "child"} {
		_, err = store.ExecContext(t.Context(), "INSERT INTO agents (id) VALUES (?)", agentID)
		require.NoError(t, err)
	}
	for index, row := range []struct{ id, agentID, sessionID string }{
		{"root-a", "root", "a"}, {"root-b", "root", "b"}, {"child-a", "child", "a"}, {"root-empty", "root", ""},
	} {
		_, err = store.ExecContext(t.Context(), `INSERT INTO messages
			(id, agent_id, agent_session_id, seq, source, content, content_compression)
			VALUES (?, ?, ?, ?, ?, X'7B7D', ?)`, row.id, row.agentID, row.sessionID, index+1,
			leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE)
		require.NoError(t, err)
	}
	return store
}

func TestNotificationEntryKeysKeepExactAgentAndSessionIdentity(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	original := []byte("  {\"text\":\"한😀\"}\n")
	identity, err := agent.NewNotificationEntry(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{IdempotencyKey: "native:0", Original: original})
	require.NoError(t, err)
	queries := gendb.New(store)
	for _, owner := range []struct{ id, agentID, sessionID string }{
		{"root-a", "root", "a"}, {"root-b", "root", "b"}, {"child-a", "child", "a"}, {"root-empty", "root", ""},
	} {
		encoded, err := agent.AppendNotificationJournal(nil, agent.MessageContent{}, identity)
		require.NoError(t, err)
		_, err = agent.AppendNotificationJournal(encoded, agent.MessageContent{}, identity)
		assert.ErrorContains(t, err, "repeats key")
		for range 2 {
			encoded, err = agent.AppendNotificationJournal(encoded, agent.MessageContent{}, agent.NotificationEntry{})
			require.NoError(t, err)
		}
		count, err := queries.UpdateNotificationJournal(t.Context(), gendb.UpdateNotificationJournalParams{
			ID: owner.id, AgentID: owner.agentID, SupplementalContent: encoded,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
		rows, err := queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{
			AgentID: owner.agentID, AgentSessionID: owner.sessionID,
		})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, owner.id, rows[0].ID)
		assert.Equal(t, owner.agentID, rows[0].AgentID)
		assert.Equal(t, owner.sessionID, rows[0].AgentSessionID)
		entries, err := agent.DecodeNotificationJournal(rows[0].SupplementalContent)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "native:0", entries[0].IdempotencyKey)
		assert.Equal(t, "c3e1e3210fb97d16bc7fdf806492e720fd300931caf4c26ee86bcea865ba49d9", hex.EncodeToString(entries[0].Fingerprint[:]))
	}
	var count int
	require.NoError(t, store.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name = 'notification_entries'`).Scan(&count))
	assert.Zero(t, count)
}

func TestNotificationEntryParentOwnershipAndDeletion(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	queries := gendb.New(store)
	identity, err := agent.NewNotificationEntry(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{IdempotencyKey: "native-key", Original: []byte(`{}`)})
	require.NoError(t, err)
	encoded, err := agent.AppendNotificationJournal(nil, agent.MessageContent{}, identity)
	require.NoError(t, err)
	for _, owner := range []struct{ messageID, agentID string }{
		{"root-a", "child"}, {"missing", "root"},
	} {
		count, err := queries.UpdateNotificationJournal(t.Context(), gendb.UpdateNotificationJournalParams{
			ID: owner.messageID, AgentID: owner.agentID, SupplementalContent: encoded,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
		require.NoError(t, err)
		assert.Zero(t, count)
	}
	for _, owner := range []struct{ messageID, agentID string }{{"root-a", "root"}, {"child-a", "child"}} {
		count, err := queries.UpdateNotificationJournal(t.Context(), gendb.UpdateNotificationJournalParams{
			ID: owner.messageID, AgentID: owner.agentID, SupplementalContent: encoded,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	}
	wrongSession, err := queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{AgentID: "root", AgentSessionID: "not-a"})
	require.NoError(t, err)
	assert.Empty(t, wrongSession)
	_, err = queries.DeleteMessageByAgentAndID(t.Context(), gendb.DeleteMessageByAgentAndIDParams{ID: "root-a", AgentID: "root"})
	require.NoError(t, err)
	rows, err := queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{AgentID: "root", AgentSessionID: "a"})
	require.NoError(t, err)
	assert.Empty(t, rows)
	_, err = store.ExecContext(t.Context(), "DELETE FROM agents WHERE id = ?", "child")
	require.NoError(t, err)
	rows, err = queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{AgentID: "child", AgentSessionID: "a"})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestNotificationAggregateCompletionRejectsUnknownOrdinals(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	for ordinal := int32(1); ordinal <= lastDeclaredOrdinal(t, leapmuxv1.MessageCompletion_name); ordinal++ {
		_, err := store.ExecContext(t.Context(), "UPDATE messages SET completion = ? WHERE id = ?", ordinal, "root-a")
		require.NoError(t, err)
		var stored sql.NullInt64
		require.NoError(t, store.QueryRowContext(t.Context(), "SELECT completion FROM messages WHERE id = ?", "root-a").Scan(&stored))
		assert.True(t, stored.Valid)
		assert.Equal(t, int64(ordinal), stored.Int64)
	}
	for _, ordinal := range []any{0, -1, 1.5, lastDeclaredOrdinal(t, leapmuxv1.MessageCompletion_name) + 1, int64(9223372036854775807), "complete", false, 2147483647} {
		_, err := store.ExecContext(t.Context(), "UPDATE messages SET completion = ? WHERE id = ?", ordinal, "root-a")
		assert.Error(t, err, "%v", ordinal)
	}
	_, err := store.ExecContext(t.Context(), "UPDATE messages SET completion = NULL WHERE id = ?", "root-a")
	require.NoError(t, err)
	var absent sql.NullInt64
	require.NoError(t, store.QueryRowContext(t.Context(), "SELECT completion FROM messages WHERE id = ?", "root-a").Scan(&absent))
	assert.False(t, absent.Valid)
}
