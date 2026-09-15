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

// queryRecorder captures the statement a generated method runs, so the plan
// assertions below read the SQL a caller executes rather than a copy of it.
type queryRecorder struct {
	*sql.DB
	query string
	args  []any
}

func (r *queryRecorder) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	r.query, r.args = query, args
	return r.DB.ExecContext(ctx, query, args...)
}

func (r *queryRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query, r.args = query, args
	return r.DB.QueryContext(ctx, query, args...)
}

func (r *queryRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.query, r.args = query, args
	return r.DB.QueryRowContext(ctx, query, args...)
}

// Every query that reads a table through a PARTIAL index must repeat that
// index's predicate.
//
// SQLite matches a partial index SYNTACTICALLY: it uses the index only where
// the query carries a term that implies the index predicate. A query that omits
// the term gets no error and no warning, only a plan that reads the whole
// table. Two of these already drifted that way.
//
// The predicate is also a filter in its own right. An empty span_id, input_id
// or child_agent_id would otherwise match every row that shares the remaining
// terms, which for the two writes below is a row the caller never addressed.
func TestQueriesRepeatTheirPartialIndexPredicate(t *testing.T) {
	t.Parallel()

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))
	recorder := &queryRecorder{DB: connection}
	q := queries.New(recorder)

	for _, c := range []struct {
		name  string
		index string
		run   func(t *testing.T)
	}{
		{
			name:  "ListMessagesByAgentAndSpan",
			index: "idx_messages_span_id",
			run: func(t *testing.T) {
				_, err := q.ListMessagesByAgentAndSpan(t.Context(),
					queries.ListMessagesByAgentAndSpanParams{AgentID: "agent", SpanID: "call"})
				require.NoError(t, err)
			},
		},
		{
			name:  "GetLatestMessageByAgentSpanAndSource",
			index: "idx_messages_span_id",
			run: func(t *testing.T) {
				_, err := q.GetLatestMessageByAgentSpanAndSource(t.Context(),
					queries.GetLatestMessageByAgentSpanAndSourceParams{
						AgentID: "agent", SpanID: "call",
						Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					})
				require.ErrorIs(t, err, sql.ErrNoRows)
			},
		},
		{
			name:  "GetAgentMessageBySpanIDAndSource",
			index: "idx_messages_span_id",
			run: func(t *testing.T) {
				_, err := q.GetAgentMessageBySpanIDAndSource(t.Context(),
					queries.GetAgentMessageBySpanIDAndSourceParams{
						AgentID: "agent", SpanID: "call",
						Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					})
				require.ErrorIs(t, err, sql.ErrNoRows)
			},
		},
		{
			name:  "ListMessageMarksByAgentID",
			index: "idx_messages_mark_type",
			run: func(t *testing.T) {
				_, err := q.ListMessageMarksByAgentID(t.Context(), "agent")
				require.NoError(t, err)
			},
		},
		{
			name:  "GetControlResponseSourcesForInput",
			index: "idx_control_response_input",
			run: func(t *testing.T) {
				_, err := q.GetControlResponseSourcesForInput(t.Context(),
					queries.GetControlResponseSourcesForInputParams{AgentID: "agent", InputID: "input"})
				require.NoError(t, err)
			},
		},
		{
			name:  "RecordControlResponseExecutionSession",
			index: "idx_control_response_input",
			run: func(t *testing.T) {
				_, err := q.RecordControlResponseExecutionSession(t.Context(),
					queries.RecordControlResponseExecutionSessionParams{AgentID: "agent", InputID: "input"})
				require.NoError(t, err)
			},
		},
		{
			name:  "GetAgentBackgroundTaskByChildAgentID",
			index: "idx_agent_background_tasks_child",
			run: func(t *testing.T) {
				_, err := q.GetAgentBackgroundTaskByChildAgentID(t.Context(), "child")
				require.ErrorIs(t, err, sql.ErrNoRows)
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.run(t)
			plan := explainPlan(t, connection, recorder.query, recorder.args)
			assert.Containsf(t, plan, c.index,
				"%s must seek %s; plan:\n%s\nquery:\n%s", c.name, c.index, plan, recorder.query)
		})
	}
}

// explainPlan returns the detail line of every step SQLite plans for one
// statement, joined with newlines.
func explainPlan(t *testing.T, connection *sql.DB, query string, args []any) string {
	t.Helper()

	rows, err := connection.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
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
	return strings.Join(details, "\n")
}
