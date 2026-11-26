package droid

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type droidChildControlSink struct {
	agenttest.ControlSink
	mu       sync.Mutex
	children map[string]*agenttest.ControlSink
}

func (s *droidChildControlSink) childControlSink(childID string) *agenttest.ControlSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.children == nil {
		s.children = make(map[string]*agenttest.ControlSink)
	}
	child := s.children[childID]
	if child == nil {
		child = &agenttest.ControlSink{}
		s.children[childID] = child
	}
	return child
}

func (s *droidChildControlSink) ChildSink(childID string) agent.ProviderServices {
	return agent.NewProviderServices(s.childControlSink(childID))
}

func (s *droidChildControlSink) AcquireChildSink(childID string) agent.ChildSinkLease {
	return agent.ChildSinkLease{Sink: s.ChildSink(childID)}
}

func (s *droidChildControlSink) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	return agent.ChildSinkLease{Sink: s.ChildSink(childID)}
}

func newBoundChildReplyHarness(t *testing.T) (*Agent, *Agent, *droidChildControlSink, *agenttest.ControlSink, *bufferStdin, *bufferStdin) {
	t.Helper()
	root, _, rootStdin := newSteerAgent(t)
	rootSink := &droidChildControlSink{}
	root.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(rootSink))
	childAgentID, err := rootSink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		SpawnSpanID:      "droid-tool-task-1",
		ProviderChildKey: steerChildID,
		AgentSessionID:   steerChildID,
		Title:            "Inspect the note",
	})
	require.NoError(t, err)
	require.NoError(t, rootSink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask, RowKey: steerChildID, Kind: bgtask.KindSubagent,
		ChildAgentID: childAgentID, ParentAgentID: root.AgentID(), Title: "Inspect the note",
		Status: bgtask.StatusSucceeded,
	}))
	child, _, childStdin := newSteerAgent(t)
	child.sink = agent.NewModelProgressResetSink(root.sink.ChildSink(childAgentID))
	child.Mu.Lock()
	child.sessionID = steerChildID
	child.Mu.Unlock()
	child.onTurnEnd = func(status bgtask.Status) {
		root.closeBoundChildTurn(steerChildID, status)
	}
	ready := make(chan struct{})
	close(ready)
	root.childConns = map[string]*droidChildConnection{steerChildID: {ready: ready, agent: child}}
	return root, child, rootSink, rootSink.childControlSink(childAgentID), rootStdin, childStdin
}

