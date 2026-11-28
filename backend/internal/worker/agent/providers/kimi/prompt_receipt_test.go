package kimi

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKimiChildPromptRig(t *testing.T) (*kimiTestRig, *kimiOutputRig) {
	t.Helper()
	rig := newKimiTestRig(t, agent.Options{})
	seedKimiConversationChildInvocation(t, rig)
	return rig, &kimiOutputRig{agent: rig.agent, sink: rig.sink}
}

type kimiTestChildPromptTurn struct {
	id        string
	ordinal   int64
	promptID  string
	outcome   string
	endReason string
}

// deliverKimiChildPromptTurn consumes an exact prompt through the public feed.
// Prompt records and triggerPromptId are absent from the current native protocol.
func deliverKimiChildPromptTurn(t *testing.T, native *kimiOutputRig, turn kimiTestChildPromptTurn) {
	t.Helper()
	dispatch := func(frame map[string]any) {
		raw, err := json.Marshal(frame)
		require.NoError(t, err)
		native.agent.HandleOutput(kimiConversationTestFrame(t, native.sessionID(), "agent-0", string(raw)))
	}
	header := map[string]any{"turn_id": turn.id, "ordinal": turn.ordinal, "kind": "agent", "status": "running", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}}
	dispatch(map[string]any{"type": "turn.started", "turn": header})
	dispatch(map[string]any{"type": "item.ended", "turn_id": turn.id, "item": map[string]any{
		"type": "user_message", "item_id": turn.promptID, "status": "ended", "outcome": "completed", "created_at": "2026-11-18T00:00:00Z",
		"text": "Native user prompt", "prompt_ids": []string{turn.promptID}, "placement": "opener", "origin": map[string]any{"kind": "user"}, "attachments": []any{}, "skills": []any{},
	}})
	header["status"], header["outcome"], header["ended_at"] = "ended", turn.outcome, "2026-11-18T00:00:01Z"
	if turn.endReason != "" {
		header["end_reason"] = turn.endReason
	}
	dispatch(map[string]any{"type": "turn.ended", "turn": header})
}

func TestKimiBlockedChildPromptReceiptReleasesAdmission(t *testing.T) {
	t.Parallel()
	rig, _ := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "blocked"}
	}})
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "The hook blocks this prompt."})
	require.NoError(t, err)
	require.NotEmpty(t, result.NativeReceipt.InputID)
	assert.Equal(t, rig.sessionID(), result.NativeReceipt.SessionID)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		result.NativeReceipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
	}, rig.sink.Child(child.childAgentID).NativeReplyRetirements())
	assert.Empty(t, rig.sink.NativeReplyRetirements())
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Try the next prompt."}), "blocked submission without a turn cannot retain pending admission")
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiChildLifetimeWaitsForItsAuthoritativeReply(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "Native user prompt"})
	require.NoError(t, err)
	receipt := result.NativeReceipt
	require.NotEmpty(t, receipt.InputID)

	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	text := "Native user prompt"
	snapshot := conversationRecoveryTestTurn("child-reply", 0, "The child answer.")
	snapshot.EndedAt = "2026-11-18T00:00:01Z"
	snapshot.Items = append([]kimiConversationItem{{
		Type: contracts.KimiConversationItemKindUserMessage, ItemID: receipt.InputID,
		Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
		CreatedAt: "2026-11-18T00:00:00Z", Text: &text, PromptIDs: []string{receipt.InputID}, Placement: "opener",
		Origin: json.RawMessage(`{"kind":"user"}`), Attachments: []json.RawMessage{}, Skills: []json.RawMessage{},
	}}, snapshot.Items...)
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/agents/agent-0/turns"), fakeKapReply{
		BeforeReply: func() { close(entered); <-released },
		Data:        map[string]any{"turns": []kimiConversationTurn{snapshot}, "has_more": false},
	})
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "child-reply", promptID: receipt.InputID, outcome: "completed"})
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the reply lookup did not reach the child history endpoint")
	}
	rig.agent.dispatchMu.Lock()
	assert.False(t, rig.agent.run("agent-0").childLifetime.retiring)
	rig.agent.finishDispatch()
	childSink := rig.sink.Child(child.childAgentID)
	assert.Empty(t, childSink.NativeReplyTurns())
	release()
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the exact reply lookup completes")
	turns := childSink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, int64(2), turns[0].Items[0].Ordinal)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Empty(t, rig.sink.NativeReplyTurns())
	rig.agent.dispatchMu.Lock()
	assert.True(t, rig.agent.run("agent-0").childLifetime.retiring)
	rig.agent.finishDispatch()
}

