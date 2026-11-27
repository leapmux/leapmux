package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestReadLayoutEventsBindsAccountAndScopeAndRedactsTheOtherPlacement(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-owner").ID)
	other := userid.MustNew(storetest.SeedUser(t, st, "layout-stranger").ID)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: owner})
	workspace := storetest.SeedWorkspace(t, st, owner.String(), "First")
	second := storetest.SeedWorkspace(t, st, owner.String(), "Second")
	service := NewWorkspaceService(st, nil, nil)
	start, err := service.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: workspace})
	require.NoError(t, err)
	require.Zero(t, start.GetAfterSequence())
	tab := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "script-tab"}
	before := &leapmuxv1.LayoutTabPlacement{WorkspaceId: workspace, TileId: "first-tile", WorkerId: "first-worker"}
	after := &leapmuxv1.LayoutTabPlacement{WorkspaceId: second, TileId: "second-tile", WorkerId: "second-worker"}
	payloads, err := marshalLayoutFacts([]*leapmuxv1.LayoutEventFact{
		{Event: &leapmuxv1.LayoutEventFact_Tab{Tab: &leapmuxv1.LayoutTabChange{
			Tab: tab, Kind: leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED, Before: before, After: after,
		}}},
		{Event: &leapmuxv1.LayoutEventFact_Tab{Tab: &leapmuxv1.LayoutTabChange{
			Tab: tab, Kind: leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_CLOSED, Before: after,
		}}},
	})
	require.NoError(t, err)
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		return appendLayoutEvents(ctx, tx.LayoutEvents(), owner, 100, payloads, "")
	}))

	page, err := service.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspace, AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err)
	require.Len(t, page.GetRecords(), 1)
	move := page.GetRecords()[0].GetFact().GetTab()
	assert.Equal(t, leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED, move.GetKind())
	assert.Equal(t, before.GetWorkspaceId(), move.GetBefore().GetWorkspaceId())
	assert.Nil(t, move.GetAfter(), "the other placement is outside the signed scope")
	assert.Equal(t, int64(2), page.GetScannedSequence(), "skipping the unrelated close still advances")

	for _, tc := range []struct {
		ctx       context.Context
		workspace string
	}{
		{ctx, ""},
		{ctx, second},
		{auth.WithUser(t.Context(), &auth.UserInfo{ID: other}), workspace},
	} {
		_, err := service.ReadLayoutEvents(tc.ctx, &leapmuxv1.ReadLayoutEventsRequest{
			WorkspaceId: tc.workspace, AfterCursor: start.GetCursor(),
		})
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	}
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		return tx.LayoutEvents().PruneThrough(ctx, owner, 1)
	}))
	gap, err := service.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspace, AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err)
	require.NotNil(t, gap.GetGap())
	assert.Equal(t, int64(1), gap.GetGap().GetResumeAfterSequence())
	assert.Empty(t, gap.GetRecords())
	skipped, err := service.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspace, AfterCursor: gap.GetGap().GetCursor(),
	})
	require.NoError(t, err)
	assert.Empty(t, skipped.GetRecords())
	assert.Equal(t, int64(2), skipped.GetScannedSequence())
}

