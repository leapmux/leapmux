package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCreationCancellation_ReplaysPendingRemoveResult(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	repository := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repository, "cancel-result")
	path := filepath.Join(worktree.WorkingDir, "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("uncommitted file\n"), 0o600))
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.PublishTab = func(ctx context.Context, _ *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		close(entered)
		select {
		case <-release:
			return &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Reason: leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, Message: "Tab creation was canceled."}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	registration := newTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch(dispatcher, leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, &leapmuxv1.RegisterTabPayloadRequest{Creation: creationIntent("cancel-file"), Payload: &leapmuxv1.TabPayload{WorkingDir: worktree.WorkingDir, Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: path}}}}, registration)
	}()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("registration did not reach publication")
	}
	request := &leapmuxv1.CloseTabRequest{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "cancel-file"}, WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE}
	first := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, request, first)
	require.Empty(t, first.rejections())
	require.Len(t, first.responsesSnapshot(), 1)
	firstResult := &leapmuxv1.CloseTabResponse{}
	require.NoError(t, proto.Unmarshal(first.responsesSnapshot()[0].GetPayload(), firstResult))
	require.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, firstResult.GetWorktreeRemoval())
	_, err := os.Stat(worktree.WorkingDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	retry := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, request, retry)
	require.Empty(t, retry.rejections())
	require.Len(t, retry.responsesSnapshot(), 1)
	retryResult := &leapmuxv1.CloseTabResponse{}
	require.NoError(t, proto.Unmarshal(retry.responsesSnapshot()[0].GetPayload(), retryResult))
	assert.True(t, proto.Equal(firstResult, retryResult), "a retry after a lost response must return the recorded removal outcome")
}

func TestCreationCancellation_OwnsCleanupAfterRecoveryCallerCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, _, _ := setupTestService(t)
		directory := t.TempDir()
		payload := &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filepath.Join(directory, "pending.txt")}}}
		require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: "user-1", TabID: "caller-cancel", Payload: payload}))
		publication, err := proto.Marshal(&leapmuxv1.TabPublication{Creation: creationIntent("caller-cancel"), TabType: leapmuxv1.TabType_TAB_TYPE_FILE})
		require.NoError(t, err)
		require.NoError(t, svc.Queries.InsertTabCreation(t.Context(), db.InsertTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "caller-cancel", TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), RequestHash: make([]byte, 32), RequestPayload: []byte{}, PublicationPayload: publication, RuntimePayload: []byte{}, PublicationResult: []byte{}, State: creationPending}))
		changed, err := svc.Queries.RequestTabCreationCancel(t.Context(), db.RequestTabCreationCancelParams{State: creationCancelRequested, CancelAction: sql.NullInt64{Int64: int64(leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP), Valid: true}, UserID: "user-1", WorkerID: svc.WorkerID, TabID: "caller-cancel", State_2: creationPending})
		require.NoError(t, err)
		require.Equal(t, int64(1), changed)
		key := db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "caller-cancel"}
		row, err := svc.Queries.GetTabCreation(t.Context(), key)
		require.NoError(t, err)
		release := make(chan struct{})
		observed := make(chan error, 1)
		var first sync.Once
		svc.CancelTabPublication = func(ctx context.Context, _ *leapmuxv1.TabPublication) error {
			first.Do(func() { observed <- ctx.Err() })
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		caller, cancel := context.WithCancel(t.Context())
		cancel()
		done := make(chan error, 1)
		go func() { _, err := svc.finishPendingCreationCancellation(caller, row); done <- err }()
		synctest.Wait()
		returned := false
		select {
		case err := <-done:
			returned = true
			assert.ErrorIs(t, err, context.Canceled)
		default:
			assert.Fail(t, "the canceled recovery caller remained attached to cleanup")
		}
		if returned {
			require.NoError(t, <-observed, "the cleanup producer must use the Service context")
			close(release)
			svc.creations.workers.Wait()
			completed, err := svc.Queries.GetTabCreation(t.Context(), key)
			require.NoError(t, err)
			assert.Equal(t, creationCanceled, completed.State)
			_, err = svc.TabPayloads.Get(t.Context(), "user-1", "caller-cancel")
			assert.ErrorIs(t, err, ErrTabPayloadNotFound)
		} else {
			close(release)
			svc.creations.stop()
			assert.ErrorIs(t, <-done, context.Canceled)
		}
	})
}

