package service

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildInputReadinessRetainsItsOriginalNativeSource(t *testing.T) {
	for _, replacement := range []string{"none", "root native", "fresh same root", "root A B A", "child native", "root physical"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			h, _, root, _ := newCapturedPlanTest(t)
			childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "ready-child", Title: "Child"})
			require.NoError(t, err)
			child := root.ChildSink(childID)
			captured := agent.CaptureTranscript(child, agent.MessageContent{}, agent.SpanInfo{})
			owner := captured.FrozenContent().Publication.Owner().(*transcriptOwner)
			source, err := owner.sink.capturedNativeSessionSource(captured)
			require.NoError(t, err)
			switch replacement {
			case "root native":
				root.UpdateSessionID("replacement")
			case "fresh same root":
				root.AdoptNativeSession("native")
			case "root A B A":
				root.UpdateSessionID("replacement")
				root.UpdateSessionID("native")
			case "child native":
				child.AdoptNativeSession("child-native")
			case "root physical":
				_, err = h.NewRootSink("plan-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				require.NoError(t, err)
			}
			lease := (inputReadinessAdmission{source: source}).Acquire()
			if replacement == "none" {
				require.NotNil(t, lease)
				lease.Release()
				lease.Release()
			} else {
				assert.Nil(t, lease)
			}
			// An expired source retires quietly; malformed or foreign captures fail.
			svc := &Service{Output: h}
			require.NoError(t, svc.childInputReady(childID)(captured))
			require.Error(t, svc.childInputReady("another-child")(captured))
			require.Error(t, svc.childInputReady(childID)(agent.CapturedTranscript{}))
		})
	}
}

type readinessDispatcher struct {
	inputqueue.Dispatcher
	calls chan string
}

func (dispatcher readinessDispatcher) SteerPolicy(string) agent.SteerPolicy {
	return agent.SteerPolicy{}
}
func (dispatcher readinessDispatcher) Dispatch(item inputqueue.DispatchItem) (inputqueue.DispatchResult, error) {
	dispatcher.calls <- item.AgentID
	return inputqueue.DispatchResult{StartsTurn: true}, nil
}

func TestChildInputReadinessSchedulesOnlyItsChildQueue(t *testing.T) {
	t.Parallel()
	h, _, root, _ := newCapturedPlanTest(t)
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "ready-child", Title: "Child"})
	require.NoError(t, err)
	siblingID, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "sibling", Title: "Sibling"})
	require.NoError(t, err)
	store := inputqueue.NewStore(h.db, nil)
	calls := make(chan string, 3)
	queue := inputqueue.NewManager(store, readinessDispatcher{calls: calls}, inputqueue.NopObserver{})
	t.Cleanup(queue.StopAndWait)
	svc := &Service{Output: h, InputQueue: queue}
	for _, agentID := range []string{"plan-owner", childID, siblingID} {
		_, err := store.Enqueue(t.Context(), inputqueue.NewItem{ID: agentID + "-input", AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued"})
		require.NoError(t, err)
	}
	captured := agent.CaptureTranscript(root.ChildSink(childID), agent.MessageContent{}, agent.SpanInfo{})
	require.NoError(t, svc.childInputReady(childID)(captured))
	ctx := testutil.DeadlineContext(t)
	select {
	case dispatched := <-calls:
		assert.Equal(t, childID, dispatched)
	case <-ctx.Done():
		t.Fatal("the readiness notification did not dispatch the child input")
	}
	queue.StopAndWait()
	assert.Empty(t, calls)
	for _, agentID := range []string{"plan-owner", siblingID} {
		snapshot, err := store.Snapshot(t.Context(), agentID)
		require.NoError(t, err)
		assert.Len(t, snapshot.Items, 1)
		assert.False(t, snapshot.ActiveTurn)
	}
}
