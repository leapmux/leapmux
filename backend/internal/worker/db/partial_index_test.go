package db_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
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

// inequalityIndexPredicate matches one `column <> ”` or `column <> 0` term of a
// partial index predicate.
//
// That family is the whole subject of the sweep below. SQLite PROVES an equality
// term implies `IS NOT NULL`, so an index predicated on that rides without the term
// spelled; it can make no such inference for `<>`, so every query that wants such an
// index must repeat the term itself.
var inequalityIndexPredicate = regexp.MustCompile(`(\w+)\s*<>\s*(''|0)`)

// sqlcStatementMarker matches the `-- name: X :one` line that opens each statement
// of a sqlc query file.
//
// The MARKER alone, with the body sliced between consecutive markers. A single
// pattern that also captured the body had to consume the NEXT marker as its
// terminator, and Go's regexp has no lookahead -- so FindAll resumed past that
// marker and silently skipped every other statement. The sweep still passed, with
// half its subject matter missing.
var sqlcStatementMarker = regexp.MustCompile(`(?m)^--\s*name:\s*(\w+)\s*:\w+`)

// sqlWhereKeyword matches the WHERE keyword on ANY whitespace, not a space alone.
// Two of these indexes put WHERE at the start of its own line, where a
// space-delimited match finds nothing and the index silently leaves the scan.
var sqlWhereKeyword = regexp.MustCompile(`(?i)\sWHERE\s`)

// TestEveryQueryRepeatsThePartialIndexPredicateItNeeds refuses a query that filters
// an inequality-family partial index's column against a parameter and omits the
// index's own term.
//
// This is the RULE the case list above states but cannot enforce. That list is keyed
// by INDEX, so one pinned query marks its index covered and the NEXT query on the
// same column rides no index at all -- a silent whole-table scan, which is how two
// of these drifted before. This is keyed by QUERY, and it is derived from the sqlc
// files rather than hand-written, so a new query inherits the rule.
//
// It is ADDITIVE, not a replacement. A text sweep proves only that the term is
// PRESENT; the EXPLAIN assertions above prove the planner really seeks the index.
//
// Three limits, each deliberate:
//   - It reads the WHERE clause alone. `child_agent_id` also appears in an INSERT
//     column list, an ON CONFLICT assignment and two RETURNING clauses, and a bare
//     "the statement mentions the column" test reports all four.
//   - A query that filters the column against a LITERAL is exempt. `child_agent_id
//     = ”` deliberately selects the rows the index excludes.
//   - It reaches sqlc statements only. A raw SQL string in Go (inputqueue/store.go
//     builds several) is outside it, and the EXPLAIN cases above are what cover
//     those.
func TestEveryQueryRepeatsThePartialIndexPredicateItNeeds(t *testing.T) {
	t.Parallel()

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))

	// Every inequality term the schema declares, by the table it indexes.
	type indexTerm struct{ index, column, predicate string }
	terms := map[string][]indexTerm{}
	rows, err := connection.QueryContext(t.Context(),
		`SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`)
	require.NoError(t, err)
	for rows.Next() {
		var name, table, create string
		require.NoError(t, rows.Scan(&name, &table, &create))
		where := sqlWhereKeyword.FindStringIndex(create)
		if where == nil {
			continue
		}
		for _, match := range inequalityIndexPredicate.FindAllStringSubmatch(create[where[0]:], -1) {
			terms[table] = append(terms[table], indexTerm{index: name, column: match[1], predicate: match[0]})
		}
	}
	require.NoError(t, rows.Close())
	require.Len(t, terms, 5, "the schema declares inequality-family partial indexes on five tables")

	files, err := filepath.Glob(filepath.Join("queries", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "the worker declares sqlc queries; a scan that finds none is broken")

	checked := 0
	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		text := string(body)
		markers := sqlcStatementMarker.FindAllStringSubmatchIndex(text, -1)
		for i, marker := range markers {
			end := len(text)
			if i+1 < len(markers) {
				end = markers[i+1][0]
			}
			name, statement := text[marker[2]:marker[3]], text[marker[1]:end]
			where := whereClauseOf(statement)
			if where == "" {
				continue
			}
			for table, indexTerms := range terms {
				if !queryReadsTable(statement, table) {
					continue
				}
				for _, term := range indexTerms {
					if !filtersColumnByParameter(where, term.column) {
						continue
					}
					checked++
					assert.Containsf(t, squashSpaces(where), term.predicate,
						"%s (%s) filters %s.%s against a parameter, so it wants %s -- "+
							"but it omits that index's own %q term, and SQLite matches a partial "+
							"index SYNTACTICALLY: without the term the query reads the WHOLE table, "+
							"with no error and no warning",
						name, filepath.Base(file), table, term.column, term.index, term.predicate)
				}
			}
		}
	}
	require.Positive(t, checked, "the sweep found no query to check; the scan is broken")
}

// whereClauseOf returns the statement text from its first WHERE to the end, minus a
// trailing RETURNING clause. It answers an empty string for a statement with no WHERE.
func whereClauseOf(statement string) string {
	start := sqlWhereKeyword.FindStringIndex(statement)
	if start == nil {
		return ""
	}
	clause := statement[start[0]:]
	if end := sqlReturningKeyword.FindStringIndex(clause); end != nil {
		clause = clause[:end[0]]
	}
	return clause
}

var sqlReturningKeyword = regexp.MustCompile(`(?i)\sRETURNING\s`)

// queryReadsTable reports whether the statement names the table after FROM, JOIN,
// UPDATE or DELETE FROM -- the four positions that make the table's indexes relevant.
func queryReadsTable(statement, table string) bool {
	for _, keyword := range []string{"FROM ", "JOIN ", "UPDATE ", "INTO "} {
		if regexp.MustCompile(`(?i)\b` + keyword + table + `\b`).MatchString(statement) {
			return true
		}
	}
	return false
}

// filtersColumnByParameter reports whether the WHERE clause compares the column to a
// bound parameter. A comparison against a LITERAL is deliberately excluded: a query
// that asks for `child_agent_id = ”` selects the rows the index excludes.
func filtersColumnByParameter(where, column string) bool {
	return regexp.MustCompile(`(?i)\b` + column + `\s*=\s*(\?|sqlc\.(arg|narg)\()`).MatchString(where)
}

func squashSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }
