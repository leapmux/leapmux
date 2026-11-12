package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

var errPublicationLifecycleRead = errors.New("workspace lifecycle listing failed")
var errPublicationReceiptRead = errors.New("publication receipt read failed")

type publicationLifecycleGate struct {
	enabled  atomic.Bool
	arrivals atomic.Int32
	ready    chan struct{}
	released sync.Once
}

func (g *publicationLifecycleGate) release() { g.released.Do(func() { close(g.ready) }) }
func (g *publicationLifecycleGate) wait(ctx context.Context) error {
	if !g.enabled.Load() {
		return nil
	}
	if g.arrivals.Add(1) == 2 {
		g.release()
	}
	select {
	case <-g.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type publicationLifecycleFaults struct {
	store.Store
	failList      *atomic.Bool
	lists         *atomic.Int32
	failReceipt   *atomic.Bool
	failConsumeID *atomic.Int64
	gate          *publicationLifecycleGate
}

func (s publicationLifecycleFaults) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(publicationLifecycleFaults{Store: tx, failList: s.failList, lists: s.lists, failReceipt: s.failReceipt, gate: s.gate, failConsumeID: s.failConsumeID})
	})
}

func (s publicationLifecycleFaults) LifecycleOutbox() store.LifecycleOutboxStore {
	return publicationLifecycleRows{LifecycleOutboxStore: s.Store.LifecycleOutbox(), fail: s.failList, lists: s.lists, gate: s.gate, failConsumeID: s.failConsumeID}
}

func (s publicationLifecycleFaults) WorkerTabPublications() store.WorkerTabPublicationStore {
	return publicationLifecycleReceipts{WorkerTabPublicationStore: s.Store.WorkerTabPublications(), fail: s.failReceipt}
}

type publicationLifecycleReceipts struct {
	store.WorkerTabPublicationStore
	fail *atomic.Bool
}

func (s publicationLifecycleReceipts) Get(ctx context.Context, key store.WorkerTabPublicationKey) (*store.WorkerTabPublication, error) {
	if s.fail.Load() {
		return nil, errPublicationReceiptRead
	}
	return s.WorkerTabPublicationStore.Get(ctx, key)
}

type publicationLifecycleRows struct {
	failConsumeID *atomic.Int64
	store.LifecycleOutboxStore
	fail  *atomic.Bool
	lists *atomic.Int32
	gate  *publicationLifecycleGate
}

func (s publicationLifecycleRows) ListPending(ctx context.Context, p store.ListPendingLifecycleOutboxParams) ([]store.LifecycleOutboxRow, error) {
	s.lists.Add(1)
	if s.fail.Load() {
		return nil, errPublicationLifecycleRead
	}
	if err := s.gate.wait(ctx); err != nil {
		return nil, err
	}
	return s.LifecycleOutboxStore.ListPending(ctx, p)
}

func (s publicationLifecycleRows) MarkConsumed(ctx context.Context, p store.MarkLifecycleOutboxConsumedParams) error {
	if s.failConsumeID.Load() == p.ID {
		return errors.New("workspace lifecycle consumption failed")
	}
	return s.LifecycleOutboxStore.MarkConsumed(ctx, p)
}

type publicationLifecycleFixture struct {
	cancelPublication  func(context.Context, *leapmuxv1.TabPublication) error
	resolvePublication func(context.Context, *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error)
	failConsumeID      atomic.Int64
	base               store.Store
	wrapped            publicationLifecycleFaults
	owner              userid.UserID
	workerID           string
	manager            *crdt.Manager
	workspaces         *WorkspaceService
	failList           atomic.Bool
	lists              atomic.Int32
	call               func(context.Context, *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error)
	failReceipt        atomic.Bool
	gate               publicationLifecycleGate
}

