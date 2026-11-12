package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

var errPublicationInsert = errors.New("publication receipt insert failed")

type publicationFailureStore struct {
	store.Store
	fail *atomic.Bool
}

func (s publicationFailureStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error { return fn(publicationFailureStore{Store: tx, fail: s.fail}) })
}
func (s publicationFailureStore) WorkerTabPublications() store.WorkerTabPublicationStore {
	return publicationFailureReceipts{WorkerTabPublicationStore: s.Store.WorkerTabPublications(), fail: s.fail}
}

type publicationFailureReceipts struct {
	store.WorkerTabPublicationStore
	fail *atomic.Bool
}

func (s publicationFailureReceipts) Insert(ctx context.Context, p store.InsertWorkerTabPublicationParams) error {
	if s.fail.Load() {
		return errPublicationInsert
	}
	return s.WorkerTabPublicationStore.Insert(ctx, p)
}

func seedPublicationWorkspace(t *testing.T, manager *crdt.Manager, workspaceID string) {
	t.Helper()
	results, err := manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{{BatchId: "seed-" + workspaceID, Ops: []*leapmuxv1.CrdtOp{
		{OpId: "root-" + workspaceID, Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{NodeId: "root", Field: &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF}}}},
		{OpId: "workspace-" + workspaceID, Body: &leapmuxv1.CrdtOp_CreateWorkspace{CreateWorkspace: &leapmuxv1.CreateWorkspaceOp{WorkspaceId: workspaceID, RootNodeId: "root"}}},
	}}}})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
}

func publicationRegistry(t *testing.T, st store.Store) *crdt.Registry {
	t.Helper()
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		manager := crdt.NewManager(owner, NewCRDTJournal(st), NewCRDTAuthChecker(st), nil, time.Now)
		if err := manager.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return manager, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(30 * time.Second) })
	return registry
}

func storedPublicationRequest(workspaceID, tabID string) *leapmuxv1.TabPublication {
	return &leapmuxv1.TabPublication{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, Creation: &leapmuxv1.TabCreation{TabId: tabID, Placement: &leapmuxv1.TabPlacement{WorkspaceId: workspaceID, Destination: &leapmuxv1.TabPlacement_TileId{TileId: "root"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}}
}

func TestWorkerTabPublication_ReceiptAndCRDTCommitAtomically(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "publication-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	workspace := storetest.SeedWorkspace(t, st, owner.ID, "publication")
	var fail atomic.Bool
	wrapped := publicationFailureStore{Store: st, fail: &fail}
	registry := publicationRegistry(t, wrapped)
	manager, err := registry.Get(t.Context(), owner.ID)
	require.NoError(t, err)
	seedPublicationWorkspace(t, manager, workspace)
	sibling := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "sibling"}
	seeded, err := manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{{BatchId: "seed-sibling", Ops: []*leapmuxv1.CrdtOp{
		{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: sibling, Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root"}}}},
		{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: sibling, Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "a"}}}},
		{Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: sibling, Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: worker.ID}}}},
	}}}})
	require.NoError(t, err)
	require.NotNil(t, seeded[0].GetCommitted())
	_, before, err := NewCRDTJournal(st).LoadState(t.Context(), owner.ID)
	require.NoError(t, err)
	request := storedPublicationRequest(workspace, "created")
	request.Creation.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_FIRST
	fail.Store(true)
	response, err := manager.PublishTab(t.Context(), worker.ID, request)
	require.ErrorIs(t, err, errPublicationInsert)
	assert.Nil(t, response)
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "created")
	assert.Equal(t, "a", manager.Materialized(crdt.SubscriberFilter{}).GetTabs()["sibling"].GetPosition().GetValue(), "a failed receipt must not normalize sibling state")
	oldSibling, err := st.WorkspaceTabIndex().GetOwned(t.Context(), store.GetOwnedTabParams{UserID: userid.MustNew(owner.ID), TabID: "sibling"})
	require.NoError(t, err)
	assert.Equal(t, "a", oldSibling.Position)

	_, err = st.WorkspaceTabIndex().GetOwned(t.Context(), store.GetOwnedTabParams{UserID: userid.MustNew(owner.ID), TabID: "created"})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: userid.MustNew(owner.ID), WorkerID: worker.ID, TabID: "created"})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, after, err := NewCRDTJournal(st).LoadState(t.Context(), owner.ID)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "the batch also rolls back when the receipt fails")
	fail.Store(false)
	response, err = manager.PublishTab(t.Context(), worker.ID, request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	registry.Shutdown(30 * time.Second)
	restarted := publicationRegistry(t, st)
	loaded, err := restarted.Get(t.Context(), owner.ID)
	require.NoError(t, err)
	replay, err := loaded.PublishTab(t.Context(), worker.ID, request)
	require.NoError(t, err)
	assert.True(t, proto.Equal(response, replay))
	assert.Contains(t, loaded.Materialized(crdt.SubscriberFilter{}).GetTabs(), "created")
}

func TestWorkerTabPublication_AuthenticatesFirstTabWithoutDelegation(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "first-tab-owner")
	worker, workerToken := hubtestutil.CreateTestWorker(t, st, owner.ID)
	workspace := storetest.SeedWorkspace(t, st, owner.ID, "first-tab")
	registry := publicationRegistry(t, st)
	manager, err := registry.Get(t.Context(), owner.ID)
	require.NoError(t, err)
	seedPublicationWorkspace(t, manager, workspace)
	svc := &WorkerConnectorService{
		hasher: hubtestutil.TestSecretHasher(),
		store:  st, crdtRegistry: registry}
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, svc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(4<<20))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	client := workerhub.NewConnectorClient(httpServer.Client(), httpServer.URL)
	request := storedPublicationRequest(workspace, "first")
	_, err = client.PublishTab(t.Context(), &leapmuxv1.PublishTabRequest{Publication: request})
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	ctx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Authorization", "Bearer "+workerToken)
	response, err := client.PublishTab(ctx, &leapmuxv1.PublishTabRequest{Publication: request})
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	assert.Equal(t, "first", response.GetPublished().GetTab().GetTabId())
	invalid := storedPublicationRequest(workspace, "invalid ")
	_, err = client.PublishTab(ctx, &leapmuxv1.PublishTabRequest{Publication: invalid})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = st.WorkerTabPublications().Get(t.Context(), store.WorkerTabPublicationKey{UserID: userid.MustNew(owner.ID), WorkerID: worker.ID, TabID: "invalid "})
	require.ErrorIs(t, err, store.ErrNotFound)

	replay, err := client.PublishTab(ctx, &leapmuxv1.PublishTabRequest{Publication: request})
	require.NoError(t, err)
	assert.True(t, proto.Equal(response, replay))
	other := storetest.SeedUser(t, st, "foreign-publication-owner")
	foreignWorkspace := storetest.SeedWorkspace(t, st, other.ID, "foreign")
	denied, err := client.PublishTab(ctx, &leapmuxv1.PublishTabRequest{Publication: storedPublicationRequest(foreignWorkspace, "foreign")})
	require.NoError(t, err)
	require.NotNil(t, denied.GetRejected())
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "foreign")
}