func TestDroidChildSendReturnsNativeReceiptAndExactReply(t *testing.T) {
	t.Parallel()
	root, child, rootSink, childSink, _, _ := newBoundChildReplyHarness(t)

	result, err := root.SendChildInput(steerChildID, agent.ChildInput{Content: "Report the child marker."})
	require.NoError(t, err)
	require.NotEmpty(t, result.NativeReceipt.InputID)
	assert.Equal(t, steerChildID, result.NativeReceipt.SessionID)
	assert.Equal(t, result.NativeReceipt.InputID, result.NativeReceipt.TurnID)

	deliverDroidReplyMessage(t, child, "", result.NativeReceipt.InputID, "prev", "user",
		map[string]any{"type": "text", "text": "Report the child marker."})
	deliverDroidReplyMessage(t, child, "", "child-msg-1", result.NativeReceipt.InputID, "assistant",
		map[string]any{"type": "text", "text": "Child exact reply."})
	deliverDroidReplyEnd(t, child, result.NativeReceipt.TurnID)

	key := droidMessageWriteKey("child-msg-1", 0)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: steerChildID, TurnID: result.NativeReceipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: result.NativeReceipt.InputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: key}},
	}}, childSink.NativeReplyTurns())
	foundKey := false
	for _, row := range childSink.Messages() {
		if row.IdempotencyKey == key {
			foundKey = true
		}
	}
	assert.True(t, foundKey, "the child sink must persist the keyed reply row")
	row, ok := rootSink.BackgroundTask(steerChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestDroidChildSteerReportsEchoAndPostSteerReply(t *testing.T) {
	t.Parallel()
	root, child, _, childSink, _, childStdin := newBoundChildReplyHarness(t)
	steerer, ok := any(root).(agent.ChildSteerer)
	require.True(t, ok, "Droid must implement ChildSteerer for bound child turns")
	assert.Equal(t, agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: true}, steerer.ChildSteerPolicy())

	lead, err := root.SendChildInput(steerChildID, agent.ChildInput{Content: "Run the child tool."})
	require.NoError(t, err)
	deliverDroidReplyMessage(t, child, "", lead.NativeReceipt.InputID, "prev", "user")
	deliverDroidReplyMessage(t, child, "", "child-lead-1", lead.NativeReceipt.InputID, "assistant",
		map[string]any{"type": "text", "text": "Before steer."},
		map[string]any{"type": "tool_use", "id": "child-tool-1"})
	deliverDroidReplyNotification(t, child, map[string]any{
		"type": contracts.DroidToolNotificationToolResult, "messageId": "child-tool-res-1", "toolUseId": "child-tool-1",
	})

	_, err = steerer.SteerChildInput(steerChildID, agent.SteerRequest{
		InputID: "queue-steer-1", AttemptID: "attempt-1", Content: "Include the steered note.",
	})
	require.NoError(t, err)
	params := lastRequest(t, childStdin)
	steerMessageID, _ := params["messageId"].(string)
	require.NotEmpty(t, steerMessageID)

	deliverDroidReplyMessage(t, child, "", steerMessageID, "child-tool-res-1", "user",
		map[string]any{"type": "text", "text": "Include the steered note."})
	require.Equal(t, []agent.SteerResolution{{
		InputID: "queue-steer-1", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeEchoed,
		Receipt: agent.NativeInputReceipt{SessionID: steerChildID, InputID: steerMessageID, TurnID: lead.NativeReceipt.TurnID},
	}}, childSink.SteerResolutions())

	deliverDroidReplyMessage(t, child, "", "child-steered-1", steerMessageID, "assistant",
		map[string]any{"type": "text", "text": "After steer."})
	deliverDroidReplyEnd(t, child, lead.NativeReceipt.TurnID)

	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: steerChildID, TurnID: lead.NativeReceipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{
			{InputID: lead.NativeReceipt.InputID, AfterOrdinal: 1},
			{InputID: steerMessageID, AfterOrdinal: 2},
		},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: droidMessageWriteKey("child-lead-1", 0)},
			{Ordinal: 3, MessageKey: droidMessageWriteKey("child-steered-1", 0)},
		},
	}}, childSink.NativeReplyTurns())
}

func TestDroidRootRoutesChildControlAnswersByInstanceID(t *testing.T) {
	t.Parallel()
	root, child, rootSink, childSink, rootStdin, childStdin := newBoundChildReplyHarness(t)

	// Publish a permission with the SAME rpcId ("rpc-shared") on BOTH root and child.
	rootPerm, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "type": droidTypeRequest, "id": "rpc-shared", "method": droidMethodRequestPermission,
		"params": map[string]any{
			"toolUses": []map[string]any{{
				"toolUse":          map[string]any{"type": "tool_use", "id": "root-call", "name": "Edit", "input": map[string]any{}},
				"confirmationType": contracts.DroidConfirmationTypeEdit,
				"details":          map[string]any{"type": contracts.DroidConfirmationTypeEdit},
			}},
			"options": []map[string]any{{"label": "Allow", "value": contracts.DroidPermissionOptionProceedOnce}},
		},
	})
	require.NoError(t, err)
	childPerm, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "type": droidTypeRequest, "id": "rpc-shared", "method": droidMethodRequestPermission,
		"params": map[string]any{
			"toolUses": []map[string]any{{
				"toolUse":          map[string]any{"type": "tool_use", "id": "child-call", "name": "Edit", "input": map[string]any{}},
				"confirmationType": contracts.DroidConfirmationTypeEdit,
				"details":          map[string]any{"type": contracts.DroidConfirmationTypeEdit},
			}},
			"options": []map[string]any{{"label": "Allow", "value": contracts.DroidPermissionOptionProceedOnce}},
		},
	})
	require.NoError(t, err)
	root.HandleOutput(rootPerm)
	child.HandleOutput(childPerm)

	rootReqs := rootSink.PublishedControls()
	childReqs := childSink.PublishedControls()
	require.Len(t, rootReqs, 1)
	require.Len(t, childReqs, 1)
	require.NotEmpty(t, rootReqs[0].InstanceID)
	require.NotEmpty(t, childReqs[0].InstanceID)
	require.NotEqual(t, rootReqs[0].InstanceID, childReqs[0].InstanceID)

	answer, err := json.Marshal(droidResponseEnvelope("rpc-shared", contracts.DroidPermissionResult{
		SelectedOption: contracts.DroidPermissionOptionProceedOnce,
	}))
	require.NoError(t, err)
	require.NoError(t, root.SendRawInput(agent.RawInput{
		Data:              answer,
		ControlInstanceID: childReqs[0].InstanceID,
	}))
	assert.Empty(t, rootStdin.frames(), "answering the child's control instance must not write to the root stdin")
	require.Len(t, childStdin.frames(), 1, "answering the child's control instance must write to the child stdin")
}