func newPublicationLifecycleFixture(t *testing.T) *publicationLifecycleFixture {
	t.Helper()
	f := &publicationLifecycleFixture{base: hubtestutil.OpenTestStore(t)}
	owner := storetest.SeedUser(t, f.base, "publication-lifecycle-owner")
	worker, workerToken := hubtestutil.CreateTestWorker(t, f.base, owner.ID)
	f.owner, f.workerID = userid.MustNew(owner.ID), worker.ID
	f.gate.ready = make(chan struct{})
	f.wrapped = publicationLifecycleFaults{Store: f.base, failList: &f.failList, lists: &f.lists, failReceipt: &f.failReceipt, gate: &f.gate, failConsumeID: &f.failConsumeID}
	registry := publicationRegistry(t, f.wrapped)
	var err error
	f.manager, err = registry.Get(t.Context(), owner.ID)
	require.NoError(t, err)
	f.workspaces = &WorkspaceService{store: f.wrapped, registry: registry}
	connector := &WorkerConnectorService{
		hasher: hubtestutil.TestSecretHasher(),
		store:  f.wrapped, crdtRegistry: registry}
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, connector)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(4<<20))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	t.Cleanup(f.gate.release)
	client := workerhub.NewConnectorClient(httpServer.Client(), httpServer.URL)
	authorizedContext := func(ctx context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ctx, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer "+workerToken)
		return ctx, cancel
	}
	f.call = func(ctx context.Context, publication *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		ctx, cancel := authorizedContext(ctx)
		defer cancel()
		return client.PublishTab(ctx, &leapmuxv1.PublishTabRequest{Publication: publication})
	}
	f.cancelPublication = func(ctx context.Context, publication *leapmuxv1.TabPublication) error {
		ctx, cancel := authorizedContext(ctx)
		defer cancel()
		_, err := client.CancelTabPublication(ctx, &leapmuxv1.CancelTabPublicationRequest{Publication: publication})
		return err
	}
	f.resolvePublication = func(ctx context.Context, publication *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
		ctx, cancel := authorizedContext(ctx)
		defer cancel()
		return client.ResolveTabPublication(ctx, &leapmuxv1.ResolveTabPublicationRequest{Publication: publication})
	}
	return f
}

func (f *publicationLifecycleFixture) createPendingWorkspace(t *testing.T) string {
	t.Helper()
	f.failList.Store(true)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: f.owner})
	result, err := f.workspaces.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Pending canonical workspace"})
	require.NoError(t, err)
	require.NotEmpty(t, result.GetWorkspaceId())
	assert.NotContains(t, f.manager.Materialized(crdt.SubscriberFilter{}).GetWorkspaces(), result.GetWorkspaceId())
	rows, err := f.base.LifecycleOutbox().ListPending(t.Context(), store.ListPendingLifecycleOutboxParams{UserID: f.owner, Limit: store.CRDTBatchPageLimit})
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	return result.GetWorkspaceId()
}

func firstLeafPublication(workspaceID, tabID string) *leapmuxv1.TabPublication {
	return &leapmuxv1.TabPublication{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, Creation: &leapmuxv1.TabCreation{TabId: tabID, Placement: &leapmuxv1.TabPlacement{WorkspaceId: workspaceID, Destination: &leapmuxv1.TabPlacement_FirstLeaf{FirstLeaf: &emptypb.Empty{}}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}}
}

func TestWorkerTabPublication_CatchesUpAKnownWorkspaceCreate(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	workspaceID := f.createPendingWorkspace(t)
	f.failList.Store(false)
	request := firstLeafPublication(workspaceID, "after-workspace-create")
	response, err := f.call(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished(), "a committed workspace create must not become a permanent missing-root rejection")
	state := f.manager.Materialized(crdt.SubscriberFilter{})
	root := state.GetWorkspaces()[workspaceID].GetRootNodeId()
	assert.NotEmpty(t, root)
	assert.Equal(t, root, response.GetPublished().GetTileId())
	assert.Contains(t, state.GetTabs(), request.GetCreation().GetTabId())
	_, err = f.base.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: f.owner, WorkerID: f.workerID, TabID: request.GetCreation().GetTabId()})
	require.NoError(t, err)
	pending, err := f.base.LifecycleOutbox().ListPending(t.Context(), store.ListPendingLifecycleOutboxParams{UserID: f.owner, Limit: store.CRDTBatchPageLimit})
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestWorkerTabPublication_RetriesLifecycleCatchUpWithoutSavingRejection(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	workspaceID := f.createPendingWorkspace(t)
	request := firstLeafPublication(workspaceID, "retry-lifecycle")
	_, err := f.call(t.Context(), request)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	_, err = f.base.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: f.owner, WorkerID: f.workerID, TabID: request.GetCreation().GetTabId()})
	require.ErrorIs(t, err, store.ErrNotFound)
	f.failList.Store(false)
	response, err := f.call(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
}

