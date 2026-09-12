package db_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	queries "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Capture the generated query so the test checks the SQL that callers execute.
type messageQueryRecorder struct {
	*sql.DB
	query string
	args  []any
}

func (r *messageQueryRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query, r.args = query, args
	return r.DB.QueryContext(ctx, query, args...)
}

func (r *messageQueryRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.query, r.args = query, args
	return r.DB.QueryRowContext(ctx, query, args...)
}

func TestToolMessageLookupsUseTheSpanIndex(t *testing.T) {
	t.Parallel()
	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))
	recorder := &messageQueryRecorder{DB: connection}
	q := queries.New(recorder)
	for _, run := range []func(){
		func() {
			_, err := q.ListMessagesByAgentAndSpan(t.Context(), queries.ListMessagesByAgentAndSpanParams{AgentID: "agent", SpanID: "call"})
			require.NoError(t, err)
		},
		func() {
			_, err := q.GetLatestMessageByAgentSpanAndSource(t.Context(), queries.GetLatestMessageByAgentSpanAndSourceParams{AgentID: "agent", SpanID: "call", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT})
			require.ErrorIs(t, err, sql.ErrNoRows)
		},
	} {
		run()
		rows, err := connection.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+recorder.query, recorder.args...)
		require.NoError(t, err)
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			details = append(details, detail)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		assert.Contains(t, strings.Join(details, "\n"), "idx_messages_span_id", recorder.query)
	}
}
