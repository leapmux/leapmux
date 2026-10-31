package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	mysqlstore "github.com/leapmux/leapmux/internal/hub/store/mysql"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestMySQLWorkerStateListingsReadOnlyTheRequestedPage(t *testing.T) {
	dsn := startMySQLContainer(t)
	st, err := mysqlstore.OpenTestable(config.MySQLConfig{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	database, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	assertCanonicalWorkerColumns(t, database)
	owner := storetest.SeedUser(t, st, "worker-plan-owner")
	other := storetest.SeedUser(t, st, "worker-plan-other-owner")
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, population := range []struct {
		owner, prefix string
		createdAt     time.Time
	}{
		{owner: owner.ID, createdAt: now.Add(-2 * time.Hour)},
		{owner: other.ID, prefix: "other-", createdAt: now.Add(-time.Hour)},
	} {
		seedWorkerPlanRows(t, database, population.owner, population.prefix+"active-", 20000, population.createdAt, sqltime.MySQLNullTime{})
		seedWorkerPlanRows(t, database, population.owner, population.prefix+"deregistering-", 2000, population.createdAt, sqltime.MySQLNullTime{})
		seedWorkerPlanRows(t, database, population.owner, population.prefix+"deleted-", 2000, population.createdAt, sqltime.MySQLNullTimeOf(now))
		_, err = database.ExecContext(context.Background(), `UPDATE workers SET deregistering_at = ?
WHERE id LIKE ? OR (id LIKE ? AND RIGHT(id, 1) IN ('0', '2', '4', '6', '8'))`, sqltime.NewMySQLTime(now), population.prefix+"deregistering-%", population.prefix+"deleted-%")
		require.NoError(t, err)
	}
	active := leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE
	deregistering := leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING
	deleted := leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED
	for _, state := range []struct {
		title  string
		status *leapmuxv1.WorkerStatus
		prefix string
		count  int
	}{
		{title: "all live", prefix: "deregistering-", count: 2000},
		{title: "active", status: &active, prefix: "active-", count: 20000},
		{title: "deregistering", status: &deregistering, prefix: "deregistering-", count: 2000},
		{title: "deleted", status: &deleted, prefix: "deleted-", count: 2000},
	} {
		for _, byOwner := range []bool{false, true} {
			t.Run(state.title+"/"+map[bool]string{false: "all owners", true: "one owner"}[byOwner], func(t *testing.T) {
				recorder := &workerPlanRecorder{DB: database}
				queries := gendb.New(recorder)
				params := store.ListWorkersAdminParams{Status: state.status, PageParams: store.PageParams{Limit: 50}}
				prefix, wantOwner := "other-"+state.prefix, other.ID
				if byOwner {
					params.UserID = &owner.ID
					prefix, wantOwner = state.prefix, owner.ID
				}
				seen := make(map[string]bool, 100)
				for pageNumber := range 2 {
					count, err := readWorkerPlanPage(context.Background(), queries, params)
					require.NoError(t, err)
					require.Equal(t, 51, count, "the generated query must retain its probe row")
					assertWorkerPagePlan(t, database, recorder.query, recorder.args)
					page, err := st.Workers().ListAdmin(context.Background(), params)
					require.NoError(t, err)
					require.Len(t, page.Rows, 50)
					require.NotEmpty(t, page.NextCursor)
					for i, row := range page.Rows {
						assert.Equal(t, fmt.Sprintf("%s%06d", prefix, state.count-1-pageNumber*50-i), row.ID)
						assert.Equal(t, wantOwner, row.RegisteredBy)
						if state.status != nil {
							assert.Equal(t, *state.status, row.Status)
						} else {
							assert.NotEqual(t, deleted, row.Status)
						}
						assert.False(t, seen[row.ID], "consecutive pages must not repeat a worker")
						seen[row.ID] = true
					}
					params.Cursor = page.NextCursor
				}
			})
		}
	}
	t.Run("active owner listing", func(t *testing.T) {
		recorder := &workerPlanRecorder{DB: database}
		queries := gendb.New(recorder)
		queryParams := gendb.ListWorkersByUserIDParams{RegisteredBy: owner.ID, Limit: 51}
		params := store.ListWorkersByUserIDParams{
			RegisteredBy: userid.MustNew(owner.ID), PageParams: store.PageParams{Limit: 50},
		}
		for pageNumber := range 2 {
			rows, err := queries.ListWorkersByUserID(context.Background(), queryParams)
			require.NoError(t, err)
			require.Len(t, rows, 51)
			assertWorkerPagePlan(t, database, recorder.query, recorder.args)
			page, err := st.Workers().ListByUserID(context.Background(), params)
			require.NoError(t, err)
			require.Len(t, page.Rows, 50)
			require.NotEmpty(t, page.NextCursor)
			for i, row := range page.Rows {
				assert.Equal(t, fmt.Sprintf("active-%06d", 19999-pageNumber*50-i), row.ID)
				assert.Equal(t, owner.ID, row.RegisteredBy)
				assert.Equal(t, active, row.Status)
			}
			cursor, err := store.ParseCursor(page.NextCursor)
			require.NoError(t, err)
			require.NotNil(t, cursor)
			queryParams.CursorTime = sqltime.MySQLNullTimeOf(cursor.Time)
			queryParams.CursorID = sql.NullString{String: cursor.ID, Valid: true}
			params.Cursor = page.NextCursor
		}
	})
}

// readWorkerPlanPage captures the generated query for each public filter choice.
// The store call in the test checks the corresponding public result separately.
func readWorkerPlanPage(ctx context.Context, queries *gendb.Queries, params store.ListWorkersAdminParams) (int, error) {
	global := gendb.ListWorkersAdminLiveParams{Limit: int32(store.FetchLimit(params.Limit))}
	if params.Cursor != "" {
		cursor, err := store.ParseCursor(params.Cursor)
		if err != nil {
			return 0, err
		}
		global.CursorTime = sqltime.MySQLNullTimeOf(cursor.Time)
		global.CursorID = sql.NullString{String: cursor.ID, Valid: true}
	}
	owner := gendb.ListWorkersAdminLiveByUserParams{
		Limit: global.Limit, CursorTime: global.CursorTime, CursorID: global.CursorID,
	}
	if params.UserID != nil {
		owner.UserID = *params.UserID
	}
	status := leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED
	if params.Status != nil {
		status = *params.Status
	}
	switch status {
	case leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED:
		if params.UserID != nil {
			return workerPlanRowCount(queries.ListWorkersAdminLiveByUser(ctx, owner))
		}
		return workerPlanRowCount(queries.ListWorkersAdminLive(ctx, global))
	case leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
		if params.UserID != nil {
			return workerPlanRowCount(queries.ListWorkersAdminActiveByUser(ctx, gendb.ListWorkersAdminActiveByUserParams(owner)))
		}
		return workerPlanRowCount(queries.ListWorkersAdminActive(ctx, gendb.ListWorkersAdminActiveParams(global)))
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING:
		if params.UserID != nil {
			return workerPlanRowCount(queries.ListWorkersAdminDeregisteringByUser(ctx, gendb.ListWorkersAdminDeregisteringByUserParams(owner)))
		}
		return workerPlanRowCount(queries.ListWorkersAdminDeregistering(ctx, gendb.ListWorkersAdminDeregisteringParams(global)))
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED:
		if params.UserID != nil {
			return workerPlanRowCount(queries.ListWorkersAdminDeletedByUser(ctx, gendb.ListWorkersAdminDeletedByUserParams(owner)))
		}
		return workerPlanRowCount(queries.ListWorkersAdminDeleted(ctx, gendb.ListWorkersAdminDeletedParams(global)))
	default:
		return 0, fmt.Errorf("unexpected worker status %v", status)
	}
}

func workerPlanRowCount[T any](rows []T, err error) (int, error) { return len(rows), err }

func seedWorkerPlanRows(t *testing.T, database *sql.DB, owner, prefix string, count int, createdAt time.Time, deletedAt sqltime.MySQLNullTime) {
	t.Helper()
	result, err := database.ExecContext(context.Background(), `
INSERT INTO workers (id, auth_token, registered_by, created_at, public_key, mlkem_public_key, slhdsa_public_key, deleted_at)
WITH digits AS (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
    UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9)
SELECT CONCAT(?, LPAD(numbers.n, 6, '0')), CONCAT('token-', ?, numbers.n), ?, ?, X'', X'', X'', ?
FROM (SELECT d0.n + 10*d1.n + 100*d2.n + 1000*d3.n + 10000*d4.n AS n
    FROM digits d0 CROSS JOIN digits d1 CROSS JOIN digits d2 CROSS JOIN digits d3 CROSS JOIN digits d4) numbers
WHERE numbers.n < ?`, prefix, prefix, owner, sqltime.NewMySQLTime(createdAt), deletedAt, count)
	require.NoError(t, err)
	inserted, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, count, inserted)
}

func assertWorkerPagePlan(t *testing.T, database *sql.DB, query string, args []any) {
	t.Helper()
	var plan string
	require.NoError(t, database.QueryRowContext(context.Background(), "EXPLAIN ANALYZE "+query, args...).Scan(&plan))
	actualRows := regexp.MustCompile(`actual time=[^)]*rows=([0-9.e+]+) loops=([0-9.e+]+)`).FindAllStringSubmatch(plan, -1)
	require.NotEmpty(t, actualRows, "the plan must report its measured row counts: %s", plan)
	excessiveReads := false
	for _, match := range actualRows {
		rows, err := strconv.ParseFloat(match[1], 64)
		require.NoError(t, err)
		loops, err := strconv.ParseFloat(match[2], 64)
		require.NoError(t, err)
		assert.LessOrEqual(t, rows*loops, float64(200), "a 50-row page must not examine the full worker population: %s", plan)
		excessiveReads = excessiveReads || rows*loops > 200
	}
	if excessiveReads {
		var jsonPlan string
		require.NoError(t, database.QueryRowContext(context.Background(), "EXPLAIN FORMAT=JSON "+query, args...).Scan(&jsonPlan))
		t.Logf("index selection details: %s", jsonPlan)
	}
}

type workerPlanRecorder struct {
	*sql.DB
	query string
	args  []any
}

func assertCanonicalWorkerColumns(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `SELECT COLUMN_NAME
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workers'
ORDER BY ORDINAL_POSITION`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{
		"id", "auth_token", "registered_by", "created_at", "last_seen_at",
		"deregistering_at", "public_key", "mlkem_public_key", "slhdsa_public_key",
		"auto_registered", "deleted_at",
	}, columns, "state indexes must not add visible worker columns")
}

func (r *workerPlanRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query, r.args = query, append([]any(nil), args...)
	return r.DB.QueryContext(ctx, query, args...)
}
