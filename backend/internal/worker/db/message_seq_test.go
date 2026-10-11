package db_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

// The CHECK failure that SQLite reports for messages.seq. It holds the
// expression, so the assertions below cannot pass on another CHECK of the row.
const messageSeqCheckFailure = "CHECK constraint failed: seq >= 1"

// messages.seq refuses a sequence below 1. Every writer stores 1 or more:
//   - CreateMessage and UpdateNotificationThread allocate message_seq_hwm + 1.
//   - The input queue reserves its sequence the same way.
//   - The high-water starts at 0 and only rises.
//   - The resume clone copies stored sequences.
//
// A reader treats 0 as "before the first message" or "no message". A stored 0
// or a negative sequence therefore hides its row from each cursor read, and a
// reader cannot tell a stored 0 from "no message". The CHECK moves that
// failure to the write, and this test runs the writes.
func TestMessageSeqColumnRefusesSequencesBelowOne(t *testing.T) {
	t.Parallel()

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))
	_, err = connection.ExecContext(t.Context(), `INSERT INTO agents (id) VALUES ('agent-1')`)
	require.NoError(t, err)

	insert := func(id string, seq int64) error {
		_, err := connection.ExecContext(t.Context(), `
			INSERT INTO messages (id, agent_id, seq, source, content, content_compression)
			VALUES (?, 'agent-1', ?, ?, '{}', ?)`,
			id, seq, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE)
		return err
	}

	assert.ErrorContains(t, insert("zero", 0), messageSeqCheckFailure,
		"a sequence of 0 must be refused")
	assert.ErrorContains(t, insert("negative", -1), messageSeqCheckFailure,
		"a negative sequence must be refused")
	assert.ErrorContains(t, insert("lowest", math.MinInt64), messageSeqCheckFailure,
		"the lowest integer must be refused")
	require.NoError(t, insert("first", 1), "1 is the first sequence that the Worker allocates")

	// UpdateNotificationThread moves the sequence of a stored row, so the
	// CHECK must cover an update also.
	_, err = connection.ExecContext(t.Context(), `UPDATE messages SET seq = 0 WHERE id = 'first'`)
	assert.ErrorContains(t, err, messageSeqCheckFailure, "an update to sequence 0 must be refused")

	// The refused writes stored nothing, and their insert trigger did not
	// move the high-water: only the accepted row raised it.
	var rows []string
	result, err := connection.QueryContext(t.Context(), `SELECT id || ':' || seq FROM messages ORDER BY seq`)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, result.Close()) })
	for result.Next() {
		var row string
		require.NoError(t, result.Scan(&row))
		rows = append(rows, row)
	}
	require.NoError(t, result.Err())
	assert.Equal(t, []string{"first:1"}, rows)
	var highWater int64
	require.NoError(t, connection.QueryRowContext(t.Context(),
		`SELECT message_seq_hwm FROM agents WHERE id = 'agent-1'`).Scan(&highWater))
	assert.Equal(t, int64(1), highWater)
}
