package kimi

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestExactControlInputsRequiresAProvenPositionAfterConsumption(t *testing.T) {
	t.Parallel()
	const sessionID, turnID = "session", "turn"
	stepOne, stepTwo, stepThree := int64(1), int64(2), int64(3)
	item := func(kind string, step *int64) *kimiConversationItemState {
		return &kimiConversationItemState{identity: kimiConversationItemIdentity{kind: kind, stepIndex: step}}
	}
	document := newKimiConversationState(sessionID, kimiMainAgentID)
	turn := &kimiConversationTurnState{
		header: kimiConversationTurnHeader{TurnID: turnID, Kind: contracts.KimiConversationTurnKindAgent, Status: contracts.KimiConversationStatusRunning},
		items: map[string]*kimiConversationItemState{
			"opener":       item(contracts.KimiConversationItemKindUserMessage, nil),
			"later-input":  item(contracts.KimiConversationItemKindUserMessage, &stepTwo),
			"earlier-tool": item(contracts.KimiConversationItemKindToolCall, &stepOne),
			"later-tool":   item(contracts.KimiConversationItemKindToolCall, &stepThree),
			"unknown-step": item(contracts.KimiConversationItemKindToolCall, nil),
		},
	}
	document.turns[turnID] = turn
	output := &kimiConversationOutput{document: document, turns: map[string]*kimiConversationTurnOutput{
		turnID: {replyInputs: map[string]*kimiPromptSubmission{
			"a": {id: "a", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "opener", consumingOpener: true},
			"b": {id: "b", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "later-input"},
		}},
	}}
	control := &kimiPendingControl{sessionID: sessionID, agentID: kimiMainAgentID}
	assert.Empty(t, exactControlInputs(output, control))
	control.toolCallID = "earlier-tool"
	assert.Equal(t, []string{"a"}, exactControlInputs(output, control))
	control.toolCallID = "later-tool"
	assert.Equal(t, []string{"a", "b"}, exactControlInputs(output, control))
	control.toolCallID = "unknown-step"
	assert.Equal(t, []string{"a"}, exactControlInputs(output, control))
	control.toolCallID = "later-tool"
	control.agentID = "another-agent"
	assert.Empty(t, exactControlInputs(output, control))
	control.agentID = kimiMainAgentID
	control.toolIdentityConflict = true
	assert.Empty(t, exactControlInputs(output, control))
	control.toolIdentityConflict = false
	document.turns["other-turn"] = &kimiConversationTurnState{items: map[string]*kimiConversationItemState{
		"later-tool": item(contracts.KimiConversationItemKindToolCall, &stepThree),
	}}
	assert.Empty(t, exactControlInputs(output, control))
	delete(document.turns, "other-turn")
	turn.header.Status = contracts.KimiConversationStatusEnded
	assert.Empty(t, exactControlInputs(output, control))
}

func TestKimiExactReplyProjectionOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	const sessionID, turnID = "session_1", "turn_1"
	userText := "prompt"
	assistantText := "reply"
	snapshot := kimiConversationTurn{
		kimiConversationTurnHeader: kimiConversationTurnHeader{
			TurnID:  turnID,
			Kind:    contracts.KimiConversationTurnKindAgent,
			Status:  contracts.KimiConversationStatusEnded,
			Outcome: contracts.KimiConversationOutcomeCompleted,
		},
		Items: []kimiConversationItem{
			{
				Type: contracts.KimiConversationItemKindUserMessage, ItemID: "u1",
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				Text: &userText, PromptIDs: []string{"in-e", "in-b", "in-d", "in-a", "in-c"}, Placement: "opener",
			},
			{
				Type: contracts.KimiConversationItemKindAssistantMessage, ItemID: "a1",
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				Text: &assistantText,
			},
		},
	}
	inputs := map[string]*kimiPromptSubmission{
		"in-e": {id: "in-e", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "u1"},
		"in-b": {id: "in-b", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "u1"},
		"in-d": {id: "in-d", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "u1"},
		"in-a": {id: "in-a", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "u1"},
		"in-c": {id: "in-c", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "u1"},
		"ms-e": {id: "ms-e", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "other"},
		"ms-b": {id: "ms-b", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "other"},
		"ms-d": {id: "ms-d", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "other"},
		"ms-a": {id: "ms-a", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "other"},
		"ms-c": {id: "ms-c", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "other"},
	}
	wantInputs := []agent.NativeReplyBoundary{
		{InputID: "in-a", AfterOrdinal: 1},
		{InputID: "in-b", AfterOrdinal: 1},
		{InputID: "in-c", AfterOrdinal: 1},
		{InputID: "in-d", AfterOrdinal: 1},
		{InputID: "in-e", AfterOrdinal: 1},
	}
	wantMissing := []string{"ms-a", "ms-b", "ms-c", "ms-d", "ms-e"}
	for range 10 {
		reply, missing, err := exactReplyProjection(sessionID, kimiMainAgentID, snapshot, inputs)
		require.NoError(t, err)
		assert.Equal(t, wantInputs, reply.Inputs)
		gotMissing := make([]string, 0, len(missing))
		for _, sub := range missing {
			gotMissing = append(gotMissing, sub.id)
		}
		assert.Equal(t, wantMissing, gotMissing)
	}
}

type kimiControlOrderEvent struct {
	kind       string
	requestID  string
	instanceID string
	inputIDs   []string
}

type kimiControlOrderSink struct {
	agenttest.ControlSink
	mu        sync.Mutex
	onPublish func(agent.ControlRequest)
	events    []kimiControlOrderEvent
}

func (s *kimiControlOrderSink) PublishControlRequest(request agent.ControlRequest) error {
	s.mu.Lock()
	s.events = append(s.events, kimiControlOrderEvent{
		kind:       "publish",
		requestID:  request.RequestID,
		instanceID: request.InstanceID,
		inputIDs:   append([]string(nil), request.NativeInputIDs...),
	})
	hook := s.onPublish
	s.mu.Unlock()
	if hook != nil {
		hook(request)
	}
	return s.ControlSink.PublishControlRequest(request)
}

func (s *kimiControlOrderSink) SetNativeControlInputs(control agent.NativeControlInputs) error {
	s.mu.Lock()
	s.events = append(s.events, kimiControlOrderEvent{
		kind:       "set_inputs",
		requestID:  control.RequestID,
		instanceID: control.InstanceID,
		inputIDs:   append([]string(nil), control.InputIDs...),
	})
	s.mu.Unlock()
	return s.ControlSink.SetNativeControlInputs(control)
}

func (s *kimiControlOrderSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.mu.Lock()
	s.events = append(s.events, kimiControlOrderEvent{
		kind:       "cancel",
		requestID:  requestID,
		instanceID: instanceID,
	})
	s.mu.Unlock()
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func (s *kimiControlOrderSink) snapshotEvents() []kimiControlOrderEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]kimiControlOrderEvent(nil), s.events...)
}

