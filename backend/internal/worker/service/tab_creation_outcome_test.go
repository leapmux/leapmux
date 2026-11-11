package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func creationOutcomeContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func creationRequestForTest(t *testing.T, kind leapmuxv1.TabType, id, directory string) (string, proto.Message) {
	t.Helper()
	switch kind {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{Creation: creationIntent(id), WorkingDir: directory}
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{Target: &leapmuxv1.OpenTerminalRequest_Creation{Creation: creationIntent(id)}, WorkingDir: directory}
	case leapmuxv1.TabType_TAB_TYPE_FILE:
		return leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, &leapmuxv1.RegisterTabPayloadRequest{Creation: creationIntent(id), Payload: &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filepath.Join(directory, id+".txt")}}}}
	case leapmuxv1.TabType_TAB_TYPE_IMAGE:
		return leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, &leapmuxv1.RegisterTabPayloadRequest{Creation: creationIntent(id), Payload: &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{AgentId: "source", Seq: 1}}}}
	default:
		t.Fatal("unsupported creation type in the fixture")
		return "", nil
	}
}

func failCreationProcessStarts(svc *Service) *atomic.Int32 {
	starts := &atomic.Int32{}
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		starts.Add(1)
		return nil, errors.New("the test provider fails startup")
	}
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		starts.Add(1)
		return errors.New("the test terminal fails startup")
	}
	return starts
}

func TestCreationOutcome_RejectsDefinitivelyAndPermitsAFreshAttempt(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _ := setupTestService(t)
			defer drainAllInFlight(svc)
			failCreationProcessStarts(svc)
			var writable atomic.Bool
			var publications atomic.Int32
			svc.PublishTab = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
				publications.Add(1)
				if writable.Load() {
					return fixturePublishTab(ctx, request)
				}
				return &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Reason: leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, Message: "The workspace is archived."}}}, nil
			}
			directory := t.TempDir()
			method, request := creationRequestForTest(t, kind, "rejected", directory)
			for range 2 {
				writer := newTestWriter()
				dispatch(dispatcher, method, request, writer)
				require.Len(t, writer.rejections(), 1)
				assert.Equal(t, int32(codes.Aborted), writer.rejections()[0].code)
				assert.Empty(t, writer.responsesSnapshot())
			}
			row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "rejected"})
			require.NoError(t, err)
			assert.Equal(t, creationRejected, row.State)
			assert.Equal(t, int32(1), publications.Load(), "replaying a rejected ID must not publish it again")
			writable.Store(true)
			method, request = creationRequestForTest(t, kind, "fresh-attempt", directory)
			writer := newTestWriter()
			dispatch(dispatcher, method, request, writer)
			require.Empty(t, writer.rejections())
			require.Len(t, writer.responsesSnapshot(), 1, "a fresh ID can succeed after the workspace becomes writable")
		})
	}
}

