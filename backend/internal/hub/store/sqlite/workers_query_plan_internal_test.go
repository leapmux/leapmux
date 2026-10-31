package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func TestWorkerAdminStateQueriesUseSelectiveOrderedIndexes(t *testing.T) {
	st, database := newSessionTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-plan-owner")
	for range 64 {
		storetest.SeedWorker(t, st, owner.ID)
		pending := storetest.SeedWorker(t, st, owner.ID)
		_, err := st.Workers().ForceDeregister(context.Background(), pending.ID)
		require.NoError(t, err)
		deleted := storetest.SeedWorker(t, st, owner.ID)
		require.NoError(t, st.Workers().MarkDeleted(context.Background(), deleted.ID))
	}
	active := leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE
	deregistering := leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING
	deleted := leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED
	for _, state := range []struct {
		name   string
		status *leapmuxv1.WorkerStatus
		index  string
	}{
		{"all live", nil, "created_at"},
		{"active", &active, "active_created"},
		{"deregistering", &deregistering, "deregistering_created"},
		{"deleted", &deleted, "deleted_created"},
	} {
		for _, ownerFiltered := range []bool{false, true} {
			name := state.name
			if ownerFiltered {
				name += " by owner"
			}
			t.Run(name, func(t *testing.T) {
				recorder := &workerQueryRecorder{DB: database}
				connection := *st.conn
				connection.q = gendb.New(recorder)
				workers := &workerStore{conn: &connection}
				params := store.ListWorkersAdminParams{Status: state.status, PageParams: store.PageParams{Limit: 50}}
				index := "idx_workers_" + state.index
				if ownerFiltered {
					params.UserID = &owner.ID
					index = "idx_workers_registered_by_" + state.index
					if state.status == nil {
						index = "idx_workers_registered_by_created"
					}
				}
				page, err := workers.ListAdmin(context.Background(), params)
				require.NoError(t, err)
				require.Len(t, page.Rows, 50)
				assert.NotEmpty(t, page.NextCursor)
				require.NotEmpty(t, recorder.query)
				plan := strings.Join(explainPlan(t, database, recorder.query, recorder.args...), "\n")
				assert.Contains(t, plan, index, "a state-only page must not scan another state's population")
				// The outer join may reorder the selected page, but must never
				// expand it. Check the raw result before NewPage trims its probe.
				rows, err := database.QueryContext(context.Background(), recorder.query, recorder.args...)
				require.NoError(t, err)
				count := 0
				for rows.Next() {
					count++
				}
				require.NoError(t, rows.Err())
				require.NoError(t, rows.Close())
				assert.Equal(t, 51, count)
			})
		}
	}
}

type workerQueryRecorder struct {
	*sql.DB
	query string
	args  []any
}

func (r *workerQueryRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query, r.args = query, append([]any(nil), args...)
	return r.DB.QueryContext(ctx, query, args...)
}