func TestWorkerTabPublication_ReplaysReceiptsWithoutLifecycleReads(t *testing.T) {
	t.Parallel()
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected=%t", rejected), func(t *testing.T) {
			t.Parallel()
			f := newPublicationLifecycleFixture(t)
			workspaceID := storetest.SeedWorkspace(t, f.base, f.owner.String(), "existing-publication")
			seedPublicationWorkspace(t, f.manager, workspaceID)
			request := storedPublicationRequest(workspaceID, "existing-receipt")
			if rejected {
				request.Creation.Placement.Destination = &leapmuxv1.TabPlacement_TileId{TileId: "missing-tile"}
			}
			original, err := f.call(t.Context(), request)
			require.NoError(t, err)
			if rejected {
				require.NotNil(t, original.GetRejected())
			} else {
				require.NotNil(t, original.GetPublished())
			}
			key := store.WorkerTabPublicationKey{UserID: f.owner, WorkerID: f.workerID, TabID: request.GetCreation().GetTabId()}
			saved, err := f.base.WorkerTabPublications().Get(t.Context(), key)
			require.NoError(t, err)
			f.failList.Store(true)
			f.lists.Store(0)
			replay, err := f.call(t.Context(), request)
			require.NoError(t, err)
			assert.True(t, proto.Equal(original, replay))
			assert.Zero(t, f.lists.Load())
			changed := proto.CloneOf(request)
			changed.Creation.Presentation = &leapmuxv1.TabPresentation{DisplayMode: leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_SOURCE.Enum()}
			mismatch, err := f.call(t.Context(), changed)
			require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
			assert.Nil(t, mismatch)
			assert.Zero(t, f.lists.Load())
			retained, err := f.base.WorkerTabPublications().Get(t.Context(), key)
			require.NoError(t, err)
			assert.Equal(t, saved, retained)
		})
	}
}

func TestWorkerTabPublication_CatchesUpBeyondOneLifecyclePage(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	prior := storetest.SeedWorkspace(t, f.base, f.owner.String(), "prior-workspace")
	seedPublicationWorkspace(t, f.manager, prior)
	require.NoError(t, f.base.RunInTransaction(t.Context(), func(tx store.Store) error {
		for index := range int(store.CRDTBatchPageLimit) + 1 {
			if err := emitLifecycleOutbox(t.Context(), tx, f.owner.String(), crdt.LifecycleOpRename, crdt.LifecyclePayload{OpType: crdt.LifecycleOpRename, WorkspaceID: prior, NewTitle: fmt.Sprintf("prior-%d", index)}, nil); err != nil {
				return err
			}
		}
		return nil
	}))
	workspaceID := f.createPendingWorkspace(t)
	f.failList.Store(false)
	response, err := f.call(t.Context(), firstLeafPublication(workspaceID, "beyond-page"))
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	assert.Equal(t, f.manager.Materialized(crdt.SubscriberFilter{}).GetWorkspaces()[workspaceID].GetRootNodeId(), response.GetPublished().GetTileId())
	pending, err := f.base.LifecycleOutbox().ListPending(t.Context(), store.ListPendingLifecycleOutboxParams{UserID: f.owner, Limit: store.CRDTBatchPageLimit})
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestWorkerTabPublication_ReceiptReadFailurePrecedesLifecycleCatchUp(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	workspaceID := f.createPendingWorkspace(t)
	f.failList.Store(false)
	f.failReceipt.Store(true)
	f.lists.Store(0)
	request := firstLeafPublication(workspaceID, "receipt-read-failure")
	_, err := f.call(t.Context(), request)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Zero(t, f.lists.Load())
	assert.NotContains(t, f.manager.Materialized(crdt.SubscriberFilter{}).GetWorkspaces(), workspaceID)
	_, err = f.base.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: f.owner, WorkerID: f.workerID, TabID: request.GetCreation().GetTabId()})
	require.ErrorIs(t, err, store.ErrNotFound)
	f.failReceipt.Store(false)
	result, err := f.call(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, result.GetPublished())
}