func TestCommitBatchRollsBackTheCanonicalWriteWhenLayoutAppendFails(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-rollback-owner").ID)
	hlc := &leapmuxv1.HLC{Physical: 100, ClientId: crdt.CanonicalClockID}
	journal := &crdtJournal{store: st}
	commit := crdt.CommitBatch{
		UserID: owner.String(), PrincipalID: owner.String(), Epoch: 1,
		Batch: &leapmuxv1.OpBatch{
			BatchId: "layout-rollback",
			Ops:     []*leapmuxv1.CrdtOp{{OpId: "op", CanonicalHlc: hlc}},
		},
		Dedup: crdt.DedupEntry{
			BatchID: "layout-rollback", BodyHash: []byte("hash"),
			CanonicalFirstHLC: hlc, OpCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		},
		LayoutFacts: []*leapmuxv1.LayoutEventFact{{}},
	}
	ctx := t.Context()
	require.ErrorContains(t, journal.CommitBatch(ctx, commit), "append committed layout events")
	count, err := st.UserOpBatches().Count(ctx, owner)
	require.NoError(t, err)
	assert.Zero(t, count)
	_, err = st.UserRecentBatchIDs().Get(ctx, owner, commit.Batch.GetBatchId())
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.LayoutEvents().ReadPage(ctx, owner, "", 0, 10)
	assert.ErrorIs(t, err, store.ErrNotFound)
	commit.LayoutFacts = []*leapmuxv1.LayoutEventFact{
		{Event: &leapmuxv1.LayoutEventFact_Workspace{Workspace: &leapmuxv1.LayoutWorkspaceChange{
			WorkspaceId: "workspace", Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_CREATE, Title: "Created",
		}}},
	}
	require.NoError(t, journal.CommitBatch(ctx, commit))
	page, err := st.LayoutEvents().ReadPage(ctx, owner, "", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.Equal(t, int64(1), page.Rows[0].Sequence)
}

func TestReadLayoutEventsDrainsDurableRenameAndDeleteAfterManagerRestart(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-lifecycle-owner").ID)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: owner})
	factory := func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		return bootLayoutTestManager(ctx, st, owner)
	}
	first := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	stopFirst := sync.OnceFunc(func() { first.Shutdown(2 * time.Second) })
	t.Cleanup(stopFirst)
	creator := NewWorkspaceService(st, first, nil)
	created, err := creator.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Before"})
	require.NoError(t, err)
	workspaceID := created.GetWorkspaceId()
	start, err := creator.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: workspaceID})
	require.NoError(t, err)
	require.Equal(t, int64(1), start.GetAfterSequence())
	stopFirst()

	// These mutations commit their outbox rows, but no manager can consume
	// them until the new reader reconnects.
	offline := NewWorkspaceService(st, nil, nil)
	_, err = offline.RenameWorkspace(ctx, &leapmuxv1.RenameWorkspaceRequest{WorkspaceId: workspaceID, Title: "After"})
	require.NoError(t, err)
	_, err = offline.DeleteWorkspace(ctx, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID})
	require.NoError(t, err)
	history, err := st.LayoutEvents().ReadPage(ctx, owner, "", 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), history.Head.HighSequence)

	restarted := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	t.Cleanup(func() { restarted.Shutdown(2 * time.Second) })
	reader := NewWorkspaceService(st, restarted, nil)
	page, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspaceID, AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err)
	require.Len(t, page.GetRecords(), 2)
	rename, deleted := page.GetRecords()[0].GetFact().GetWorkspace(), page.GetRecords()[1].GetFact().GetWorkspace()
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME, rename.GetOperation())
	assert.Equal(t, "After", rename.GetTitle())
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE, deleted.GetOperation())
	assert.Equal(t, workspaceID, deleted.GetWorkspaceId())
	next, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspaceID, AfterCursor: page.GetScannedCursor(),
	})
	require.NoError(t, err)
	assert.Empty(t, next.GetRecords())
	require.NotNil(t, next.GetEnd())
	assert.Equal(t, int64(3), next.GetEnd().GetFinalSequence())
	_, err = reader.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Unrelated"})
	require.NoError(t, err)
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		return tx.LayoutEvents().PruneThrough(ctx, owner, 4)
	}))
	gap, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspaceID, AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err)
	require.NotNil(t, gap.GetGap())
	assert.Equal(t, int64(3), gap.GetGap().GetResumeAfterSequence())
	end, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspaceID, AfterCursor: gap.GetGap().GetCursor(),
	})
	require.NoError(t, err)
	require.NotNil(t, end.GetEnd())
	assert.Equal(t, int64(3), end.GetEnd().GetFinalSequence())
	_, err = reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: workspaceID})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

type layoutOutboxFaultStore struct {
	store.Store
	failConsume atomic.Bool
}

func (s *layoutOutboxFaultStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(&layoutOutboxFaultTx{Store: tx, failConsume: &s.failConsume})
	})
}

type layoutOutboxFaultTx struct {
	store.Store
	failConsume *atomic.Bool
}

