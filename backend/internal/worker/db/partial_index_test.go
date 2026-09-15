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
	covered := map[string]struct{}{}

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
		{
			name:  "GetChildAgentBySpawnSpan",
			index: "idx_agents_spawn_span",
			run: func(t *testing.T) {
				_, err := q.GetChildAgentBySpawnSpan(t.Context(),
					queries.GetChildAgentBySpawnSpanParams{ParentAgentID: sql.NullString{String: "parent", Valid: true}, SpawnSpanID: "call"})
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
		covered[c.index] = struct{}{}
	}

	// The case list above is hand-written, so it pins the queries somebody already
	// fixed rather than the RULE. This reads every partial index the schema declares
	// and refuses one that no case covers, which is what makes the next index a test
	// failure instead of a silent table scan.
	//
	// An index whose column no query filters on has nothing to pin, so it states that
	// here rather than growing an empty case.
	unpinned := map[string]string{
		// SQLite PROVES parent_agent_id = ? implies IS NOT NULL, so a query rides
		// this index without repeating the term. That inference is exactly what it
		// cannot make for `<> ''`, which is why idx_agents_spawn_span needs its
		// term spelled and this one does not.
		"idx_agents_parent":               "an IS NOT NULL predicate, which an equality term already implies",
		"idx_agents_closed_at":            "no query filters on closed_at through this index",
		"idx_agents_open_working_dir":     "no query filters on working_dir through this index",
		"idx_agent_input_queue_one_edit":  "a uniqueness constraint, enforced on write rather than read",
		"idx_worktrees_path":              "a uniqueness constraint, enforced on write rather than read",
		"idx_worktrees_deleted_at":        "no query filters on deleted_at through this index",
		"idx_terminals_closed_at":         "no query filters on closed_at through this index",
		"idx_terminals_open_working_dir":  "no query filters on working_dir through this index",
		"idx_terminals_quake_working_dir": "a uniqueness constraint, enforced on write rather than read",
	}
	for _, index := range partialIndexes(t, connection) {
		if _, ok := covered[index]; ok {
			continue
		}
		if _, ok := unpinned[index]; ok {
			continue
		}
		t.Errorf("partial index %s has no case here: add one that asserts a query seeks it, "+
			"or state in `unpinned` why no query rides it", index)
	}
}

// partialIndexes reports every partial index the live schema declares, which is
// what keeps the case list above from stopping at the indexes it was written for.
func partialIndexes(t *testing.T, connection *sql.DB) []string {
	t.Helper()

	rows, err := connection.QueryContext(t.Context(),
		"SELECT name FROM sqlite_master WHERE type = 'index' AND sql LIKE '%WHERE%' ORDER BY name")
	require.NoError(t, err)
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.NotEmpty(t, names, "the schema declares partial indexes; a query that finds none is broken")
	return names
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