func TestWorkerTabPublication_ConcurrentCatchUpPublishesBothTabs(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	workspaceID := f.createPendingWorkspace(t)
	f.failList.Store(false)
	f.gate.enabled.Store(true)
	type outcome struct {
		response *leapmuxv1.PublishTabResponse
		err      error
	}
	results := make(chan outcome, 2)
	var callers sync.WaitGroup
	t.Cleanup(func() { f.gate.release(); callers.Wait() })
	for _, id := range []string{"concurrent-first", "concurrent-second"} {
		callers.Go(func() {
			response, err := f.call(t.Context(), firstLeafPublication(workspaceID, id))
			results <- outcome{response, err}
		})
	}
	callers.Wait()
	close(results)
	positions := make(map[string]bool)
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.response.GetPublished())
		positions[result.response.GetPublished().GetPosition()] = true
	}
	assert.Len(t, positions, 2)
	state := f.manager.Materialized(crdt.SubscriberFilter{})
	for _, id := range []string{"concurrent-first", "concurrent-second"} {
		assert.Contains(t, state.GetTabs(), id)
		_, err := f.base.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: f.owner, WorkerID: f.workerID, TabID: id})
		require.NoError(t, err)
	}
}

func (f *publicationLifecycleFixture) addUnrelatedLifecycleFailure(t *testing.T) int64 {
	t.Helper()
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: f.owner})
	other, err := f.workspaces.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Unrelated workspace"})
	require.NoError(t, err)
	require.NoError(t, emitLifecycleOutbox(t.Context(), f.base, f.owner.String(), crdt.LifecycleOpRename, crdt.LifecyclePayload{OpType: crdt.LifecycleOpRename, WorkspaceID: other.GetWorkspaceId(), NewTitle: "Unrelated rename"}, nil))
	rows, err := f.base.LifecycleOutbox().ListPending(t.Context(), store.ListPendingLifecycleOutboxParams{UserID: f.owner, Limit: store.CRDTBatchPageLimit})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	f.failConsumeID.Store(rows[0].ID)
	return rows[0].ID
}

func TestWorkerTabPublication_UnrelatedLifecycleFailureDoesNotBlockDestination(t *testing.T) {
	t.Parallel()
	for _, pendingTarget := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending_target=%t", pendingTarget), func(t *testing.T) {
			t.Parallel()
			f := newPublicationLifecycleFixture(t)
			ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: f.owner})
			var destination string
			if !pendingTarget {
				target, err := f.workspaces.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Ready destination"})
				require.NoError(t, err)
				destination = target.GetWorkspaceId()
			}
			failedID := f.addUnrelatedLifecycleFailure(t)
			if pendingTarget {
				destination = f.createPendingWorkspace(t)
				f.failList.Store(false)
			}
			response, err := f.call(t.Context(), firstLeafPublication(destination, "independent-publication"))
			require.NoError(t, err, "an unrelated workspace failure cannot block a ready canonical destination")
			require.NotNil(t, response.GetPublished())
			pending, err := f.base.LifecycleOutbox().ListPending(t.Context(), store.ListPendingLifecycleOutboxParams{UserID: f.owner, Limit: store.CRDTBatchPageLimit})
			require.NoError(t, err)
			require.Len(t, pending, 1)
			assert.Equal(t, failedID, pending[0].ID)
		})
	}
}