func (s *layoutOutboxFaultTx) LifecycleOutbox() store.LifecycleOutboxStore {
	return layoutOutboxFaultRows{LifecycleOutboxStore: s.Store.LifecycleOutbox(), failConsume: s.failConsume.Load()}
}

func (s *layoutOutboxFaultStore) LifecycleOutbox() store.LifecycleOutboxStore {
	return layoutOutboxFaultRows{LifecycleOutboxStore: s.Store.LifecycleOutbox(), failConsume: s.failConsume.Load()}
}

type layoutOutboxFaultRows struct {
	store.LifecycleOutboxStore
	failConsume bool
}

func (s layoutOutboxFaultRows) MarkConsumed(ctx context.Context, p store.MarkLifecycleOutboxConsumedParams) error {
	if s.failConsume {
		return errors.New("injected outbox acknowledgement failure")
	}
	return s.LifecycleOutboxStore.MarkConsumed(ctx, p)
}

func TestReadLayoutEventsRecoversAfterDeleteCommitAndFailedAcknowledgement(t *testing.T) {
	t.Parallel()
	base := hubtestutil.OpenTestStore(t)
	st := &layoutOutboxFaultStore{Store: base}
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-delete-retry").ID)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: owner})
	factory := func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		return bootLayoutTestManager(ctx, st, owner)
	}
	first := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	stopFirst := sync.OnceFunc(func() { first.Shutdown(2 * time.Second) })
	t.Cleanup(stopFirst)
	creator := NewWorkspaceService(st, first, nil)
	created, err := creator.CreateWorkspace(ctx, &leapmuxv1.CreateWorkspaceRequest{Title: "Before failure"})
	require.NoError(t, err)
	workspaceID := created.GetWorkspaceId()
	start, err := creator.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: workspaceID})
	require.NoError(t, err)
	st.failConsume.Store(true)
	_, err = creator.DeleteWorkspace(ctx, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID})
	require.NoError(t, err)

	list := store.ListPendingLifecycleOutboxParams{UserID: owner, Limit: 10}
	pending, err := st.LifecycleOutbox().ListPending(ctx, list)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE, pending[0].OpType)
	assert.True(t, pending[0].LayoutFactWritten)
	page, err := creator.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspaceID, AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err, "a pending acknowledgement cannot hide the committed deletion")
	require.Len(t, page.GetRecords(), 1)
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE, page.GetRecords()[0].GetFact().GetWorkspace().GetOperation())

	stopFirst()
	st.failConsume.Store(false)
	restarted := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	t.Cleanup(func() { restarted.Shutdown(2 * time.Second) })
	reader := NewWorkspaceService(st, restarted, nil)
	account, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{})
	require.NoError(t, err)
	next, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{AfterCursor: account.GetCursor()})
	require.NoError(t, err, "recovery should acknowledge the committed batch")
	assert.Empty(t, next.GetRecords())
	assert.Equal(t, account.GetAfterSequence(), next.GetScannedSequence())
	pending, err = st.LifecycleOutbox().ListPending(ctx, list)
	require.NoError(t, err)
	assert.Empty(t, pending)

	final, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspaceID, AfterCursor: page.GetScannedCursor(),
	})
	require.NoError(t, err)
	require.NotNil(t, final.GetEnd())
	assert.Equal(t, page.GetScannedSequence(), final.GetEnd().GetFinalSequence())
}