func TestDroidSendRawInputControlInstanceAndNotPendingErrors(t *testing.T) {
	t.Parallel()
	root, child1, rootSink, _, rootStdin, child1Stdin := newBoundChildReplyHarness(t)
	child2, _, child2Stdin := newSteerAgent(t)
	const secondChildID = "63bf9b7b-5e82-4932-83c0-a2bf1caa2fce"
	child2AgentID, err := rootSink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "droid-tool-task-2",
		ProviderChildKey: secondChildID, AgentSessionID: secondChildID, Title: "Second child",
	})
	require.NoError(t, err)
	child2.sink = agent.NewModelProgressResetSink(root.sink.ChildSink(child2AgentID))
	child2.Mu.Lock()
	child2.sessionID = secondChildID
	child2.Mu.Unlock()
	ready := make(chan struct{})
	close(ready)
	root.childConnMu.Lock()
	root.childConns[secondChildID] = &droidChildConnection{ready: ready, agent: child2}
	root.childConnMu.Unlock()

	perm, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "type": droidTypeRequest, "id": "rpc-dup", "method": droidMethodRequestPermission,
		"params": map[string]any{
			"toolUses": []map[string]any{{
				"toolUse":          map[string]any{"type": "tool_use", "id": "c-call", "name": "Edit", "input": map[string]any{}},
				"confirmationType": contracts.DroidConfirmationTypeEdit,
				"details":          map[string]any{"type": contracts.DroidConfirmationTypeEdit},
			}},
			"options": []map[string]any{{"label": "Allow", "value": contracts.DroidPermissionOptionProceedOnce}},
		},
	})
	require.NoError(t, err)
	child1.HandleOutput(perm)
	child2.HandleOutput(perm)

	answer, err := json.Marshal(droidResponseEnvelope("rpc-dup", contracts.DroidPermissionResult{
		SelectedOption: contracts.DroidPermissionOptionProceedOnce,
	}))
	require.NoError(t, err)
	assert.ErrorIs(t, root.SendRawInput(agent.RawInput{Data: answer}), agent.ErrControlInstanceUnstated)
	assert.ErrorIs(t, root.SendRawInput(agent.RawInput{Data: answer, ControlInstanceID: "stale-id"}), agent.ErrControlInstanceReplaced)

	missing, err := json.Marshal(droidResponseEnvelope("rpc-missing", contracts.DroidPermissionResult{
		SelectedOption: contracts.DroidPermissionOptionProceedOnce,
	}))
	require.NoError(t, err)
	assert.ErrorIs(t, root.SendRawInput(agent.RawInput{Data: missing}), agent.ErrControlRequestNotPending)
	assert.Empty(t, rootStdin.frames(), "refused calls must not write to root stdin")
	assert.Empty(t, child1Stdin.frames(), "refused calls must not write to child1 stdin")
	assert.Empty(t, child2Stdin.frames(), "refused calls must not write to child2 stdin")

	child2Reqs := rootSink.childControlSink(child2AgentID).PublishedControls()
	require.Len(t, child2Reqs, 1)
	require.NoError(t, root.SendRawInput(agent.RawInput{
		Data:              answer,
		ControlInstanceID: child2Reqs[0].InstanceID,
	}))
	assert.Empty(t, rootStdin.frames())
	assert.Empty(t, child1Stdin.frames())
	require.Len(t, child2Stdin.frames(), 1)
}
