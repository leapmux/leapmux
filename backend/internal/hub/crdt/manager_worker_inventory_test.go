package crdt_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerInventory_PreservesConcurrentChanges(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, mgr, "ws", "root")
	for _, tab := range []string{"stale", "reported", "changed", "reassigned", "revived", "foreign"} {
		worker := "worker"
		if tab == "foreign" {
			worker = "other"
		}
		results, err := mgr.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "add-"+tab, tab, "root", worker, "a0")}})
		require.NoError(t, err)
		require.NotNil(t, results[0].GetCommitted())
	}
	snapshot, err := mgr.BeginWorkerTabInventory(t.Context(), "worker")
	require.NoError(t, err)
	snapshot.RecordHostedTabs([]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "reported"}})
	mgr.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.Tabs["changed"].Position = &leapmuxv1.LWWString{Value: "a1", Hlc: &leapmuxv1.HLC{Physical: 2000}}
		state.Tabs["reassigned"].WorkerId = &leapmuxv1.LWWString{Value: "replacement", Hlc: &leapmuxv1.HLC{Physical: 2000}}
		state.Tabs["revived"].RevivedAt = &leapmuxv1.HLC{Physical: 2000}
	})
	results, err := mgr.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "new", "new", "root", "worker", "a0")}})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	require.NoError(t, mgr.FinishWorkerTabInventory(t.Context(), snapshot))
	tabs := mgr.Materialized(crdt.SubscriberFilter{}).GetTabs()
	assert.NotContains(t, tabs, "stale")
	for _, tab := range []string{"reported", "changed", "reassigned", "revived", "foreign", "new"} {
		require.Contains(t, tabs, tab)
		assert.True(t, crdt.HLCIsZero(tabs[tab].GetTombstoneAt()), tab)
	}
	require.Error(t, mgr.FinishWorkerTabInventory(t.Context(), snapshot))
}

func TestWorkerInventory_RefusesFailedCommitsAndWrongOwners(t *testing.T) {
	zeroSnapshot := &crdt.WorkerTabInventorySnapshot{}
	mgr, journal, _ := runManager(t, "user-1", allowAll{}, 1000)
	require.Error(t, mgr.FinishWorkerTabInventory(t.Context(), zeroSnapshot))
	seedRootInternal(t, mgr, "ws", "root")
	results, err := mgr.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "add", "tab", "root", "worker", "a0")}})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	snapshot, err := mgr.BeginWorkerTabInventory(t.Context(), "worker")
	require.NoError(t, err)
	other, _, _ := runManager(t, "user-2", allowAll{}, 1000)
	require.Error(t, other.FinishWorkerTabInventory(t.Context(), snapshot))
	failure := errors.New("inventory commit failure")
	journal.mu.Lock()
	journal.commitErr = failure
	journal.mu.Unlock()
	require.ErrorIs(t, mgr.FinishWorkerTabInventory(t.Context(), snapshot), failure)
	require.Contains(t, mgr.Materialized(crdt.SubscriberFilter{}).GetTabs(), "tab")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = mgr.BeginWorkerTabInventory(ctx, "worker")
	require.ErrorIs(t, err, context.Canceled)
	_, err = mgr.BeginWorkerTabInventory(t.Context(), "")
	require.Error(t, err)
}

func TestWorkerInventory_CompletesAfterRegistryEviction(t *testing.T) {
	journal := newFakeJournal()
	var idle atomic.Bool
	now := func() time.Time {
		if idle.Load() {
			return time.Now().Add(-2 * time.Hour)
		}
		return time.Now()
	}
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		manager := crdt.NewManager(owner, journal, allowAll{}, nil, now)
		return manager, manager.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(time.Hour))
	t.Cleanup(func() { registry.Shutdown(30 * time.Second) })
	original, err := registry.Get(t.Context(), "user-1")
	require.NoError(t, err)
	seedRootInternal(t, original, "ws", "root")
	for _, tabID := range []string{"reported", "stale", "changed"} {
		results, err := original.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "add-"+tabID, tabID, "root", "worker", "a0")}})
		require.NoError(t, err)
		require.NotNil(t, results[0].GetCommitted())
	}
	snapshot, err := original.BeginWorkerTabInventory(t.Context(), "worker")
	require.NoError(t, err)
	snapshot.RecordHostedTabs([]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "reported"}})
	idle.Store(true)
	_, err = original.SubmitInternal(t.Context(), crdt.SubmitInput{})
	require.NoError(t, err)
	registry.SweepIdle()
	idle.Store(false)
	current, err := registry.Get(t.Context(), "user-1")
	require.NoError(t, err)
	require.NotSame(t, original, current)
	results, err := current.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "change", "changed", "root", "other-worker", "a1")}})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	require.NoError(t, current.FinishWorkerTabInventory(t.Context(), snapshot))
	tabs := current.Materialized(crdt.SubscriberFilter{}).GetTabs()
	assert.NotContains(t, tabs, "stale")
	assert.Contains(t, tabs, "reported")
	assert.Contains(t, tabs, "changed")
}
