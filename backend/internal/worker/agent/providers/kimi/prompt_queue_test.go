package kimi

import (
	"fmt"
	"sync/atomic"
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
	childID := adoptNativeKimiChild(t, provider, "agent-0")
	run := provider.run("agent-0")
	waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "the adopted child receives its native page")
	awaitKimiTestOutput(t, root)
	fake.reply("POST "+kimiSessionPath("session_1", "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	store := inputqueue.NewStore(database, nil)
	dispatcher := &kimiPendingQueueDispatcher{provider: provider, key: "session_1/agent-0", attempts: make(chan error, 2)}
	queue := inputqueue.NewManager(store, dispatcher, inputqueue.NopObserver{})
	t.Cleanup(queue.StopAndWait)
	var readySignals atomic.Int32
	// PlanWriterFor uses the real captured native-source validator. This checks
	// actual child registration expiry without adding a state or transcript row.
	dispatcher.ready = func(captured agent.CapturedTranscript) error {
		_, err := root.ChildSink(childID).PlanWriterFor(captured)
		if err == nil {
			readySignals.Add(1)
			queue.NotifyDependencyReady(childID)
		}
		return nil
	}
	result, err := provider.SendChildInput(dispatcher.key, agent.ChildInput{Content: "Pending P", Ready: dispatcher.ready})
	require.NoError(t, err)
	assert.False(t, result.StartsTurn)
	requests := fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
	require.Len(t, requests, 1)
	promptID := decodePrompt(t, requests[0]).PromptID
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "unrelated", ordinal: 0, promptID: "external", outcome: "completed"})
	_, err = store.Enqueue(t.Context(), inputqueue.NewItem{ID: "Q", AgentID: childID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Queued Q"})
	require.NoError(t, err)
	queue.NotifyDependencyReady(childID)
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-dispatcher.attempts:
		require.ErrorIs(t, err, agent.ErrInputPending)
	case <-ctx.Done():
		t.Fatal("Q did not reach the pending native admission")
	}
	require.Eventually(t, func() bool {
		snapshot, err := store.Snapshot(t.Context(), childID)
		return err == nil && !snapshot.ActiveTurn && len(snapshot.Items) == 1 && snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED
	}, 30*time.Second, time.Millisecond)
	provider.HandleOutput(kimiConversationTestFrame(t, native.sessionID(), "agent-0", `{"type":"turn.started","turn":{"turn_id":"pending-prompt","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
	provider.HandleOutput(kimiConversationTestFrame(t, native.sessionID(), "agent-0", fmt.Sprintf(`{"type":"item.ended","turn_id":"pending-prompt","item":{"type":"user_message","item_id":"owned-user-item","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Pending P","prompt_ids":[%q],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}}`, promptID)))
	awaitKimiTestOutput(t, root)
	require.True(t, provider.ActiveChildTurnState(dispatcher.key).Active)
	require.Zero(t, readySignals.Load(), "a consumed prompt still owns an active native turn")
	provider.HandleOutput(kimiConversationTestFrame(t, native.sessionID(), "agent-0", `{"type":"turn.ended","turn":{"turn_id":"pending-prompt","ordinal":1,"kind":"agent","status":"ended","outcome":"interrupted","end_reason":"aborted","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
	select {
	case err := <-dispatcher.attempts:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("P readiness did not automatically dispatch Q after unrelated child cleanup")
	}
	require.Eventually(t, func() bool {
		snapshot, err := store.Snapshot(t.Context(), childID)
		return err == nil && len(snapshot.Items) == 0 && !snapshot.ActiveTurn
	}, 30*time.Second, time.Millisecond)
	assert.EqualValues(t, 1, readySignals.Load())
	assert.False(t, provider.ActiveChildTurnState(dispatcher.key).Active)
	assert.Len(t, fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")), 2)
}