func TestKimiControlPublicationCarriesKnownNativeInputIDsAndOrdersUpdates(t *testing.T) {
	t.Parallel()

	t.Run("PublishControlRequest carries already-known NativeInputIDs", func(t *testing.T) {
		t.Parallel()
		sink := &kimiControlOrderSink{}
		a := newOfflineKimiAgent(t, sink)
		sink.UpdateSessionID("session_1")
		rig := &kimiOutputRig{agent: a, sink: &sink.ControlSink}

		a.dispatchMu.Lock()
		sub, err := a.registerPromptSubmissionLocked("session_1", kimiMainAgentID)
		require.NoError(t, err)
		sub.replySink = a.sink
		a.finishDispatch()

		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+kimiPublicPromptItem(t, "u0", "opener", sub.id)+`}`)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)
		ev := approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"})
		ev["tool_call_id"] = "call-1"
		a.HandleOutput(kimiEventFrame(t, "session_1", ev))

		events := sink.snapshotEvents()
		require.Len(t, events, 1, "known NativeInputIDs should ride PublishControlRequest without a redundant SetNativeControlInputs")
		assert.Equal(t, "publish", events[0].kind)
		assert.Equal(t, []string{sub.id}, events[0].inputIDs)
	})

	t.Run("SetNativeControlInputs is not overtaken by immediate CancelControlRequestInstance when output queue is held", func(t *testing.T) {
		t.Parallel()
		sink := &kimiControlOrderSink{}
		a := newOfflineKimiAgent(t, sink)
		sink.UpdateSessionID("session_1")
		rig := &kimiOutputRig{agent: a, sink: &sink.ControlSink}

		a.dispatchMu.Lock()
		openerSub, err := a.registerPromptSubmissionLocked("session_1", kimiMainAgentID)
		require.NoError(t, err)
		openerSub.replySink = a.sink
		steerSub, err := a.registerPromptSubmissionLocked("session_1", kimiMainAgentID)
		require.NoError(t, err)
		steerSub.replySink = a.sink
		a.finishDispatch()

		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+kimiPublicPromptItem(t, "u0", "opener", openerSub.id)+`}`)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-1","step_index":2,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)
		ev := approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"})
		ev["tool_call_id"] = "call-1"
		a.HandleOutput(kimiEventFrame(t, "session_1", ev))

		// Hold the root output queue with an unrelated pending write.
		var release atomic.Bool
		queue := agent.OutputQueueFor(a.sink)
		require.NotNil(t, queue)
		queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "hold root queue", Run: func() error {
			if !release.Load() {
				return agent.ErrOutputInFlight
			}
			return nil
		}})

		// A steer arrives at step 1 (< tool step 2), updating the control's inputs, then the control ends immediately.
		rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.ended","turn_id":"t0","item":{"type":"user_message","item_id":"u1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[%q],"placement":"steer","origin":{"kind":"user"},"text":"Steer","attachments":[],"skills":[]}}`, steerSub.id))
		a.HandleOutput(kimiEventFrame(t, "session_1", map[string]any{
			"type": contracts.KimiFrameInteraction, "kind": contracts.KimiInteractionKindApproval,
			"status": kimiInteractionEnded, "outcome": "cancelled", "interaction_id": "approval_1", "tool_call_id": "call-1",
		}))

		events := sink.snapshotEvents()
		var kinds []string
		for _, ev := range events {
			kinds = append(kinds, ev.kind)
		}
		assert.Equal(t, []string{"publish", "set_inputs", "cancel"}, kinds)
		release.Store(true)
		awaitKimiTestOutput(t, a.sink)
	})

	t.Run("delta and turn.updated frames do not call refreshExactControls", func(t *testing.T) {
		t.Parallel()
		sink := &kimiControlOrderSink{}
		a := newOfflineKimiAgent(t, sink)
		sink.UpdateSessionID("session_1")
		rig := &kimiOutputRig{agent: a, sink: &sink.ControlSink}

		a.dispatchMu.Lock()
		sub, err := a.registerPromptSubmissionLocked("session_1", kimiMainAgentID)
		require.NoError(t, err)
		sub.replySink = a.sink
		a.finishDispatch()

		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+kimiPublicPromptItem(t, "u0", "opener", sub.id)+`}`)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"assistant_message","item_id":"msg-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"hi"}}`)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)
		// Publish control without tool_call_id initially so nativeInputs starts nil.
		a.HandleOutput(kimiEventFrame(t, "session_1", approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"})))
		require.Len(t, sink.snapshotEvents(), 1)

		// Mutate the control's toolCallID directly so any call to refreshExactControls would emit SetNativeControlInputs.
		a.Mu.Lock()
		a.controls["approval_1"].toolCallID = "call-1"
		a.Mu.Unlock()

		rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"msg-1","field":"text","delta":" there"}`)
		rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"t0","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:00Z"}}`)
		assert.Len(t, sink.snapshotEvents(), 1, "item.delta and turn.updated must not call refreshExactControls")

		// A structural frame proves the mutated toolCallID really triggers refreshExactControls.
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"assistant_message","item_id":"msg-1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"hi there"}}`)
		events := sink.snapshotEvents()
		require.Len(t, events, 2)
		assert.Equal(t, "set_inputs", events[1].kind)
		assert.Equal(t, []string{sub.id}, events[1].inputIDs)
	})

	t.Run("in-flight native input update during afterText publish runs without waiting behind later queued writes", func(t *testing.T) {
		t.Parallel()
		sink := &kimiControlOrderSink{}
		a := newOfflineKimiAgent(t, sink)
		sink.UpdateSessionID("session_1")
		rig := &kimiOutputRig{agent: a, sink: &sink.ControlSink}

		a.dispatchMu.Lock()
		openerSub, err := a.registerPromptSubmissionLocked("session_1", kimiMainAgentID)
		require.NoError(t, err)
		openerSub.replySink = a.sink
		a.finishDispatch()

		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+kimiPublicPromptItem(t, "u0", "opener", openerSub.id)+`}`)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"assistant_message","item_id":"msg-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"I will run ls."}}`)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-1","step_index":2,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)

		// Hold the root queue so afterText queues publishRegisteredControl onto it.
		var releaseFirst, releaseTail atomic.Bool
		queue := agent.OutputQueueFor(a.sink)
		require.NotNil(t, queue)
		queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "hold before publish", Run: func() error {
			if !releaseFirst.Load() {
				return agent.ErrOutputInFlight
			}
			return nil
		}})

		ev := approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"})
		ev["tool_call_id"] = "call-1"
		a.HandleOutput(kimiEventFrame(t, "session_1", ev))
		require.Empty(t, sink.snapshotEvents())

		// Append an unrelated blocked write AFTER publishRegisteredControl on the root queue.
		queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "hold after publish", Run: func() error {
			if !releaseTail.Load() {
				return agent.ErrOutputInFlight
			}
			return nil
		}})

		// Simulate a concurrent steer updating pending.nativeInputs while PublishControlRequest runs.
		sink.mu.Lock()
		sink.onPublish = func(req agent.ControlRequest) {
			a.Mu.Lock()
			pending := a.controls[req.RequestID]
			a.Mu.Unlock()
			if pending != nil {
				pending.nativeInputs.Store(&agent.NativeControlInputs{
					SessionID: req.AgentSessionID, RequestID: req.RequestID,
					InstanceID: req.InstanceID, InputIDs: []string{openerSub.id, "steered-id"},
				})
			}
		}
		sink.mu.Unlock()

		releaseFirst.Store(true)
		_ = queue.Flush()

		// Even though releaseTail is still false, SetNativeControlInputs must have run immediately after PublishControlRequest!
		events := sink.snapshotEvents()
		require.Len(t, events, 2)
		assert.Equal(t, "publish", events[0].kind)
		assert.Equal(t, []string{openerSub.id}, events[0].inputIDs)
		assert.Equal(t, "set_inputs", events[1].kind)
		assert.Equal(t, []string{openerSub.id, "steered-id"}, events[1].inputIDs)

		releaseTail.Store(true)
		awaitKimiTestOutput(t, a.sink)
	})

	t.Run("toolCallTurns index tracks tool calls across apply lifecycle and rejects cross-turn duplicates", func(t *testing.T) {
		t.Parallel()
		rig := newKimiConversationRig(t)
		rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)
		output := rig.agent.run(kimiMainAgentID).conversation
		require.NotNil(t, output.document.toolCallTurns)
		assert.Same(t, output.document.turns["t0"], exactControlToolTurn(output.document, "call-1"))

		// A second turn using the same tool call ID makes ownership ambiguous -> nil.
		rig.startPublicTurn(t, kimiMainAgentID, "t1", 1)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t1","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)
		assert.Nil(t, exactControlToolTurn(output.document, "call-1"))

		// Removing the item from t0 restores unique ownership in t1.
		rig.conversationFrame(t, `{"type":"item.removed","turn_id":"t0","item_id":"call-1"}`)
		assert.Same(t, output.document.turns["t1"], exactControlToolTurn(output.document, "call-1"))

		// Removing turn t1 removes it from the index cleanly.
		rig.conversationFrame(t, `{"type":"turns.removed","turn_ids":["t1"]}`)
		assert.Nil(t, exactControlToolTurn(output.document, "call-1"))
		assert.Empty(t, output.document.toolCallTurns)
	})
}

