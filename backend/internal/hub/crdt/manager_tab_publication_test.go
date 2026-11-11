package crdt_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func publicationRequest(tabID string) *leapmuxv1.TabPublication {
	return &leapmuxv1.TabPublication{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, Creation: &leapmuxv1.TabCreation{TabId: tabID, Placement: &leapmuxv1.TabPlacement{WorkspaceId: "ws", Destination: &leapmuxv1.TabPlacement_TileId{TileId: "root"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}}
}

func TestPublishTab_ReplayDoesNotMoveOrRevive(t *testing.T) {
	manager, _, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, manager, "ws", "root")
	request := publicationRequest("tab")
	first, err := manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	require.NotNil(t, first.GetPublished())
	results, err := manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "move", "tab", "root", "worker", "z")}})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	again, err := manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	assert.True(t, proto.Equal(first, again))
	assert.Equal(t, "z", manager.Materialized(crdt.SubscriberFilter{}).GetTabs()["tab"].GetPosition().GetValue())
	changed := proto.CloneOf(request)
	changed.Creation.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_FIRST
	_, err = manager.PublishTab(t.Context(), "worker", changed)
	require.Error(t, err)
	_, err = manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{{BatchId: "close", Ops: []*leapmuxv1.CrdtOp{{OpId: "close-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: first.GetPublished().GetTab()}}}}}}})
	require.NoError(t, err)
	again, err = manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	assert.True(t, proto.Equal(first, again))
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "tab")
}

func TestPublishTab_CommitAndRejectionFailuresRemainRetryable(t *testing.T) {
	manager, journal, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, manager, "ws", "root")
	failure := errors.New("publication storage failed")
	journal.mu.Lock()
	journal.commitErr = failure
	journal.mu.Unlock()
	response, err := manager.PublishTab(t.Context(), "worker", publicationRequest("tab"))
	require.ErrorIs(t, err, failure)
	assert.Nil(t, response)
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "tab")
	journal.mu.Lock()
	journal.commitErr = nil
	journal.mu.Unlock()
	response, err = manager.PublishTab(t.Context(), "worker", publicationRequest("tab"))
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	invalid := publicationRequest("invalid")
	invalid.Creation.Placement.Destination = &leapmuxv1.TabPlacement_TileId{TileId: "missing"}
	journal.mu.Lock()
	journal.commitErr = failure
	journal.mu.Unlock()
	response, err = manager.PublishTab(t.Context(), "worker", invalid)
	require.ErrorIs(t, err, failure)
	assert.Nil(t, response)
	journal.mu.Lock()
	journal.commitErr = nil
	journal.mu.Unlock()
	response, err = manager.PublishTab(t.Context(), "worker", invalid)
	require.NoError(t, err)
	require.NotNil(t, response.GetRejected())
	manager.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.Nodes["missing"] = &leapmuxv1.NodeRecord{NodeId: "missing", ParentId: "root", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}}
	})
	repeated, err := manager.PublishTab(t.Context(), "worker", invalid)
	require.NoError(t, err)
	assert.True(t, proto.Equal(response, repeated))
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "invalid")
}

func TestPublishTab_ResolvesInsertionAndSerializesConcurrentCalls(t *testing.T) {
	manager, _, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, manager, "ws", "root")
	last, err := manager.PublishTab(t.Context(), "worker", publicationRequest("last"))
	require.NoError(t, err)
	require.NotNil(t, last.GetPublished())
	firstRequest := publicationRequest("first")
	firstRequest.Creation.Placement.Destination = &leapmuxv1.TabPlacement_FirstLeaf{FirstLeaf: &emptypb.Empty{}}
	firstRequest.Creation.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_FIRST
	first, err := manager.PublishTab(t.Context(), "worker", firstRequest)
	require.NoError(t, err)
	require.NotNil(t, first.GetPublished())
	assert.Less(t, first.GetPublished().GetPosition(), last.GetPublished().GetPosition())
	middleRequest := publicationRequest("middle")
	middleRequest.Creation.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_AFTER
	middleRequest.Creation.Placement.RelativeTabId = "first"
	var wg sync.WaitGroup
	responses := make(chan *leapmuxv1.PublishTabResponse, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			response, err := manager.PublishTab(t.Context(), "worker", middleRequest)
			responses <- response
			errs <- err
		})
	}
	wg.Wait()
	close(responses)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var middle *leapmuxv1.PublishTabResponse
	for response := range responses {
		require.NotNil(t, response.GetPublished())
		if middle == nil {
			middle = response
		} else {
			assert.True(t, proto.Equal(middle, response))
		}
	}
	assert.Greater(t, middle.GetPublished().GetPosition(), first.GetPublished().GetPosition())
	assert.Less(t, middle.GetPublished().GetPosition(), last.GetPublished().GetPosition())
	assert.Len(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), 3)
}

