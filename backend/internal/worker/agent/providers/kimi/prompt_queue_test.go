package kimi

import (
	"fmt"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kimiPendingQueueDispatcher struct {
	inputqueue.Dispatcher
	provider *Agent
	key      string
	ready    func(agent.CapturedTranscript) error
	attempts chan error
}

func (dispatcher *kimiPendingQueueDispatcher) SteerPolicy(string) agent.SteerPolicy {
	return agent.SteerPolicy{}
}
func (dispatcher *kimiPendingQueueDispatcher) Dispatch(item inputqueue.DispatchItem) (inputqueue.DispatchResult, error) {
	result, err := dispatcher.provider.SendChildInput(dispatcher.key, agent.ChildInput{Content: item.Text, Ready: dispatcher.ready})
	dispatcher.attempts <- err
	if err != nil {
		return inputqueue.DispatchResult{}, &inputqueue.DeliveryError{Err: err, Outcome: inputqueue.DispatchPending}
	}
	return inputqueue.DispatchResult{StartsTurn: result.StartsTurn}, nil
}

func TestKimiPendingChildInputResumesRealQueueAfterUnrelatedTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiServiceTestRig(t)
	provider, fake, root, database := rig.agent, rig.fake, rig.sink, rig.database
	native := &kimiOutputRig{agent: provider}
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
	child, linked := provider.children.get("agent-0")
	require.True(t, linked)
	fake.reply("POST "+kimiSessionPath("session_1", "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	store := inputqueue.NewStore(database, nil)
	dispatcher := &kimiPendingQueueDispatcher{provider: provider, key: "session_1/agent-0", attempts: make(chan error, 2)}
	queue := inputqueue.NewManager(store, dispatcher, inputqueue.NopObserver{})
	t.Cleanup(queue.StopAndWait)
	// PlanWriterFor uses the real captured native-source validator. This checks
	// actual child registration expiry without adding a state or transcript row.
	dispatcher.ready = func(captured agent.CapturedTranscript) error {
		_, err := root.ChildSink(child.childAgentID).PlanWriterFor(captured)
		if err == nil {
			queue.NotifyDependencyReady(child.childAgentID)
		}
		return nil
	}
	result, err := provider.SendChildInput(dispatcher.key, agent.ChildInput{Content: "Pending P", Ready: dispatcher.ready})
	require.NoError(t, err)
	assert.False(t, result.StartsTurn)
	requests := fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
	require.Len(t, requests, 1)
	promptID := decodePrompt(t, requests[0]).PromptID
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unrelated","state":"running","triggerPromptId":"external","origin":{"kind":"user"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unrelated","state":"completed","triggerPromptId":"external","origin":{"kind":"user"}}}`)
	_, err = store.Enqueue(t.Context(), inputqueue.NewItem{ID: "Q", AgentID: child.childAgentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Queued Q"})
	require.NoError(t, err)
	queue.NotifyDependencyReady(child.childAgentID)
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-dispatcher.attempts:
		require.ErrorIs(t, err, agent.ErrInputPending)
	case <-ctx.Done():
		t.Fatal("Q did not reach the pending native admission")
	}
	require.Eventually(t, func() bool {
		snapshot, err := store.Snapshot(t.Context(), child.childAgentID)
		return err == nil && !snapshot.ActiveTurn && len(snapshot.Items) == 1 && snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED
	}, 30*time.Second, time.Millisecond)
	native.transcriptAgentOps(t, "agent-0", 3, fmt.Sprintf(`{"op":"prompt.upsert","prompt":{"promptId":%q,"status":"aborted","createdAt":"2026-11-16T00:00:00Z"}}`, promptID))
	select {
	case err := <-dispatcher.attempts:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("P readiness did not automatically dispatch Q after unrelated child cleanup")
	}
	require.Eventually(t, func() bool {
		snapshot, err := store.Snapshot(t.Context(), child.childAgentID)
		return err == nil && len(snapshot.Items) == 0 && !snapshot.ActiveTurn
	}, 30*time.Second, time.Millisecond)
	assert.False(t, provider.ActiveChildTurnState(dispatcher.key).Active)
	assert.Len(t, fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")), 2)
}