func TestKimiBlockedChildPromptReceiptPublishesReadinessBeforeCleanup(t *testing.T) {
	t.Parallel()
	rig, _ := newKimiChildPromptRig(t)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "blocked"}
	}})
	ready := make(chan error, 1)
	_, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "A blocked prompt.", Ready: func(captured agent.CapturedTranscript) error {
		_, err := rig.sink.Child(child.childAgentID).PlanWriterFor(captured)
		ready <- err
		return nil
	}})
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-ready:
		require.NoError(t, err, "readiness must use a current child capture before cleanup retires it")
	case <-ctx.Done():
		t.Fatal("the blocked receipt did not publish readiness")
	}
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.dispatchMu.Lock()
	assert.Empty(t, rig.agent.promptSubmissions, "a refused prompt that never ran retains no submission or readiness capture")
	rig.agent.finishDispatch()
}

func TestKimiNativeChildPromptFinalityPrecedesItsLateHTTPReceipt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct{ name, outcome, reason string }{
		{"blocked", "failed", "blocked"}, {"failed", "failed", "error"}, {"aborted", "interrupted", "aborted"}, {"completed", "completed", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			var ordinal atomic.Int64
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
				prompt := decodePrompt(t, request)
				index := ordinal.Add(1)
				deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: fmt.Sprintf("own-%d", index), ordinal: index, promptID: prompt.PromptID, outcome: scenario.outcome, endReason: scenario.reason})
				return map[string]any{"prompt_id": prompt.PromptID, "status": "queued"}
			}})
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "First prompt."}))
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Second prompt."}), "a late queued receipt cannot recreate a settled reservation")
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active, "the explicit native completion ends the child turn")
		})
	}
}

func TestKimiUnrelatedChildTurnCannotConsumePendingPromptAdmission(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The queued own prompt."}))
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "unrelated", ordinal: 1, promptID: "external-prompt", outcome: "completed"})
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "A second own prompt."}), agent.ErrInputPending, "another native user turn is not proof that this queued submission ran")
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 1)
}

func TestKimiOwnChildPromptItemConsumesOnlyItsPendingAdmission(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The queued own prompt."}))
	requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
	require.Len(t, requests, 1)
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "own", ordinal: 1, promptID: decodePrompt(t, requests[0]).PromptID, outcome: "completed"})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}))
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
}

func TestKimiPendingChildPromptSurvivesUnrelatedTurnCleanup(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	ready := make(chan error, 1)
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "The queued prompt.", Ready: func(captured agent.CapturedTranscript) error {
		_, err := rig.sink.Child(child.childAgentID).PlanWriterFor(captured)
		ready <- err
		return nil
	}})
	require.NoError(t, err)
	assert.False(t, result.StartsTurn)
	requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
	require.Len(t, requests, 1)
	promptID := decodePrompt(t, requests[0]).PromptID
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "unrelated", ordinal: 1, promptID: "external-prompt", outcome: "completed"})
	rig.agent.dispatchMu.Lock()
	assert.False(t, rig.agent.run("agent-0").childLifetime.retiring, "pending input still needs its original readiness source")
	rig.agent.finishDispatch()
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}), agent.ErrInputPending)
	select {
	case err := <-ready:
		t.Fatalf("an unrelated prompt released this submission's readiness: %v", err)
	default:
	}
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "own", ordinal: 2, promptID: promptID, outcome: "completed"})
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-ready:
		require.NoError(t, err, "readiness must retain a current captured source through unrelated child completion")
	case <-ctx.Done():
		t.Fatal("the exact native prompt consumption did not publish readiness")
	}
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.dispatchMu.Lock()
	submission := rig.agent.promptSubmissions[promptID]
	if assert.NotNil(t, submission) {
		assert.Nil(t, submission.ready, "prompt history must not retain a completed readiness callback")
		assert.Nil(t, submission.readyCapture.TranscriptWriter(), "the output queue owns the capture until readiness completes")
	}
	rig.agent.finishDispatch()
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}))
}