type publicationAuth struct{ workspace, worker bool }

func (a publicationAuth) CanAccessWorkspace(context.Context, string, string) (bool, error) {
	return a.workspace, nil
}
func (a publicationAuth) CanUseWorker(context.Context, string, string) (bool, error) {
	return a.worker, nil
}
func TestPublishTab_KeepsCanonicalAuthorization(t *testing.T) {
	for _, auth := range []publicationAuth{{false, true}, {true, false}} {
		t.Run(fmt.Sprintf("workspace_%t_worker_%t", auth.workspace, auth.worker), func(t *testing.T) {
			manager, _, _ := runManager(t, "user-1", auth, 1000)
			seedRootInternal(t, manager, "ws", "root")
			response, err := manager.PublishTab(t.Context(), "worker", publicationRequest("denied"))
			require.NoError(t, err)
			require.NotNil(t, response.GetRejected())
			assert.Empty(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs())
		})
	}
}

func TestPublishTab_PreservesInsertionWhenRanksNeedRebalancing(t *testing.T) {
	cases := []struct {
		name           string
		ids, positions []string
		insertion      leapmuxv1.TabInsertion
		reference      string
		want           []string
	}{
		{"first before minimum", []string{"a"}, []string{"a"}, leapmuxv1.TabInsertion_TAB_INSERTION_FIRST, "", []string{"new", "a"}},
		{"before minimum", []string{"a"}, []string{"a"}, leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, "a", []string{"new", "a"}},
		{"between prefix ranks", []string{"a", "b"}, []string{"n", "na"}, leapmuxv1.TabInsertion_TAB_INSERTION_AFTER, "a", []string{"a", "new", "b"}},
		{"before second tied rank", []string{"a", "b"}, []string{"n", "n"}, leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, "b", []string{"a", "new", "b"}},
		{"after first tied rank", []string{"a", "b"}, []string{"n", "n"}, leapmuxv1.TabInsertion_TAB_INSERTION_AFTER, "a", []string{"a", "new", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, _ := runManager(t, "user-1", allowAll{}, 1000)
			seedRootInternal(t, manager, "ws", "root")
			for i, id := range tc.ids {
				worker := "worker"
				if i == 1 {
					worker = "other-worker"
				}
				results, err := manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "seed-"+id, id, "root", worker, tc.positions[i])}})
				require.NoError(t, err)
				require.NotNil(t, results[0].GetCommitted())
			}
			request := publicationRequest("new")
			request.Creation.Placement.Insertion = tc.insertion
			request.Creation.Placement.RelativeTabId = tc.reference
			response, err := manager.PublishTab(t.Context(), "worker", request)
			require.NoError(t, err)
			require.NotNil(t, response.GetPublished())
			ordered := func() []string {
				tabs := manager.Materialized(crdt.SubscriberFilter{}).GetTabs()
				ids := make([]string, 0, len(tabs))
				for id := range tabs {
					ids = append(ids, id)
				}
				slices.SortFunc(ids, func(a, b string) int {
					if c := strings.Compare(tabs[a].GetPosition().GetValue(), tabs[b].GetPosition().GetValue()); c != 0 {
						return c
					}
					return strings.Compare(a, b)
				})
				return ids
			}
			assert.Equal(t, tc.want, ordered())
			before := manager.Materialized(crdt.SubscriberFilter{})
			replay, err := manager.PublishTab(t.Context(), "worker", request)
			require.NoError(t, err)
			assert.True(t, proto.Equal(response, replay))
			assert.True(t, proto.Equal(before, manager.Materialized(crdt.SubscriberFilter{})), "replay must not rebalance again")
		})
	}
}

