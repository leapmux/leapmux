package db_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

type optionalEnumColumn struct {
	label, insert, read string
	maximum             int
}

func optionalEnumColumns(t *testing.T) []optionalEnumColumn {
	t.Helper()
	completionMaximum := int(lastDeclaredOrdinal(t, leapmuxv1.MessageCompletion_name))
	return []optionalEnumColumn{
		{"messages.mark_type", `INSERT INTO messages (id,agent_id,seq,source,content,content_compression,mark_type) VALUES ('target','root',2,2,X'7B7D',1,?)`, `SELECT mark_type FROM messages WHERE id='target'`, 2},
		{"messages.assembled_kind", `INSERT INTO messages (id,agent_id,seq,source,content,content_compression,assembled_kind) VALUES ('target','root',2,2,X'7B7D',1,?)`, `SELECT assembled_kind FROM messages WHERE id='target'`, 3},
		{"messages.completion", `INSERT INTO messages (id,agent_id,seq,source,content,content_compression,completion) VALUES ('target','root',2,2,X'7B7D',1,?)`, `SELECT completion FROM messages WHERE id='target'`, completionMaximum},
		{"agent_input_queue_state.pause_reason", `INSERT INTO agent_input_queue_state (agent_id,pause_reason) VALUES ('root',?)`, `SELECT pause_reason FROM agent_input_queue_state WHERE agent_id='root'`, 6},
		{"agent_input_queue_state.pause_owner", `INSERT INTO agent_input_queue_state (agent_id,pause_owner) VALUES ('root',?)`, `SELECT pause_owner FROM agent_input_queue_state WHERE agent_id='root'`, 6},
	}
}

func optionalEnumStore(t *testing.T) *sql.DB {
	t.Helper()
	store, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	_, err = store.ExecContext(t.Context(), `INSERT INTO agents (id) VALUES ('root')`)
	require.NoError(t, err)
	_, err = store.ExecContext(t.Context(), `INSERT INTO messages (id,agent_id,seq,source,content,content_compression) VALUES ('parent','root',1,2,X'7B7D',1)`)
	require.NoError(t, err)
	return store
}

func insertOptionalEnum(t *testing.T, store *sql.DB, column optionalEnumColumn, value any) error {
	t.Helper()
	_, err := store.ExecContext(t.Context(), column.insert, value)
	return err
}

func TestOptionalStorageEnumsAcceptNullAbsence(t *testing.T) {
	t.Parallel()
	for _, column := range optionalEnumColumns(t) {
		t.Run(column.label, func(t *testing.T) {
			store := optionalEnumStore(t)
			require.NoError(t, insertOptionalEnum(t, store, column, nil), "SQL NULL represents an absent optional attribute")
			var value sql.NullInt64
			require.NoError(t, store.QueryRowContext(t.Context(), column.read).Scan(&value))
			assert.False(t, value.Valid)
		})
	}
}

func TestOptionalStorageEnumsRejectUnspecifiedAndNonOrdinals(t *testing.T) {
	t.Parallel()
	for _, column := range optionalEnumColumns(t) {
		for _, value := range []any{0, -1, 1.5, column.maximum + 1} {
			t.Run(fmt.Sprintf("%s/%v", column.label, value), func(t *testing.T) {
				store := optionalEnumStore(t)
				assert.ErrorContains(t, insertOptionalEnum(t, store, column, value), "CHECK constraint failed")
			})
		}
	}
}

func TestOptionalStorageEnumsRetainEveryRealOrdinal(t *testing.T) {
	t.Parallel()
	for _, column := range optionalEnumColumns(t) {
		for value := 1; value <= column.maximum; value++ {
			t.Run(fmt.Sprintf("%s/%d", column.label, value), func(t *testing.T) {
				store := optionalEnumStore(t)
				require.NoError(t, insertOptionalEnum(t, store, column, value))
				var stored int
				require.NoError(t, store.QueryRowContext(t.Context(), column.read).Scan(&stored))
				assert.Equal(t, value, stored)
			})
		}
	}
}

func TestOptionalStorageEnumsRequireARealMessageSource(t *testing.T) {
	t.Parallel()
	for _, value := range []any{nil, 0, -1, 1.5, 4} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			store := optionalEnumStore(t)
			_, err := store.ExecContext(t.Context(), `INSERT INTO messages (id,agent_id,seq,source,content,content_compression) VALUES ('target','root',2,?,X'7B7D',1)`, value)
			assert.Error(t, err)
		})
	}
}