func TestKimiChildPromptWithoutReadinessRetainsNoServiceCapture(t *testing.T) {
	for _, execution := range []bool{false, true} {
		t.Run(fmt.Sprint(execution), func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			status := "blocked"
			if execution {
				status = "queued"
			}
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
				return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": status}
			}})
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "No readiness callback."}))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			promptID := decodePrompt(t, requests[0]).PromptID
			if execution {
				deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "own", ordinal: 1, promptID: promptID, outcome: "completed"})
			}
			rig.agent.dispatchMu.Lock()
			defer rig.agent.finishDispatch()
			submission := rig.agent.promptSubmissions[promptID]
			if !execution {
				assert.Nil(t, submission, "a stated blocked receipt without execution retains no submission or service capture")
				return
			}
			require.NotNil(t, submission)
			assert.Nil(t, submission.readyCapture.TranscriptWriter(), "a prompt without readiness must not retain services in its history")
		})
	}
}

func newKimiTestRigWithSink(t *testing.T, decorate func(agent.ProviderServices) agent.ProviderServices) (*kimiTestRig, *kimiOutputRig) {
	t.Helper()
	fake, server := newFakeKap(t)
	sink := &agenttest.ControlSink{}
	services := agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	if decorate != nil {
		services = decorate(services)
	}
	a, opts := newUnconnectedKimiAgent(t, agent.Options{}, services)
	require.NoError(t, a.connect(a.Context(), server.URL, fakeKapToken, opts, 30*time.Second))
	t.Cleanup(a.closeStream)
	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	main := a.run(kimiMainAgentID)
	waitFor(t, func() bool {
		return main.conversationRecovery.Load() == nil && !agent.OutputQueueFor(a.sink).HasPending()
	}, "the initial native conversation page and its output complete")
	rig := &kimiTestRig{agent: a, fake: fake, sink: sink}
	return rig, &kimiOutputRig{agent: a, sink: sink}
}

type kimiChildReplyEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *kimiChildReplyEventLog) record(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *kimiChildReplyEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type kimiOrderedChildReplySink struct {
	agent.ProviderServices
	log *kimiChildReplyEventLog
}

func (s *kimiOrderedChildReplySink) BindNativeInputTurn(receipt agent.NativeInputReceipt, turnID string) error {
	s.log.record("bind:" + turnID)
	return s.ProviderServices.BindNativeInputTurn(receipt, turnID)
}

func (s *kimiOrderedChildReplySink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	s.log.record("publish_reply:" + turn.TurnID)
	return s.ProviderServices.PublishNativeReplyTurn(turn)
}

func (s *kimiOrderedChildReplySink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.log.record("message")
	return s.ProviderServices.PersistMessage(source, content, span)
}

func (s *kimiOrderedChildReplySink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.log.record("turn_end")
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *kimiOrderedChildReplySink) SetTurnState(state agent.TurnState, seq uint64) {
	if !state.Active {
		s.log.record("turn_inactive")
	}
	s.ProviderServices.SetTurnState(state, seq)
}

func TestKimiChildReplyWritesStayOrderedWithChildTranscriptAndTurnState(t *testing.T) {
	t.Parallel()
	log := &kimiChildReplyEventLog{}
	rig, native := newKimiTestRigWithSink(t, func(services agent.ProviderServices) agent.ProviderServices {
		return agent.DecorateProviderServices(services, func(direct agent.ProviderServices) agent.ProviderServices {
			return &kimiChildReplySpyRoot{ProviderServices: direct, log: log}
		})
	})
	seedKimiConversationChildInvocation(t, rig)
	log.mu.Lock()
	log.events = nil
	log.mu.Unlock()

	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "Child prompt"})
	require.NoError(t, err)
	receipt := result.NativeReceipt

	// Hold the root output queue with an unrelated write so any child reply write
	// mistakenly staged on the root queue is visibly blocked behind it while the
	// child queue continues draining.
	var releaseRoot atomic.Bool
	rootQueue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, rootQueue)
	rootQueue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "hold root queue", Run: func() error {
		if !releaseRoot.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})

	native.startPublicTurn(t, "agent-0", "child-turn", 0)
	native.conversationAgentFrame(t, "agent-0", fmt.Sprintf(`{"type":"item.ended","turn_id":"child-turn","item":{"type":"user_message","item_id":%q,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Child prompt","prompt_ids":[%q],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}}`, receipt.InputID, receipt.InputID))
	native.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"child-turn","item":{"type":"assistant_message","item_id":"child-answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Child answer."}}`)
	native.endPublicTurn(t, "agent-0", "child-turn", 0, "completed")

	events := log.snapshot()
	assert.Equal(t, []string{"bind:child-turn", "message", "turn_end", "turn_inactive"}, events,
		"BindNativeInputTurn must enqueue directly on the child queue before child transcript rows and SetTurnState(Active:false)")

	releaseRoot.Store(true)
	awaitKimiTestOutput(t, rig.agent.sink)
}

type kimiChildReplySpyRoot struct {
	agent.ProviderServices
	log *kimiChildReplyEventLog
}

func (r *kimiChildReplySpyRoot) wrapLease(lease agent.ChildSinkLease) agent.ChildSinkLease {
	if lease.Sink == nil {
		return lease
	}
	lease.Sink = agent.DecorateProviderServices(lease.Sink, func(direct agent.ProviderServices) agent.ProviderServices {
		return &kimiOrderedChildReplySink{ProviderServices: direct, log: r.log}
	})
	return lease
}

func (r *kimiChildReplySpyRoot) AcquireChildSink(childID string) agent.ChildSinkLease {
	return r.wrapLease(r.ProviderServices.AcquireChildSink(childID))
}

func (r *kimiChildReplySpyRoot) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	return r.wrapLease(r.ProviderServices.AcquireChildStateSink(childID))
}

type kimiCountingTurnEndSink struct {
	agent.ProviderServices
	turnEnds atomic.Int64
	reads    atomic.Int64
}

func (s *kimiCountingTurnEndSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.turnEnds.Add(1)
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *kimiCountingTurnEndSink) ReadMessageByIdempotencyKey(sessionID, key string) (*agent.StoredMessage, error) {
	s.reads.Add(1)
	return s.ProviderServices.ReadMessageByIdempotencyKey(sessionID, key)
}

func TestKimiPublishExactReplySkipsDuplicateProjectionWhenSnapshotMatches(t *testing.T) {
	t.Parallel()
	counter := &kimiCountingTurnEndSink{}
	rig, native := newKimiTestRigWithSink(t, func(services agent.ProviderServices) agent.ProviderServices {
		return agent.DecorateProviderServices(services, func(direct agent.ProviderServices) agent.ProviderServices {
			counter.ProviderServices = direct
			return counter
		})
	})

	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "running"}
	}})
	receipt, err := rig.agent.SendInput("Ask something.", nil)
	require.NoError(t, err)

	userText := "Ask something."
	assistantText := "Exact answer."
	snapshot := kimiConversationTurn{
		kimiConversationTurnHeader: kimiConversationTurnHeader{
			TurnID:    "t0",
			Ordinal:   ptrInt64(0),
			Kind:      contracts.KimiConversationTurnKindAgent,
			Status:    contracts.KimiConversationStatusEnded,
			Outcome:   contracts.KimiConversationOutcomeCompleted,
			StartedAt: "2026-11-18T00:00:00Z",
			EndedAt:   "2026-11-18T00:00:01Z",
			Steps:     []kimiConversationStep{},
		},
		Items: []kimiConversationItem{
			{
				Type: contracts.KimiConversationItemKindUserMessage, ItemID: receipt.InputID,
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				CreatedAt: "2026-11-18T00:00:00Z", Text: &userText, PromptIDs: []string{receipt.InputID}, Placement: "opener",
				Origin: json.RawMessage(`{"kind":"user"}`), Attachments: []json.RawMessage{}, Skills: []json.RawMessage{},
			},
			{
				Type: contracts.KimiConversationItemKindAssistantMessage, ItemID: "reply-1",
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				CreatedAt: "2026-11-18T00:00:00Z", Text: &assistantText,
			},
		},
	}
	releaseREST := make(chan struct{})
	enteredREST := make(chan struct{})
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/agents/main/turns"), fakeKapReply{
		BeforeReply: func() { close(enteredREST); <-releaseREST },
		Data:        map[string]any{"turns": []kimiConversationTurn{snapshot}, "has_more": false},
	})

	native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	native.conversationFrame(t, fmt.Sprintf(`{"type":"item.ended","turn_id":"t0","item":{"type":"user_message","item_id":%q,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Ask something.","prompt_ids":[%q],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}}`, receipt.InputID, receipt.InputID))
	native.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"assistant_message","item_id":"reply-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Exact answer."}}`)
	native.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","steps":[]}}`)

	<-enteredREST
	readsBeforeREST := counter.reads.Load()
	turnEndsBeforeREST := counter.turnEnds.Load()
	close(releaseREST)
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the exact reply lookup completes")

	assert.Equal(t, readsBeforeREST, counter.reads.Load(), "an unchanged REST snapshot must not re-read idempotency keys")
	assert.Equal(t, turnEndsBeforeREST, counter.turnEnds.Load(), "an unchanged REST snapshot must not re-project a duplicate turn end")
	turns := rig.sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
}