type kimiRetirementOrderSink struct {
	agenttest.ControlSink
	mu      sync.Mutex
	retired []string
}

func (s *kimiRetirementOrderSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.mu.Lock()
	s.retired = append(s.retired, receipt.InputID)
	s.mu.Unlock()
	return s.ControlSink.RetireNativeInputTurn(receipt, outcome)
}

func TestKimiReplyRetirementOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	sink := &kimiRetirementOrderSink{}
	a := newOfflineKimiAgent(t, sink)
	sink.UpdateSessionID("session_1")
	a.dispatchMu.Lock()
	a.promptSubmissions = map[string]*kimiPromptSubmission{
		"sub-e":     {id: "sub-e", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
		"sub-b":     {id: "sub-b", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
		"sub-d":     {id: "sub-d", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
		"sub-a":     {id: "sub-a", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
		"sub-c":     {id: "sub-c", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
		"other-ses": {id: "other-ses", sessionID: "session_2", receiver: kimiMainAgentID, replySink: a.sink},
		"no-sink":   {id: "no-sink", sessionID: "session_1", receiver: kimiMainAgentID},
	}
	a.retireKimiSessionReplies("session_1")
	_, foreignKept := a.promptSubmissions["other-ses"]
	_, nilSinkKept := a.promptSubmissions["no-sink"]
	a.finishDispatch()
	awaitKimiTestOutput(t, a.sink)
	sink.mu.Lock()
	got := append([]string(nil), sink.retired...)
	sink.mu.Unlock()
	assert.Equal(t, []string{"sub-a", "sub-b", "sub-c", "sub-d", "sub-e"}, got)
	assert.True(t, foreignKept, "retireKimiSessionReplies leaves other sessions untouched")
	assert.True(t, nilSinkKept, "retireKimiSessionReplies leaves submissions without a replySink untouched")

	// retireExactReplyInputs also retires turn reply inputs in deterministic sorted order.
	sink.mu.Lock()
	sink.retired = nil
	sink.mu.Unlock()
	a.dispatchMu.Lock()
	run := a.run(kimiMainAgentID)
	run.conversation = &kimiConversationOutput{document: newKimiConversationState("session_1", kimiMainAgentID), replyPublications: 1}
	pub := &kimiConversationTurnOutput{
		replyInputs: map[string]*kimiPromptSubmission{
			"turn-d": {id: "turn-d", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
			"turn-a": {id: "turn-a", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
			"turn-c": {id: "turn-c", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
			"turn-b": {id: "turn-b", sessionID: "session_1", receiver: kimiMainAgentID, replySink: a.sink},
		},
	}
	a.retireExactReplyInputs(run, pub)
	a.finishDispatch()
	awaitKimiTestOutput(t, a.sink)
	sink.mu.Lock()
	got = append([]string(nil), sink.retired...)
	sink.mu.Unlock()
	assert.Equal(t, []string{"turn-a", "turn-b", "turn-c", "turn-d"}, got)
}

func TestKimiDuplicateControlInteractionFillsOrConflictsToolCallID(t *testing.T) {
	t.Parallel()
	sink := &kimiControlOrderSink{}
	a := newOfflineKimiAgent(t, sink)
	sink.UpdateSessionID("session_1")
	rig := &kimiOutputRig{agent: a, sink: &sink.ControlSink}

	a.dispatchMu.Lock()
	sub, err := a.registerPromptSubmissionLocked("session_1", kimiMainAgentID)
	require.NoError(t, err)
	sub.replySink = a.sink
	a.finishDispatch()

	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+kimiPublicPromptItem(t, "u0", "opener", sub.id)+`}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"ls"}}}`)

	// Initial interaction omits tool_call_id, so PublishControlRequest carries no native inputs.
	ev := approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"})
	a.HandleOutput(kimiEventFrame(t, "session_1", ev))

	// Duplicate interaction supplies tool_call_id and refreshes the control's native inputs.
	ev["tool_call_id"] = "call-1"
	a.HandleOutput(kimiEventFrame(t, "session_1", ev))

	// A conflicting tool_call_id marks a conflict and clears the control's native inputs.
	ev["tool_call_id"] = "call-2"
	a.HandleOutput(kimiEventFrame(t, "session_1", ev))

	events := sink.snapshotEvents()
	require.Len(t, events, 3)
	assert.Equal(t, "publish", events[0].kind)
	assert.Empty(t, events[0].inputIDs)
	assert.Equal(t, "set_inputs", events[1].kind)
	assert.Equal(t, []string{sub.id}, events[1].inputIDs)
	assert.Equal(t, "set_inputs", events[2].kind)
	assert.Empty(t, events[2].inputIDs)
}

type kimiFailingControlInputsSink struct {
	agenttest.ControlSink
	mu    sync.Mutex
	err   error
	calls int
}

func (s *kimiFailingControlInputsSink) SetNativeControlInputs(control agent.NativeControlInputs) error {
	s.mu.Lock()
	s.calls++
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.ControlSink.SetNativeControlInputs(control)
}

func TestKimiApplyExactControlInputsErrorAndInactiveGuards(t *testing.T) {
	t.Parallel()
	sink := &kimiFailingControlInputsSink{err: errors.New("storage unavailable")}
	a := newOfflineKimiAgent(t, sink)
	sink.UpdateSessionID("session_1")

	pending := publishedKimiControl(kimiControlApproval, "session_1", false)
	proof := &agent.NativeControlInputs{
		SessionID: "session_1", RequestID: "approval_1", InstanceID: pending.instanceID, InputIDs: []string{"in-1"},
	}
	pending.nativeInputs.Store(proof)
	a.Mu.Lock()
	a.controls = map[string]*kimiPendingControl{"approval_1": pending}
	a.Mu.Unlock()

	// A failing SetNativeControlInputs leaves appliedInputs nil so a retry runs.
	a.applyExactControlInputs(pending)
	assert.Equal(t, 1, sink.calls)
	assert.Nil(t, pending.appliedInputs)

	// Once the error clears, the same proof succeeds and records appliedInputs.
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	a.applyExactControlInputs(pending)
	assert.Equal(t, 2, sink.calls)
	assert.Same(t, proof, pending.appliedInputs)

	// Re-applying the already-applied proof is a no-op.
	a.applyExactControlInputs(pending)
	assert.Equal(t, 2, sink.calls)

	// Inactive controls (answered, stopped, or replaced) skip SetNativeControlInputs.
	nextProof := &agent.NativeControlInputs{
		SessionID: "session_1", RequestID: "approval_1", InstanceID: pending.instanceID, InputIDs: []string{"in-1", "in-2"},
	}
	pending.nativeInputs.Store(nextProof)

	a.Mu.Lock()
	pending.answered = true
	a.Mu.Unlock()
	a.applyExactControlInputs(pending)
	assert.Equal(t, 2, sink.calls, "answered control must not update native inputs")

	a.Mu.Lock()
	pending.answered = false
	a.Mu.Unlock()
	a.SetStoppedForTest(true)
	a.applyExactControlInputs(pending)
	assert.Equal(t, 2, sink.calls, "stopped agent must not update native inputs")

	a.SetStoppedForTest(false)
	a.Mu.Lock()
	delete(a.controls, "approval_1")
	a.Mu.Unlock()
	a.applyExactControlInputs(pending)
	assert.Equal(t, 2, sink.calls, "removed control must not update native inputs")
}