func TestCancelTabPublication_PreventsLatePublishAndRemovesCommittedTab(t *testing.T) {
	for _, publishFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("published_%t", publishFirst), func(t *testing.T) {
			manager, _, _ := runManager(t, "user-1", allowAll{}, 1000)
			seedRootInternal(t, manager, "ws", "root")
			request := publicationRequest("canceled")
			if publishFirst {
				response, err := manager.PublishTab(t.Context(), "worker", request)
				require.NoError(t, err)
				require.NotNil(t, response.GetPublished())
			}
			require.NoError(t, manager.CancelTabPublication(t.Context(), "worker", request))
			require.NoError(t, manager.CancelTabPublication(t.Context(), "worker", request))
			response, err := manager.PublishTab(t.Context(), "worker", request)
			require.NoError(t, err)
			require.NotNil(t, response.GetRejected())
			assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "canceled")
		})
	}
}

func TestCancelTabPublication_PreservesTabOnMismatchAndStorageFailure(t *testing.T) {
	manager, journal, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, manager, "ws", "root")
	request := publicationRequest("protected")
	response, err := manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	changed := proto.CloneOf(request)
	changed.Creation.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_FIRST
	require.Error(t, manager.CancelTabPublication(t.Context(), "worker", changed))
	assert.Contains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "protected")
	failure := errors.New("cancellation storage failed")
	journal.mu.Lock()
	journal.commitErr = failure
	journal.mu.Unlock()
	require.ErrorIs(t, manager.CancelTabPublication(t.Context(), "worker", request), failure)
	assert.Contains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "protected")
	journal.mu.Lock()
	journal.commitErr = nil
	journal.mu.Unlock()
	response, err = manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	require.NotNil(t, response.GetPublished())
	require.NoError(t, manager.CancelTabPublication(t.Context(), "worker", request))
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "protected")
}

func TestResolveTabPublication_PreservesReceiptAfterClose(t *testing.T) {
	manager, _, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, manager, "ws", "root")
	request := publicationRequest("resolved")
	published, err := manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	current, err := manager.ResolveTabPublication(t.Context(), "worker", request)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED, current.GetPresence())
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, current.GetArchiveState())
	assert.True(t, proto.Equal(published, current.GetReceipt()))
	_, err = manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{{BatchId: "resolve-close", Ops: []*leapmuxv1.CrdtOp{{OpId: "resolve-close-op", Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: published.GetPublished().GetTab()}}}}}}})
	require.NoError(t, err)
	current, err = manager.ResolveTabPublication(t.Context(), "worker", request)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_ABSENT, current.GetPresence())
	assert.True(t, proto.Equal(published, current.GetReceipt()), "a current close does not rewrite creation proof")
	changed := proto.CloneOf(request)
	changed.Creation.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_FIRST
	_, err = manager.ResolveTabPublication(t.Context(), "worker", changed)
	require.Error(t, err)
}

func TestCancelTabPublication_PreservesReassignedCanonicalTab(t *testing.T) {
	manager, _, _ := runManager(t, "user-1", allowAll{}, 1000)
	seedRootInternal(t, manager, "ws", "root")
	request := publicationRequest("reassigned")
	result, err := manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	require.NotNil(t, result.GetPublished())
	results, err := manager.SubmitInternal(t.Context(), crdt.SubmitInput{Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "reassign-worker", "reassigned", "root", "replacement-worker", "n")}})
	require.NoError(t, err)
	require.NotNil(t, results[0].GetCommitted())
	current, err := manager.ResolveTabPublication(t.Context(), "worker", request)
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_REASSIGNED, current.GetPresence())
	require.NoError(t, manager.CancelTabPublication(t.Context(), "worker", request), "cancel only the old Worker-scoped creation")
	tab := manager.Materialized(crdt.SubscriberFilter{}).GetTabs()["reassigned"]
	require.NotNil(t, tab)
	assert.Equal(t, "replacement-worker", tab.GetWorkerId().GetValue())
	replay, err := manager.PublishTab(t.Context(), "worker", request)
	require.NoError(t, err)
	require.NotNil(t, replay.GetRejected())
}