func TestKimiPublishExactReplyProjectsNewItemsFromRESTSnapshot(t *testing.T) {
	t.Parallel()
	rig, native := newKimiPublicPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "running"}
	}})
	receipt, err := rig.agent.SendInput("Ask something.", nil)
	require.NoError(t, err)

	userText := "Ask something."
	assistantText := "Recovered from REST."
	snapshot := kimiConversationTurn{
		kimiConversationTurnHeader: kimiConversationTurnHeader{
			TurnID:    "t0",
			Ordinal:   ptrInt64(0),
			Kind:      contracts.KimiConversationTurnKindAgent,
			Status:    contracts.KimiConversationStatusEnded,
			Outcome:   contracts.KimiConversationOutcomeCompleted,
			StartedAt: "2026-11-18T00:00:00Z",
			EndedAt:   "2026-11-18T00:00:01Z",
			Steps:     []kimiConversationStep{},
		},
		Items: []kimiConversationItem{
			{
				Type: contracts.KimiConversationItemKindUserMessage, ItemID: receipt.InputID,
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				CreatedAt: "2026-11-18T00:00:00Z", Text: &userText, PromptIDs: []string{receipt.InputID}, Placement: "opener",
				Origin: json.RawMessage(`{"kind":"user"}`), Attachments: []json.RawMessage{}, Skills: []json.RawMessage{},
			},
			{
				Type: contracts.KimiConversationItemKindAssistantMessage, ItemID: "rest-only",
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				CreatedAt: "2026-11-18T00:00:00Z", Text: &assistantText,
			},
		},
	}
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/agents/main/turns"), fakeKapReply{
		Data: map[string]any{"turns": []kimiConversationTurn{snapshot}, "has_more": false},
	})

	// Live stream missed the assistant item; REST snapshot supplies it.
	native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	native.conversationFrame(t, fmt.Sprintf(`{"type":"item.ended","turn_id":"t0","item":{"type":"user_message","item_id":%q,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Ask something.","prompt_ids":[%q],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}}`, receipt.InputID, receipt.InputID))
	native.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","steps":[]}}`)

	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the exact reply lookup completes")
	turns := rig.sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, int64(2), turns[0].Items[0].Ordinal)
	var found bool
	for _, msg := range rig.sink.Messages() {
		if text, ok := (kimiProvider{}).ReplyText(agent.MessageContent{Original: msg.Content}); ok && text == assistantText {
			found = true
		}
	}
	assert.True(t, found, "new items introduced by the REST snapshot must still be projected")
}

func ptrInt64(v int64) *int64 { return &v }