func TestReadLayoutEventsSchedulesCatchUpWhilePagesAdvance(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-busy-account").ID)
	ctx := auth.WithUser(testutil.DeadlineContext(t), &auth.UserInfo{ID: owner})
	watched := storetest.SeedWorkspace(t, st, owner.String(), "Watched")
	unrelated := storetest.SeedWorkspace(t, st, owner.String(), "Unrelated")
	offline := NewWorkspaceService(st, nil, nil)
	start, err := offline.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: watched})
	require.NoError(t, err)
	_, err = offline.RenameWorkspace(ctx, &leapmuxv1.RenameWorkspaceRequest{WorkspaceId: watched, Title: "Renamed"})
	require.NoError(t, err)
	facts := make([]*leapmuxv1.LayoutEventFact, layoutEventPageLimit+1)
	for i := range facts {
		facts[i] = workspaceRenameLayoutFact(unrelated, "Unrelated edit")
	}
	payloads, err := marshalLayoutFacts(facts)
	require.NoError(t, err)
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		return appendLayoutEvents(ctx, tx.LayoutEvents(), owner, 100, payloads, "")
	}))

	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return bootLayoutTestManager(ctx, st, owner)
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	t.Cleanup(unblock)
	reader := NewWorkspaceService(st, registry, nil)
	page, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: watched, AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err, "committed pages must return while catch-up runs")
	require.Equal(t, int64(layoutEventPageLimit), page.GetScannedSequence())
	assert.Empty(t, page.GetRecords())
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("committed page delivery did not schedule lifecycle catch-up")
	}
	draining := registry.StartLifecycleDrain(owner.String())
	for range 64 {
		require.Equal(t, draining, registry.StartLifecycleDrain(owner.String()),
			"all live requests for an account must share one completion signal")
	}
	unblock()
	for len(page.GetRecords()) == 0 {
		page, err = reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
			WorkspaceId: watched, AfterCursor: page.GetScannedCursor(),
		})
		require.NoError(t, err)
	}
	require.Len(t, page.GetRecords(), 1)
	assert.Equal(t, "Renamed", page.GetRecords()[0].GetFact().GetWorkspace().GetTitle())
}

func TestReadLayoutEventsRetainsLossOfAnUnreadableLifecycleRow(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-unreadable-outbox").ID)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: owner})
	workspace := storetest.SeedWorkspace(t, st, owner.String(), "Watched")
	offline := NewWorkspaceService(st, nil, nil)
	account, err := offline.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{})
	require.NoError(t, err)
	scoped, err := offline.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: workspace})
	require.NoError(t, err)
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		if _, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: workspace, OwnerUserID: owner}); err != nil {
			return err
		}
		return tx.LifecycleOutbox().Insert(ctx, store.InsertLifecycleOutboxParams{
			UserID: owner, OpType: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE, Payload: []byte("{"),
		})
	}))
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		return bootLayoutTestManager(ctx, st, owner)
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	reader := NewWorkspaceService(st, registry, nil)
	accountPage, err := reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{AfterCursor: account.GetCursor()})
	require.NoError(t, err)
	require.Len(t, accountPage.GetRecords(), 1)
	assert.NotNil(t, accountPage.GetRecords()[0].GetFact().GetLoss())
	savedPage, err := offline.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspace, AfterCursor: scoped.GetCursor(),
	})
	require.NoError(t, err)
	require.Len(t, savedPage.GetRecords(), 1, "a separate client must see the loss after consumption")
	assert.NotNil(t, savedPage.GetRecords()[0].GetFact().GetLoss())
	assert.Equal(t, accountPage.GetRecords()[0].GetSequence(), savedPage.GetRecords()[0].GetSequence())
	_, err = reader.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: workspace, AfterCursor: savedPage.GetScannedCursor(),
	})
	require.Error(t, err, "a deleted scope with no provable end cannot report that it is still watching")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.ErrorContains(t, err, "cannot establish the final layout position")
}

func workspaceRenameLayoutFact(workspaceID, title string) *leapmuxv1.LayoutEventFact {
	return &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{Workspace: &leapmuxv1.LayoutWorkspaceChange{
		WorkspaceId: workspaceID, Title: title, Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME,
	}}}
}