func TestCreationCancellation_CloseCallerDetachesWhileOwnedCleanupContinues(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	svc, dispatcher, _ := setupTestService(t)
	ctx := creationOutcomeContext(t)
	publicationEntered, cancellationEntered := make(chan struct{}), make(chan struct{})
	releasePublication, releaseCancellation := make(chan struct{}), make(chan struct{})
	var publicationOnce, cancellationOnce sync.Once
	var callers sync.WaitGroup
	finishPublication := sync.OnceFunc(func() { close(releasePublication) })
	finishCancellation := sync.OnceFunc(func() { close(releaseCancellation) })
	defer func() { finishCancellation(); finishPublication(); svc.creations.stop(); callers.Wait() }()
	svc.PublishTab = func(ctx context.Context, _ *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		publicationOnce.Do(func() { close(publicationEntered) })
		select {
		case <-releasePublication:
			return &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Message: "Tab creation was canceled."}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	svc.CancelTabPublication = func(ctx context.Context, _ *leapmuxv1.TabPublication) error {
		cancellationOnce.Do(func() { close(cancellationEntered) })
		select {
		case <-releaseCancellation:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	method, creation := creationRequestForTest(t, leapmuxv1.TabType_TAB_TYPE_FILE, "close-caller", directory)
	creatorDone := make(chan struct{})
	callers.Go(func() { defer close(creatorDone); dispatch(dispatcher, method, creation, newTestWriter()) })
	select {
	case <-publicationEntered:
	case <-ctx.Done():
		t.Fatal("creation did not reach publication")
	}
	callerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	request := &leapmuxv1.CloseTabRequest{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "close-caller"}, WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP}
	writer, done := newTestWriter(), make(chan struct{})
	callers.Go(func() {
		defer close(done)
		svc.handleCloseTab(callerCtx, channel.NewCaller(userid.MustNew("user-1"), authscope.NonAdminGrant()), request, writer)
	})
	select {
	case <-cancellationEntered:
	case <-ctx.Done():
		t.Fatal("close did not reach cancellation")
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the canceled CloseTab caller remained attached to owned cleanup")
	}
	key := db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "close-caller"}
	row, err := svc.Queries.GetTabCreation(ctx, key)
	require.NoError(t, err)
	require.Equal(t, creationCancelRequested, row.State)
	_, err = svc.TabPayloads.Get(ctx, "user-1", "close-caller")
	require.NoError(t, err, "caller cancellation must not stop or complete cleanup")
	finishCancellation()
	finishPublication()
	select {
	case <-creatorDone:
	case <-ctx.Done():
		t.Fatal("creator did not finish")
	}
	svc.creations.workers.Wait()
	row, err = svc.Queries.GetTabCreation(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, creationCanceled, row.State)
	assert.True(t, row.CloseResponsePayload.Valid)
	_, err = svc.TabPayloads.Get(ctx, "user-1", "close-caller")
	assert.ErrorIs(t, err, ErrTabPayloadNotFound)
	replay := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, request, replay)
	require.Empty(t, replay.rejections())
	require.Len(t, replay.responsesSnapshot(), 1)
	result := &leapmuxv1.CloseTabResponse{}
	require.NoError(t, proto.Unmarshal(replay.responsesSnapshot()[0].GetPayload(), result))
	saved, err := recordedCreationCloseResponse(row)
	require.NoError(t, err)
	assert.True(t, proto.Equal(saved, result))
}

func TestCreationCancellation_OrdersPayloadRegistrationBeforeRevocation(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _ := setupTestService(t)
			ctx := creationOutcomeContext(t)
			events := make(chan *leapmuxv1.WorkerPrivateEvent, 4)
			svc.PrivateEvents.subscribers["user-1"] = map[string]chan *leapmuxv1.WorkerPrivateEvent{"ordering": events}
			persisted, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			defer func() { finish(); svc.creations.stop(); <-done }()
			svc.afterPayloadPersistenceFn = func(ctx context.Context) {
				close(persisted)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			method, request := creationRequestForTest(t, kind, "payload-order", t.TempDir())
			registration := newTestWriter()
			go func() { defer close(done); dispatch(dispatcher, method, request, registration) }()
			select {
			case <-persisted:
			case <-ctx.Done():
				t.Fatal("creation did not persist its payload")
			}
			closed := newTestWriter()
			dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
				Tab:            &leapmuxv1.TabIdent{TabType: kind, TabId: "payload-order"},
				WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
			}, closed)
			require.Empty(t, closed.rejections())
			require.Len(t, closed.responsesSnapshot(), 1)
			finish()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("creation did not finish after cancellation")
			}
			require.Len(t, registration.rejections(), 1)
			require.Len(t, events, 2)
			assert.NotNil(t, (<-events).GetTabPayloadRegistered(), "registration must precede cancellation revocation")
			assert.NotNil(t, (<-events).GetTabPayloadRevoked(), "revocation must be the final payload event")
			_, err := svc.TabPayloads.Get(ctx, "user-1", "payload-order")
			require.ErrorIs(t, err, ErrTabPayloadNotFound)
		})
	}
}