func TestKimiQueueOutputWriteTracksDrainsAndRejectsNilInputs(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineKimiAgent(t, sink)

	// Nil queue, nil callback, and nil-queue reply sink return errors without tracking.
	require.Error(t, a.queueOutputWrite(nil, "nil queue", func() error { return nil }))
	secondary := agent.OutputQueueFor(agent.NewProviderServices(&agenttest.Sink{}))
	require.NotNil(t, secondary)
	require.Error(t, a.queueOutputWrite(secondary, "nil write", nil))
	require.Error(t, a.queueKimiReplyWrite(nil, "nil reply sink", func(agent.ProviderServices) error { return nil }))
	assert.Empty(t, a.snapshotPublicationQueues())

	// While dispatchMu is held, a secondary queue write stays pending and tracked,
	// then drains and untracks when finishDispatch runs.
	var ran atomic.Bool
	a.dispatchMu.Lock()
	err := a.queueOutputWrite(secondary, "deferred secondary write", func() error {
		ran.Store(true)
		return nil
	})
	var pendingErr *agent.PendingOutputError
	require.ErrorAs(t, err, &pendingErr)
	assert.False(t, ran.Load())
	assert.Contains(t, a.snapshotPublicationQueues(), secondary)
	a.finishDispatch()
	assert.True(t, ran.Load(), "finishDispatch must flush tracked secondary queues")
	assert.Empty(t, a.snapshotPublicationQueues(), "idle secondary queue must be untracked after drain")

	// When another owner is at the head of the secondary queue, flushQueuedOutput
	// leaves it tracked; once that head drains while sessionMu is held, our write
	// becomes head and finishSessionMutation flushes and untracks the queue.
	blockerOwner := agent.NewOutputOwner()
	var releaseBlocker, secondRan atomic.Bool
	secondary.Append(agent.OutputWrite{Owner: blockerOwner, Label: "hold secondary", Run: func() error {
		if !releaseBlocker.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})
	a.sessionMu.Lock()
	err = a.queueOutputWrite(secondary, "blocked secondary write", func() error {
		secondRan.Store(true)
		return nil
	})
	require.ErrorAs(t, err, &pendingErr)
	a.flushQueuedOutput()
	assert.False(t, secondRan.Load())
	assert.Contains(t, a.snapshotPublicationQueues(), secondary, "blocked queue stays tracked until its owner drains")

	releaseBlocker.Store(true)
	require.ErrorIs(t, secondary.FlushOwnerAfterReady(blockerOwner), agent.ErrOutputInFlight)
	assert.False(t, secondRan.Load(), "write still waits while sessionMu is held")
	assert.Contains(t, a.snapshotPublicationQueues(), secondary)

	a.finishSessionMutation()
	assert.True(t, secondRan.Load(), "finishSessionMutation must flush the now-head write on the tracked secondary queue")
	assert.Empty(t, a.snapshotPublicationQueues())
}

func TestKimiPublishExactReplyProjectsWhenPublicationNotCompleted(t *testing.T) {
	t.Parallel()
	rig, native := newKimiPublicPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "running"}
	}})
	receipt, err := rig.agent.SendInput("Ask something.", nil)
	require.NoError(t, err)

	userText := "Ask something."
	assistantText := "Exact answer."
	snapshot := kimiConversationTurn{
		kimiConversationTurnHeader: kimiConversationTurnHeader{
			TurnID:    "t0",
			Ordinal:   ptrInt64(0),
			Kind:      contracts.KimiConversationTurnKindAgent,
			Status:    contracts.KimiConversationStatusEnded,
			Outcome:   contracts.KimiConversationOutcomeCompleted,
			StartedAt: "2026-11-18T00:00:00Z",
			EndedAt:   "2026-11-18T00:00:01Z",
			Steps:     []kimiConversationStep{},
		},
		Items: []kimiConversationItem{
			{
				Type: contracts.KimiConversationItemKindUserMessage, ItemID: receipt.InputID,
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				CreatedAt: "2026-11-18T00:00:00Z", Text: &userText, PromptIDs: []string{receipt.InputID}, Placement: "opener",
				Origin: json.RawMessage(`{"kind":"user"}`), Attachments: []json.RawMessage{}, Skills: []json.RawMessage{},
			},
			{
				Type: contracts.KimiConversationItemKindAssistantMessage, ItemID: "reply-1",
				Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
				CreatedAt: "2026-11-18T00:00:00Z", Text: &assistantText,
			},
		},
	}

	// Deliver turn frames without a REST reply so publication is populated, then
	// simulate an uncompleted publication with released item bodies matching snapshot.
	native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	native.conversationFrame(t, fmt.Sprintf(`{"type":"item.ended","turn_id":"t0","item":{"type":"user_message","item_id":%q,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Ask something.","prompt_ids":[%q],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}}`, receipt.InputID, receipt.InputID))
	native.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"assistant_message","item_id":"reply-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Exact answer."}}`)

	rig.agent.dispatchMu.Lock()
	run := rig.agent.run(kimiMainAgentID)
	turn := run.conversation.document.turns["t0"]
	turn.replaceHeader(snapshot.kimiConversationTurnHeader)
	for _, item := range turn.items {
		item.releaseBody()
	}
	pub := run.conversation.turns["t0"]
	require.False(t, pub.completed)
	require.NoError(t, rig.agent.publishExactReply(run, pub, rig.sessionID(), snapshot))
	assert.True(t, pub.completed, "an uncompleted publication must still project even when snapshot matches document")
	rig.agent.finishDispatch()
	awaitKimiTestOutput(t, rig.agent.sink)

	turns := rig.sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
}