func TestCreationOutcome_ReplaysOnlyACurrentlyHostedPublication(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		for _, presence := range []leapmuxv1.TabPublicationPresence{leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_ABSENT, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_REASSIGNED} {
			t.Run(kind.String()+"/"+presence.String(), func(t *testing.T) {
				t.Parallel()
				svc, dispatcher, _ := setupTestService(t)
				defer drainAllInFlight(svc)
				starts := failCreationProcessStarts(svc)
				method, request := creationRequestForTest(t, kind, "replayed", t.TempDir())
				first := newTestWriter()
				dispatch(dispatcher, method, request, first)
				require.Empty(t, first.rejections())
				svc.AgentStartup.WaitForInFlight()
				svc.TerminalStartup.WaitForInFlight()
				initialStarts := starts.Load()
				var resolutions, cancellations atomic.Int32
				svc.ResolveTabPublication = func(ctx context.Context, r *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
					resolutions.Add(1)
					receipt, err := fixturePublishTab(ctx, r)
					return &leapmuxv1.ResolveTabPublicationResponse{Receipt: receipt, Presence: presence, ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED}, err
				}
				svc.CancelTabPublication = func(context.Context, *leapmuxv1.TabPublication) error { cancellations.Add(1); return nil }
				retry := newTestWriter()
				dispatch(dispatcher, method, request, retry)
				if presence == leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED {
					require.Empty(t, retry.rejections(), "an archived hosted tab still exists, including a visible startup failure")
					require.Len(t, retry.responsesSnapshot(), 1)
				} else {
					require.Len(t, retry.rejections(), 1, "a local row cannot prove current Hub ownership")
					assert.Equal(t, int32(codes.Aborted), retry.rejections()[0].code)
					assert.Empty(t, retry.responsesSnapshot())
				}
				assert.Positive(t, resolutions.Load())
				assert.Zero(t, cancellations.Load(), "replay must not cancel a reassigned canonical tab")
				assert.Equal(t, initialStarts, starts.Load(), "replay must not start another process")
				switch kind {
				case leapmuxv1.TabType_TAB_TYPE_AGENT:
					row, err := svc.Queries.GetAgentByID(t.Context(), "replayed")
					require.NoError(t, err)
					assert.False(t, row.ClosedAt.Valid)
				case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
					row, err := svc.Queries.GetTerminal(t.Context(), "replayed")
					require.NoError(t, err)
					assert.False(t, row.ClosedAt.Valid)
				default:
					_, err := svc.TabPayloads.Get(t.Context(), "user-1", "replayed")
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestCreationOutcome_DistinguishesReplayReadFailureFromResourceClosure(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			var failRead atomic.Bool
			queryName := "GetWorkerTabPayload"
			if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
				queryName = "GetAgentByID"
			}
			if kind == leapmuxv1.TabType_TAB_TYPE_TERMINAL {
				queryName = "GetTerminal"
			}
			svc, dispatcher, _ := setupTestService(t, withQueryRewrite(func(query string) string {
				if failRead.Load() && strings.Contains(query, "-- name: "+queryName+" :one") {
					return "SELECT * FROM replay_read_failure"
				}
				return query
			}))
			// Install the fixture before dispatch. Payload reads use a store-owned handle.
			svc.TabPayloads.q = svc.Queries
			defer drainAllInFlight(svc)
			failCreationProcessStarts(svc)
			method, request := creationRequestForTest(t, kind, "read-failure", t.TempDir())
			first := newTestWriter()
			dispatch(dispatcher, method, request, first)
			require.Empty(t, first.rejections())
			svc.AgentStartup.WaitForInFlight()
			svc.TerminalStartup.WaitForInFlight()
			failRead.Store(true)
			retry := newTestWriter()
			dispatch(dispatcher, method, request, retry)
			require.Len(t, retry.rejections(), 1)
			assert.Equal(t, int32(codes.Internal), retry.rejections()[0].code, "a database failure cannot prove that creation ended")
			failRead.Store(false)
			svc.closeTabForConvergence(kind, "user-1", "read-failure", dropWorktreeLink)
			closed := newTestWriter()
			dispatch(dispatcher, method, request, closed)
			require.Len(t, closed.rejections(), 1)
			assert.Equal(t, int32(codes.Aborted), closed.rejections()[0].code)
		})
	}
}

func TestCreationOutcome_CallerCancellationLeavesTheOwnedResultUnknown(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	svc.PublishTab = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return fixturePublishTab(ctx, request)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	_, request := creationRequestForTest(t, leapmuxv1.TabType_TAB_TYPE_FILE, "canceled-caller", t.TempDir())
	callerContext, cancel := context.WithCancel(creationOutcomeContext(t))
	defer cancel()
	writer, done := newTestWriter(), make(chan struct{})
	go func() {
		defer close(done)
		svc.handleRegisterTabPayload(callerContext, channel.NewCaller(userid.MustNew("user-1"), authscope.NonAdminGrant()), request.(*leapmuxv1.RegisterTabPayloadRequest), writer)
	}()
	select {
	case <-entered:
	case <-callerContext.Done():
		t.Fatal("creation never reached publication")
	}
	cancel()
	<-done
	require.Len(t, writer.rejections(), 1)
	assert.NotEqual(t, int32(codes.Aborted), writer.rejections()[0].code)
	finish()
	svc.creations.workers.Wait()
	row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "canceled-caller"})
	require.NoError(t, err)
	assert.Equal(t, creationCommitted, row.State, "the Worker can still commit after its caller stops observing")
}

