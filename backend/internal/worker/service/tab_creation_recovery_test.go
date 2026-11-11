package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/tabcreation"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func seedPendingCreation(t *testing.T, svc *Service, owner, id string) db.GetTabCreationParams {
	t.Helper()
	request := &leapmuxv1.RegisterTabPayloadRequest{Creation: creationIntent(id), Payload: &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: "/foreign/file.txt"}}}}
	requestPayload, err := proto.Marshal(request)
	require.NoError(t, err)
	publicationPayload, err := proto.Marshal(&leapmuxv1.TabPublication{Creation: request.Creation, TabType: leapmuxv1.TabType_TAB_TYPE_FILE})
	require.NoError(t, err)
	key := db.GetTabCreationParams{UserID: owner, WorkerID: svc.WorkerID, TabID: id}
	hash, err := tabcreation.Fingerprint(request)
	require.NoError(t, err)
	runtime, err := json.Marshal(creationRecoveryInputs{Runtime: []byte(`{}`), Unscoped: true})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.InsertTabCreation(t.Context(), db.InsertTabCreationParams{UserID: key.UserID, WorkerID: key.WorkerID, TabID: key.TabID, TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), RequestHash: hash, RequestPayload: requestPayload, PublicationPayload: publicationPayload, RuntimePayload: runtime, PublicationResult: []byte{}, State: creationPending}))
	return key
}

func TestCreationRecovery_SkipsAnotherOwnerBeforeInventory(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	key := seedPendingCreation(t, svc, "another-owner", "a-foreign-pending")
	own := seedPendingCreation(t, svc, "user-1", "z-own-pending")
	require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: own.UserID, TabID: own.TabID, Payload: &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: "/foreign/file.txt"}}}}))
	var publications atomic.Int32
	svc.PublishTab = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		assert.Equal(t, own.TabID, request.GetCreation().GetTabId())
		publications.Add(1)
		return fixturePublishTab(ctx, request)
	}
	require.NoError(t, svc.ResolvePendingTabCreations(t.Context()), "another owner's pending row must not prevent the registered owner's inventory preparation")
	row, err := svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, creationPending, row.State)
	assert.NotEmpty(t, row.RequestPayload)
	current, err := svc.Queries.GetTabCreation(t.Context(), own)
	require.NoError(t, err)
	assert.Equal(t, creationCommitted, current.State)
	assert.Equal(t, int32(1), publications.Load())
}

func TestCreationRecovery_DoesNotProtectAnotherOwnersPayload(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	svc, _, _ := setupTestService(t)
	key := seedPendingCreation(t, svc, "another-owner", "shared-payload-id")
	payload := &leapmuxv1.TabPayload{WorkingDir: directory, Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: directory + "/file.txt"}}}
	require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: "user-1", TabID: key.TabID, Payload: payload}))
	pass := svc.BeginTabReconciliation()
	assert.False(t, pass.ReapIf(leapmuxv1.TabType_TAB_TYPE_FILE, "another-owner", key.TabID, func() bool { t.Error("a protected owner reached its absence clock"); return true }))
	defer pass.Done()
	assert.True(t, pass.ReapIf(leapmuxv1.TabType_TAB_TYPE_FILE, "user-1", key.TabID, func() bool { return true }), "another owner's pending creation cannot protect this owner's absent payload")
	_, err := svc.TabPayloads.Get(t.Context(), "user-1", key.TabID)
	assert.ErrorIs(t, err, ErrTabPayloadNotFound)
	row, err := svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, creationPending, row.State)
}

func TestCreationRecovery_RejectsZeroAndForeignStoredOwners(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct{ label, registered, stored string }{
		{"both missing", "", ""}, {"missing identity", "", "user-1"}, {"missing stored owner", "user-1", ""}, {"foreign owner", "user-1", "another-owner"},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			t.Parallel()
			var observing atomic.Bool
			var queries atomic.Int32
			svc, _, _ := setupTestService(t, withQueryRewrite(func(query string) string {
				if observing.Load() {
					queries.Add(1)
				}
				return query
			}))
			if scenario.registered == "" {
				svc.owner.Store(nil)
			}
			observing.Store(true)
			err := svc.resumeTabCreation(t.Context(), db.TabCreation{UserID: scenario.stored, WorkerID: svc.WorkerID, TabID: "unowned", State: creationPending})
			observing.Store(false)
			require.ErrorContains(t, err, "matching Worker owner")
			assert.Zero(t, queries.Load())
		})
	}
	t.Run("matching completed cancellation", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		key := seedPendingCreation(t, svc, "user-1", "completed")
		_, err := svc.DB.ExecContext(t.Context(), "UPDATE tab_creations SET state = ? WHERE user_id = ? AND worker_id = ? AND tab_id = ?", creationCanceled, key.UserID, key.WorkerID, key.TabID)
		require.NoError(t, err)
		row, err := svc.Queries.GetTabCreation(t.Context(), key)
		require.NoError(t, err)
		require.NoError(t, svc.resumeTabCreation(t.Context(), row))
	})
}

func TestCreationRecovery_RefusesAnUndeliveredOwnerBeforeQueries(t *testing.T) {
	t.Parallel()
	var observing atomic.Bool
	var queries atomic.Int32
	svc, _, _ := setupTestService(t, withQueryRewrite(func(query string) string {
		if observing.Load() {
			queries.Add(1)
		}
		return query
	}))
	svc.owner.Store(nil)
	observing.Store(true)
	err := svc.ResolvePendingTabCreations(t.Context())
	observing.Store(false)
	require.ErrorIs(t, err, errCreationOwnerMissing)
	assert.Zero(t, queries.Load())
}

func TestCreationRecovery_RejectsBlankStorageOwnersAndWorkers(t *testing.T) {
	t.Parallel()
	for _, blank := range []string{"owner", "worker"} {
		t.Run(blank, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			userID, workerID := "user-1", svc.WorkerID
			if blank == "owner" {
				userID = ""
			} else {
				workerID = ""
			}
			err := svc.Queries.InsertTabCreation(t.Context(), db.InsertTabCreationParams{UserID: userID, WorkerID: workerID, TabID: "blank-key", TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), RequestHash: make([]byte, 32), RequestPayload: []byte{}, PublicationPayload: []byte{}, RuntimePayload: []byte{}, PublicationResult: []byte{}, State: creationPending})
			require.Error(t, err)
		})
	}
}
