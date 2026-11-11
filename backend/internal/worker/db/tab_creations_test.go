package db_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	queries "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTabCreationQueries(t *testing.T) {
	t.Parallel()
	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))
	recorder := &queryRecorder{DB: connection}
	q := queries.New(recorder)
	list := func(owner, worker string) ([]queries.TabCreation, error) {
		return q.ListPendingTabCreations(t.Context(), queries.ListPendingTabCreationsParams{
			UserID: owner, WorkerID: worker,
			PendingState:         int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_PENDING),
			CancelRequestedState: int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCEL_REQUESTED),
			PublishedState:       int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_PUBLISHED),
			CanceledState:        int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCELED),
		})
	}
	empty, err := list("owner", "worker")
	require.NoError(t, err)
	assert.Empty(t, empty)
	states := []struct {
		id    string
		state leapmuxv1.TabCreationState
	}{
		{"f-published", leapmuxv1.TabCreationState_TAB_CREATION_STATE_PUBLISHED},
		{"e-canceled", leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCELED},
		{"d-cancel-requested", leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCEL_REQUESTED},
		{"c-rejected", leapmuxv1.TabCreationState_TAB_CREATION_STATE_REJECTED},
		{"b-committed", leapmuxv1.TabCreationState_TAB_CREATION_STATE_COMMITTED},
		{"a-pending", leapmuxv1.TabCreationState_TAB_CREATION_STATE_PENDING},
	}
	checkpoints := []struct {
		id        string
		payload   any
		timestamp string
	}{
		{"z-null", nil, "2026-01-01T00:00:00Z"},
		{"y-empty", []byte{}, "2026-01-01T00:00:00Z"},
		{"x-data", []byte("{}"), "2026-01-01T00:00:01Z"},
	}
	for _, owner := range []string{"owner", "other-owner"} {
		for _, worker := range []string{"worker", "other-worker"} {
			for _, state := range states {
				for _, checkpoint := range checkpoints {
					id := state.id + "-" + checkpoint.id
					require.NoError(t, q.InsertTabCreation(t.Context(), queries.InsertTabCreationParams{
						UserID: owner, WorkerID: worker, TabID: id, TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE),
						RequestHash: bytes.Repeat([]byte{1}, 32), RequestPayload: []byte(owner + "|" + worker),
						PublicationPayload: []byte{}, RuntimePayload: []byte("{}"), PublicationResult: []byte{}, State: int64(state.state),
					}))
					_, err := connection.ExecContext(t.Context(), "UPDATE tab_creations SET created_at = ?, cancel_progress_payload = ? WHERE user_id = ? AND worker_id = ? AND tab_id = ?", checkpoint.timestamp, checkpoint.payload, owner, worker, id)
					require.NoError(t, err)
				}
			}
		}
	}
	expectedIDs := []string{
		"a-pending-y-empty", "a-pending-z-null", "d-cancel-requested-y-empty", "d-cancel-requested-z-null", "e-canceled-y-empty", "f-published-y-empty", "f-published-z-null",
		"a-pending-x-data", "d-cancel-requested-x-data", "e-canceled-x-data", "f-published-x-data",
	}
	for _, owner := range []string{"owner", "other-owner", "missing-owner", ""} {
		for _, worker := range []string{"worker", "other-worker", "missing-worker", ""} {
			t.Run(fmt.Sprintf("owner=%s/worker=%s", owner, worker), func(t *testing.T) {
				rows, err := list(owner, worker)
				require.NoError(t, err)
				var actualIDs []string
				for _, row := range rows {
					actualIDs = append(actualIDs, row.TabID)
					assert.Equal(t, owner, row.UserID)
					assert.Equal(t, worker, row.WorkerID)
					assert.Equal(t, []byte(owner+"|"+worker), row.RequestPayload)
				}
				if owner == "" || owner == "missing-owner" || worker == "" || worker == "missing-worker" {
					assert.Empty(t, actualIDs)
				} else {
					assert.Equal(t, expectedIDs, actualIDs)
				}
			})
		}
	}
	_, err = list("owner", "worker")
	require.NoError(t, err)
	t.Logf("generated recovery SQL: %s; arguments: %v", recorder.query, recorder.args)
	plan, err := connection.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+recorder.query, recorder.args...)
	require.NoError(t, err)
	for plan.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, plan.Scan(&id, &parent, &unused, &detail))
		t.Logf("generated recovery plan: %s", detail)
	}
	require.NoError(t, plan.Err())
	require.NoError(t, plan.Close())

	for _, owner := range []string{"owner", "other-owner", "missing-owner", ""} {
		for _, worker := range []string{"worker", "other-worker", "missing-worker", ""} {
			t.Run(fmt.Sprintf("point-owner=%s/worker=%s", owner, worker), func(t *testing.T) {
				t.Parallel()
				q := queries.New(connection)
				for _, state := range states {
					id := state.id + "-y-empty"
					row, err := q.GetTabCreation(t.Context(), queries.GetTabCreationParams{UserID: owner, WorkerID: worker, TabID: id})
					exists := owner != "" && owner != "missing-owner" && worker != "" && worker != "missing-worker"
					if exists {
						require.NoError(t, err)
						assert.Equal(t, owner, row.UserID)
						assert.Equal(t, worker, row.WorkerID)
						assert.Equal(t, id, row.TabID)
						assert.Equal(t, int64(state.state), row.State)
						assert.Equal(t, []byte(owner+"|"+worker), row.RequestPayload)
					} else {
						require.ErrorIs(t, err, sql.ErrNoRows)
					}
					for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
						pending, err := q.HasPendingTabCreation(t.Context(), queries.HasPendingTabCreationParams{
							UserID: owner, WorkerID: worker, TabID: id, TabType: int64(kind),
							State: int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_PENDING), State_2: int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCEL_REQUESTED),
						})
						require.NoError(t, err)
						expected := exists && kind == leapmuxv1.TabType_TAB_TYPE_FILE && (state.id == "a-pending" || state.id == "d-cancel-requested")
						assert.Equal(t, expected, pending, "tab %s, type %s", id, kind)
					}
				}
				for _, id := range []string{"", "missing-tab"} {
					_, err := q.GetTabCreation(t.Context(), queries.GetTabCreationParams{UserID: owner, WorkerID: worker, TabID: id})
					assert.ErrorIs(t, err, sql.ErrNoRows)
					present, err := q.HasPendingTabCreation(t.Context(), queries.HasPendingTabCreationParams{
						UserID: owner, WorkerID: worker, TabID: id, TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE),
						State: int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_PENDING), State_2: int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCEL_REQUESTED),
					})
					require.NoError(t, err)
					assert.False(t, present)
				}
			})
		}
	}
}