func TestWorkerTabPublication_ProgressesPastUnrelatedFailureAcrossPages(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: f.owner})
	prior, err := f.workspaces.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Independent backlog"})
	require.NoError(t, err)
	failedID := f.addUnrelatedLifecycleFailure(t)
	require.NoError(t, f.base.RunInTransaction(t.Context(), func(tx store.Store) error {
		for index := range int(store.CRDTBatchPageLimit) + 1 {
			if err := emitLifecycleOutbox(t.Context(), tx, f.owner.String(), crdt.LifecycleOpRename, crdt.LifecyclePayload{OpType: crdt.LifecycleOpRename, WorkspaceID: prior.GetWorkspaceId(), NewTitle: fmt.Sprintf("backlog-%d", index)}, nil); err != nil {
				return err
			}
		}
		return nil
	}))
	destination := f.createPendingWorkspace(t)
	f.failList.Store(false)
	request := firstLeafPublication(destination, "pending-behind-failed-row")
	_, err = f.call(t.Context(), request)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	_, err = f.base.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: f.owner, WorkerID: f.workerID, TabID: request.GetCreation().GetTabId()})
	require.ErrorIs(t, err, store.ErrNotFound)
	response, err := f.call(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	assert.Equal(t, request.GetCreation().GetTabId(), response.GetPublished().GetTab().GetTabId())
	pending, err := f.base.LifecycleOutbox().ListPending(t.Context(), store.ListPendingLifecycleOutboxParams{UserID: f.owner, Limit: store.CRDTBatchPageLimit})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, failedID, pending[0].ID)
}

func TestWorkerTabPublication_CanonicalDependencyDoesNotAuthorizeDeletedWorkspace(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: f.owner})
	target, err := f.workspaces.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Deleted destination"})
	require.NoError(t, err)
	f.addUnrelatedLifecycleFailure(t)
	f.failList.Store(true)
	_, err = f.workspaces.DeleteWorkspace(ctx, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: target.GetWorkspaceId()})
	require.NoError(t, err)
	f.failList.Store(false)
	require.Contains(t, f.manager.Materialized(crdt.SubscriberFilter{}).GetWorkspaces(), target.GetWorkspaceId())
	request := firstLeafPublication(target.GetWorkspaceId(), "denied-after-sql-delete")
	response, err := f.call(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, response.GetRejected())
	assert.NotContains(t, f.manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), request.GetCreation().GetTabId())
}

func TestWorkerTabPublication_CanonicalDependencyPreservesArchivedState(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: f.owner})
	target, err := f.workspaces.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Archived destination"})
	require.NoError(t, err)
	archiveID := id.Generate()
	require.NoError(t, f.base.WorkspaceSections().Create(t.Context(), store.CreateWorkspaceSectionParams{
		ID:          archiveID,
		UserID:      f.owner,
		Name:        "Archived",
		Position:    "z",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))
	f.addUnrelatedLifecycleFailure(t)
	_, err = f.workspaces.MoveWorkspace(ctx, &leapmuxv1.MoveWorkspaceRequest{WorkspaceId: target.GetWorkspaceId(), SectionId: archiveID, Position: "n", ExpectedArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE})
	require.NoError(t, err)
	request := firstLeafPublication(target.GetWorkspaceId(), "published-while-archived")
	response, err := f.call(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	resolved, err := f.manager.ResolveTabPublication(t.Context(), f.workerID, request)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, resolved.GetArchiveState())
}

func TestWorkerTabPublication_RPCEnvelopesPreserveReceiptIdentity(t *testing.T) {
	t.Parallel()
	f := newPublicationLifecycleFixture(t)
	workspaceID := f.createPendingWorkspace(t)
	f.failList.Store(false)
	publication := firstLeafPublication(workspaceID, "shared-publication-descriptor")
	published, err := f.call(t.Context(), publication)
	require.NoError(t, err)
	require.NotNil(t, published.GetPublished())
	resolved, err := f.resolvePublication(t.Context(), publication)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED, resolved.GetPresence())
	assert.True(t, proto.Equal(published, resolved.GetReceipt()))
	require.NoError(t, f.cancelPublication(t.Context(), publication))
	resolved, err = f.resolvePublication(t.Context(), publication)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_ABSENT, resolved.GetPresence())
	require.NotNil(t, resolved.GetReceipt().GetRejected())
	replayed, err := f.call(t.Context(), publication)
	require.NoError(t, err)
	assert.True(t, proto.Equal(resolved.GetReceipt(), replayed))
	assert.NotContains(t, f.manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), publication.GetCreation().GetTabId())
}