func TestCreationOutcome_PublishesRegistrationThroughTheConfiguredPayloadBus(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		for _, enabled := range []bool{true, false} {
			t.Run(kind.String()+"/"+map[bool]string{true: "distinct bus", false: "no bus"}[enabled], func(t *testing.T) {
				t.Parallel()
				svc, dispatcher, _ := setupTestService(t)
				bus := NewPrivateEventsBus()
				t.Cleanup(bus.Stop)
				registered := make(chan *leapmuxv1.WorkerPrivateEvent, 2)
				other := make(chan *leapmuxv1.WorkerPrivateEvent, 2)
				bus.subscribers["user-1"] = map[string]chan *leapmuxv1.WorkerPrivateEvent{"test": registered}
				svc.PrivateEvents.subscribers["user-1"] = map[string]chan *leapmuxv1.WorkerPrivateEvent{"test": other}
				if enabled {
					svc.TabPayloads.events = bus
				} else {
					svc.TabPayloads.events = nil
				}
				method, request := creationRequestForTest(t, kind, "configured-registration", t.TempDir())
				writer := newTestWriter()
				dispatch(dispatcher, method, request, writer)
				require.Empty(t, writer.rejections())
				require.Len(t, writer.responsesSnapshot(), 1)
				assert.Empty(t, other, "the Service bus must not replace the payload store's dependency")
				if !enabled {
					assert.Empty(t, registered)
					return
				}
				require.Len(t, registered, 1)
				event := (<-registered).GetTabPayloadRegistered()
				require.NotNil(t, event)
				assert.Equal(t, "configured-registration", event.GetTabId())
				stored, err := svc.TabPayloads.Get(t.Context(), "user-1", event.GetTabId())
				require.NoError(t, err)
				assert.True(t, proto.Equal(stored, event.GetPayload()), "the post-commit event must carry the normalized stored payload")
			})
		}
	}
}

func TestCreationOutcome_LateRejectionPreservesAnAdmittedRemove(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "late-rejection-remove")
	publicationEntered, publicationRelease := make(chan struct{}), make(chan struct{})
	cancellationEntered, cancellationRelease := make(chan struct{}), make(chan struct{})
	var enteredPublish, enteredCancel, releasePublishOnce, releaseCancelOnce sync.Once
	releasePublish := func() { releasePublishOnce.Do(func() { close(publicationRelease) }) }
	releaseCancel := func() { releaseCancelOnce.Do(func() { close(cancellationRelease) }) }
	svc.PublishTab = func(ctx context.Context, _ *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		enteredPublish.Do(func() { close(publicationEntered) })
		select {
		case <-publicationRelease:
			return &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Reason: leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, Message: "Tab creation was canceled."}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	svc.CancelTabPublication = func(ctx context.Context, _ *leapmuxv1.TabPublication) error {
		enteredCancel.Do(func() { close(cancellationEntered) })
		select {
		case <-cancellationRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	method, request := creationRequestForTest(t, leapmuxv1.TabType_TAB_TYPE_FILE, "late-rejection", worktree.WorkingDir)
	created, creationDone := newTestWriter(), make(chan struct{})
	go func() { defer close(creationDone); dispatch(dispatcher, method, request, created) }()
	defer func() { releasePublish(); <-creationDone }()
	ctx := creationOutcomeContext(t)
	select {
	case <-publicationEntered:
	case <-ctx.Done():
		t.Fatal("creation did not reach publication")
	}
	closed, closeDone := newTestWriter(), make(chan struct{})
	go func() {
		defer close(closeDone)
		dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "late-rejection"}, WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE}, closed)
	}()
	defer func() { releaseCancel(); <-closeDone }()
	select {
	case <-cancellationEntered:
	case <-ctx.Done():
		t.Fatal("close did not reach its held cancellation acknowledgement")
	}
	row, err := svc.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "late-rejection"})
	require.NoError(t, err)
	require.Equal(t, creationCancelRequested, row.State)
	releasePublish()
	select {
	case <-creationDone:
	case <-ctx.Done():
		t.Fatal("creation did not finish after rejection")
	}
	require.Len(t, created.rejections(), 1)
	assert.Equal(t, int32(codes.Aborted), created.rejections()[0].code)
	_, err = svc.TabPayloads.Get(ctx, "user-1", "late-rejection")
	require.NoError(t, err, "late rejection must not perform KEEP cleanup while REMOVE owns teardown")
	linked, err := svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{UserID: "user-1", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "late-rejection"})
	require.NoError(t, err)
	assert.Equal(t, worktree.WorktreeID, linked.ID)
	requested := newTestWriter()
	dispatch(dispatcher, method, request, requested)
	require.Len(t, requested.rejections(), 1)
	assert.Equal(t, int32(codes.Aborted), requested.rejections()[0].code)
	releaseCancel()
	select {
	case <-closeDone:
	case <-ctx.Done():
		t.Fatal("REMOVE did not finish after cancellation acknowledgement")
	}
	require.Empty(t, closed.rejections())
	require.Len(t, closed.responsesSnapshot(), 1)
	result := &leapmuxv1.CloseTabResponse{}
	require.NoError(t, proto.Unmarshal(closed.responsesSnapshot()[0].GetPayload(), result))
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.GetWorktreeRemoval())
	_, err = os.Stat(worktree.WorkingDir)
	assert.ErrorIs(t, err, os.ErrNotExist)
	finished := newTestWriter()
	dispatch(dispatcher, method, request, finished)
	require.Len(t, finished.rejections(), 1)
	assert.Equal(t, int32(codes.Aborted), finished.rejections()[0].code)
}