func bootLayoutTestManager(ctx context.Context, st store.Store, owner userid.UserID) (*crdt.Manager, error) {
	manager := crdt.NewManager(owner, NewCRDTJournal(st), NewCRDTAuthChecker(st), nil, time.Now)
	if err := manager.Bootstrap(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func TestReadLayoutEventsSkipsLifecycleDrainWhenNoOutboxPending(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-idle-owner").ID)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{ID: owner})

	var boots atomic.Int32
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		boots.Add(1)
		return bootLayoutTestManager(ctx, st, owner)
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	svc := NewWorkspaceService(st, registry, nil)
	start, err := svc.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{})
	require.NoError(t, err)

	page, err := svc.ReadLayoutEvents(ctx, &leapmuxv1.ReadLayoutEventsRequest{
		AfterCursor: start.GetCursor(),
	})
	require.NoError(t, err)
	assert.Empty(t, page.GetRecords())
	assert.Zero(t, boots.Load(), "idle layout poll with no pending lifecycle outbox must not bootstrap CRDT manager")
}

func TestRegistryShutdownCancelsAndJoinsLifecycleDrains(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-shutdown-owner").ID)

	entered := make(chan struct{})
	exited := make(chan struct{})
	registry := crdt.NewRegistry(func(workCtx context.Context, _ userid.UserID) (*crdt.Manager, error) {
		close(entered)
		<-workCtx.Done()
		close(exited)
		return nil, workCtx.Err()
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(NewLifecycleOutboxReader(st)))

	_ = registry.StartLifecycleDrain(owner.String())
	<-entered

	registry.Shutdown(2 * time.Second)

	select {
	case <-exited:
	default:
		t.Fatal("Registry.Shutdown returned before the cancelled lifecycle drain goroutine exited")
	}
}

func TestConsumeLifecycleOutboxWithLayoutFactRejectsNilFact(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	owner := userid.MustNew(storetest.SeedUser(t, st, "layout-nil-fact-owner").ID)
	reader := NewLifecycleOutboxReader(st)
	err := reader.ConsumeLifecycleOutboxWithLayoutFact(t.Context(), crdt.LifecycleOutboxRow{
		ID: 1, UserID: owner.String(),
	}, time.Now(), nil)
	require.ErrorContains(t, err, "layout event fact is required")
}

func TestRequireScopesFailsClosedOnEmptyScopes(t *testing.T) {
	t.Parallel()
	unscopedUser := &auth.UserInfo{ID: userid.MustNew("user-unscoped")}
	err := requireScopes(unscopedUser)
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrHTTPForbidden)
	assert.ErrorContains(t, err, "no required scopes declared")
}

func TestLayoutEventPageResultCaughtUp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		result      layoutEventPageResult
		workspaceID string
		want        bool
	}{
		{
			name:        "nil response",
			result:      layoutEventPageResult{workspaceLive: true},
			workspaceID: "",
			want:        false,
		},
		{
			name: "non-nil gap",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   5,
					HighWaterSequence: 5,
					Gap:               &leapmuxv1.LayoutEventGap{ResumeAfterSequence: 5},
				},
				workspaceLive: true,
			},
			workspaceID: "ws-1",
			want:        false,
		},
		{
			name: "non-nil end",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   5,
					HighWaterSequence: 5,
					End:               &leapmuxv1.LayoutEventEnd{FinalSequence: 5},
				},
				workspaceLive: true,
			},
			workspaceID: "ws-1",
			want:        false,
		},
		{
			name: "owner lifecycle pending",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   5,
					HighWaterSequence: 5,
				},
				ownerLifecyclePending: true,
				workspaceLive:         true,
			},
			workspaceID: "ws-1",
			want:        false,
		},
		{
			name: "scanned sequence below high water",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   4,
					HighWaterSequence: 5,
				},
				workspaceLive: true,
			},
			workspaceID: "ws-1",
			want:        false,
		},
		{
			name: "account scope does not require workspaceLive",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   5,
					HighWaterSequence: 5,
				},
				workspaceLive: false,
			},
			workspaceID: "",
			want:        true,
		},
		{
			name: "workspace scope requires workspaceLive",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   5,
					HighWaterSequence: 5,
				},
				workspaceLive: false,
			},
			workspaceID: "ws-1",
			want:        false,
		},
		{
			name: "caught-up live workspace page",
			result: layoutEventPageResult{
				response: &leapmuxv1.ReadLayoutEventsResponse{
					ScannedSequence:   5,
					HighWaterSequence: 5,
				},
				workspaceLive: true,
			},
			workspaceID: "ws-1",
			want:        true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.result.caughtUp(tc.workspaceID))
		})
	}
}