func TestCreationOutcome_ResolvedRejectionEndsTheOwnedFlight(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _ := setupTestService(t)
			starts := failCreationProcessStarts(svc)
			entered := make(chan struct{})
			var once sync.Once
			svc.ResolveTabPublication = func(context.Context, *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
				once.Do(func() { close(entered) })
				return &leapmuxv1.ResolveTabPublicationResponse{Receipt: &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Reason: leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, Message: "The tab was canceled."}}}}, nil
			}
			method, request := creationRequestForTest(t, kind, "resolver-rejected", t.TempDir())
			writer, done := newTestWriter(), make(chan struct{})
			go func() { defer close(done); dispatch(dispatcher, method, request, writer) }()
			ctx := creationOutcomeContext(t)
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("creation never resolved its publication")
			}
			select {
			case <-done:
			case <-ctx.Done():
				svc.creations.stop()
				<-done
				assert.Fail(t, "a definitive resolver rejection kept restoring the creation")
			}
			require.Len(t, writer.rejections(), 1)
			assert.Equal(t, int32(codes.Aborted), writer.rejections()[0].code)
			row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "resolver-rejected"})
			require.NoError(t, err)
			assert.Equal(t, creationCanceled, row.State)
			assert.Zero(t, len(row.RuntimePayload), "a resolved rejection must clear private recovery inputs")
			assert.Zero(t, starts.Load())
		})
	}
}

func TestCreationOutcome_ResolvedReplayDoesNotBlockInventoryPreparation(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	starts := failCreationProcessStarts(svc)
	directory := t.TempDir()
	_ = savedAgentCreation(t, svc, creationStartupIntent{Plan: gitModePlan{Mode: gitModeUseCurrent, WorkingDir: directory, PlannedWorkingDir: directory}})
	require.NoError(t, svc.Queries.SetAgentStartupError(t.Context(), db.SetAgentStartupErrorParams{ID: "saved", StartupError: "the original startup failed"}))
	svc.ResolveTabPublication = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
		receipt, err := fixturePublishTab(ctx, request)
		return &leapmuxv1.ResolveTabPublicationResponse{Receipt: receipt, Presence: leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_ABSENT}, err
	}
	require.NoError(t, svc.ResolvePendingTabCreations(t.Context()), "a proven current absence is resolved work, even after the stored startup outcome completes")
	assert.Zero(t, starts.Load())
	row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "saved"})
	require.NoError(t, err)
	assert.Equal(t, creationCommitted, row.State)
	assert.Zero(t, len(row.RuntimePayload))
}

func TestCreationOutcome_RequiresWorkspaceWriteBeforeAnyMutation(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _ := setupTestService(t)
			var publications atomic.Int32
			svc.PublishTab = func(context.Context, *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
				publications.Add(1)
				return nil, errors.New("the denied creation reached publication")
			}
			method, request := creationRequestForTest(t, kind, "scope-denied", t.TempDir())
			payload, err := proto.Marshal(request)
			require.NoError(t, err)
			caller := channel.NewCaller(userid.MustNew("user-1"), mustScopes("worker:read agent:read agent:write terminal:write file:read git:write"))
			writer := newTestWriter()
			dispatchTestRequest(dispatcher, t.Context(), caller, &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, writer)
			require.Len(t, writer.rejections(), 1)
			assert.Equal(t, int32(codes.PermissionDenied), writer.rejections()[0].code)
			assert.Zero(t, publications.Load())
			for _, table := range []string{"tab_creations", "agents", "terminals", "worker_tab_payloads"} {
				var count int
				require.NoError(t, svc.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))
				assert.Zero(t, count, "%s must remain unchanged without workspace-write permission", table)
			}
		})
	}
}
