package zcode

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// A snapshot that reports a sequence past the dispatch cursor reveals a gap
// the live stream never announced. The watcher completes replay before it
// applies the read observation, so the undelivered events cannot be discarded.
func TestZCodeTurnRecovery_ReplaysASnapshotGapBeforeApplyingTheObservation(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.Mu.Lock()
	a.lastSeq = 7
	a.Mu.Unlock()
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}

	type result struct {
		settled bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		settled, err := a.checkZCodeTurnRecovery(context.Background(), turn, state)
		done <- result{settled, err}
	}()

	read := waitZCodeRequest(t, stdin, MethodSessionRead)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(
		`{"runtime":{"eventSeq":9,"stateRevision":9,"activeTurnId":"turn-1"},"projection":{"contextUsed":100,"turnCount":1,"currentTurnId":"turn-1"}}`)))
	// The gap the snapshot reports is fetched before the observation applies.
	fetch := waitZCodeRequest(t, stdin, MethodSessionEvents)
	var params struct {
		AfterSeq int64 `json:"afterSeq"`
	}
	require.NoError(t, json.Unmarshal(fetch.Params, &params))
	assert.Equal(t, int64(7), params.AfterSeq, "the fetch asks for the gap the snapshot reports")
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, fetch), zcodeEventsReply(t, 9,
		zcodeFetchedEvent(t, 8, contracts.ZCodeEventSessionTitleUpdated, ""),
		zcodeFetchedEvent(t, 9, contracts.ZCodeEventSessionTitleUpdated, ""),
	)))
	res := <-done
	require.NoError(t, res.err)
	assert.False(t, res.settled)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, int64(9), a.lastSeq, "the gap dispatches before the observation applies")
	assert.Equal(t, int64(100), state.lastWatermark.contextUsed, "the observation still folds")
}

// A gap the replay cannot close leaves the events undelivered rather than
// discarded: the read observation waits and the cursor stays put.
func TestZCodeTurnRecovery_ANewerSnapshotCannotDiscardUndeliveredEvents(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	a.Mu.Lock()
	a.lastSeq = 7
	a.Mu.Unlock()
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}

	type result struct {
		settled bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		settled, err := a.checkZCodeTurnRecovery(context.Background(), turn, state)
		done <- result{settled, err}
	}()

	read := waitZCodeRequest(t, stdin, MethodSessionRead)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, read), json.RawMessage(
		`{"runtime":{"eventSeq":55,"stateRevision":9,"activeTurnId":"turn-1"},"projection":{"contextUsed":100,"turnCount":1,"currentTurnId":"turn-1"}}`)))
	fetch := waitZCodeRequest(t, stdin, MethodSessionEvents)
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, fetch), -32603, "temporary failure"))
	res := <-done
	require.Error(t, res.err, "an unclosable gap must fail the check instead of applying over it")
	assert.False(t, res.settled)
	a.HandleOutput(zcodeEventLine(t, 8, contracts.ZCodeEventTurnStarted, `{"turnId":"later"}`))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, int64(8), a.lastSeq, "the undelivered events must not be discarded")
	assert.Zero(t, state.lastWatermark.contextUsed, "the observation must not apply over a gap")
}

// Accepted-but-not-started input creates a provisional watcher. A native
// turn.started adopts the native ID while preserving the owner.
func TestZCodeTurnRecovery_ProvisionalOwnerAdoptsNativeTurnID(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	// Send input and get an accepted receipt.
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":5}`)
	require.NoError(t, a.SendInput("hello", nil))

	// The provisional owner exists with a synthetic ID.
	a.Mu.Lock()
	require.Equal(t, 1, len(a.turns), "a provisional owner must exist")
	var provisional *zcodeTurnState
	for _, turn := range a.turns {
		provisional = turn
	}
	assert.True(t, provisional.id == "" || provisional.id[0] == '#', "the provisional owner has no native ID yet")
	assert.False(t, provisional.settled)
	a.Mu.Unlock()

	// A turn.started adopts the native ID.
	a.HandleOutput(zcodeTurnEventLine(t, 1, "native-turn-1", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":1,"input":"hello","inputId":"`+provisional.inputID+`"}`))

	a.Mu.Lock()
	owner := a.turns["native-turn-1"]
	a.Mu.Unlock()
	require.NotNil(t, owner, "the native ID must be adopted")
	assert.Equal(t, provisional.generation, owner.generation, "the generation must be preserved")
	assert.Equal(t, provisional.inputID, owner.inputID)
}

// A session/read snapshot with runtime.activeTurnId lets a provisional owner
// adopt the native ID when no turn.started arrives.
func TestZCodeTurnRecovery_ActiveTurnIDFromSnapshotAdoptsProvisionalOwner(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":5}`)
	require.NoError(t, a.SendInput("hello", nil))

	a.Mu.Lock()
	var provisional *zcodeTurnState
	for _, turn := range a.turns {
		provisional = turn
	}
	id := provisional.id
	a.Mu.Unlock()

	// Simulate the watcher's session/read.
	a.Mu.Lock()
	state := a.recoverWatchers[id]
	a.Mu.Unlock()
	require.NotNil(t, state, "the watcher must be running")

	snap := zcodeSessionReadSnapshot{
		Runtime: &zcodeRuntimeState{
			ActiveTurnID:  "native-turn-1",
			StateRevision: 10,
		},
		Projection: &zcodeProjectionState{TurnCount: 5, CurrentTurnID: "native-turn-1"},
	}
	settled, err := a.applyZCodeRecoverySnapshot(provisional, state, snap)
	require.NoError(t, err)
	assert.False(t, settled)

	a.Mu.Lock()
	owner := a.turns["native-turn-1"]
	a.Mu.Unlock()
	require.NotNil(t, owner, "the provisional owner must adopt the native ID")
	assert.Equal(t, provisional.inputID, owner.inputID)
}

// A resumed owner with runtime.activeTurnId produces an owner without
// replaying unanchored history.
func TestZCodeTurnRecovery_ResumedActiveTurnGetsOwnerWithoutHistory(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	snap := zcodeStateSnapshot{}
	snap.Session.SessionID = "sess-1"
	snap.Runtime.ActiveTurnID = "resumed-turn"
	snap.Runtime.StateRevision = 20
	snap.Projection.TurnCount = 12

	a.applyCompleteSessionSnapshot(snap)

	a.Mu.Lock()
	owner := a.turns["resumed-turn"]
	fg := a.foregroundTurnID
	a.Mu.Unlock()
	require.NotNil(t, owner, "the resumed active turn must have an owner")
	assert.Equal(t, "resumed-turn", fg)
	assert.Equal(t, 12, owner.baselineTurnCount)
	assert.Empty(t, owner.contentByScope, "no historical content is emitted before anchoring")
	assert.Empty(t, owner.anchorMessageID, "no anchor until replay or a native start establishes one")
}

// A quiet progress advancing the projection watermark proves liveness.
func TestZCodeTurnRecovery_QuietProgressAdvancesTheWatermark(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}

	// First observation sets the watermark.
	snap1 := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{ActiveTurnID: "turn-1"},
		Projection: &zcodeProjectionState{ContextUsed: 100, TotalTokenCount: 500, TurnCount: 1, CurrentTurnID: "turn-1"},
	}
	settled, err := a.applyZCodeRecoverySnapshot(turn, state, snap1)
	require.NoError(t, err)
	assert.False(t, settled)
	assert.Equal(t, int64(100), state.lastWatermark.contextUsed)
	assert.Equal(t, int64(500), state.lastWatermark.totalTokenCount)

	// Second observation with advancing watermarks: still alive.
	snap2 := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{ActiveTurnID: "turn-1"},
		Projection: &zcodeProjectionState{ContextUsed: 200, TotalTokenCount: 800, TurnCount: 2, CurrentTurnID: "turn-1"},
	}
	settled, err = a.applyZCodeRecoverySnapshot(turn, state, snap2)
	require.NoError(t, err)
	assert.False(t, settled, "advancing watermarks prove liveness")
	assert.Equal(t, int64(200), state.lastWatermark.contextUsed)
}

// An active tool on the owner prevents local settlement even with two idle
// snapshots and an attributed boundary.
func TestZCodeTurnRecovery_ActiveToolPreventsSettlement(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{},
		toolCallIDs: map[string]struct{}{"tool-1": {}},
	}
	state := &zcodeTurnRecoveryState{
		idleSnapshots: 2,
		boundary: &zcodePromptBoundaryEvidence{
			reason: zcodeStateReasonPromptCompleted, raw: []byte(`{}`),
		},
	}
	// Seed an open tool call.
	a.Mu.Lock()
	a.toolCalls["tool-1"] = &zcodeToolCall{
		name: "Bash", lastFrame: []byte(`{}`),
	}
	a.Mu.Unlock()

	snap := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{},
		Projection: &zcodeProjectionState{ContextUsed: 100, TurnCount: 1},
	}
	settled, err := a.applyZCodeRecoverySnapshot(turn, state, snap)
	require.NoError(t, err)
	assert.False(t, settled, "an active tool must prevent settlement")
}

// Two idle snapshots without an attributed boundary do not settle the turn.
func TestZCodeTurnRecovery_TwoIdleSnapshotsWithoutBoundaryDoNotSettle(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{idleSnapshots: 2}

	snap := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{},
		Projection: &zcodeProjectionState{},
	}
	settled, err := a.applyZCodeRecoverySnapshot(turn, state, snap)
	require.NoError(t, err)
	assert.False(t, settled, "two idle snapshots alone establish only inactivity")
}

// A delayed boundary from turn A after turn B starts is not assigned to B.
func TestZCodeTurnRecovery_DelayedBoundaryIsNotAssignedToTheNewOwner(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	a.Mu.Lock()
	if a.turns == nil {
		a.turns = map[string]*zcodeTurnState{}
	}
	turnA := &zcodeTurnState{
		id: "turn-a", baselineTurnCount: 0,
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
		admissionRevision: 5,
	}
	turnB := &zcodeTurnState{
		id: "turn-b", baselineTurnCount: 10,
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
		admissionRevision: 15,
	}
	a.turns["turn-a"] = turnA
	a.turns["turn-b"] = turnB
	a.foregroundTurnID = "turn-b"
	a.Mu.Unlock()

	// The boundary names turn A. It must not be attributed to turn B.
	projection := zcodeProjectionState{CurrentTurnID: "turn-a", TurnCount: 1}
	a.Mu.Lock()
	evidenceA := a.attributeZCodePromptBoundaryLocked(turnA, zcodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	evidenceB := a.attributeZCodePromptBoundaryLocked(turnB, zcodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()

	assert.NotNil(t, evidenceA, "the boundary names turn A")
	assert.Nil(t, evidenceB, "the boundary must not be attributed to turn B")
}

// A background boundary does not settle the foreground watcher.
func TestZCodeTurnRecovery_BackgroundBoundaryDoesNotSettleForeground(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	a.Mu.Lock()
	if a.turns == nil {
		a.turns = map[string]*zcodeTurnState{}
	}
	fg := &zcodeTurnState{
		id: "fg-turn", baselineTurnCount: 0,
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	bg := &zcodeTurnState{
		id: "bg-turn", background: true, baselineTurnCount: 0,
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	a.turns["fg-turn"] = fg
	a.turns["bg-turn"] = bg
	a.foregroundTurnID = "fg-turn"
	a.Mu.Unlock()

	// The boundary names the background turn.
	projection := zcodeProjectionState{CurrentTurnID: "bg-turn", TurnCount: 1}
	a.Mu.Lock()
	evidence := a.attributeZCodePromptBoundaryLocked(bg, zcodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	evidenceFG := a.attributeZCodePromptBoundaryLocked(fg, zcodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()

	assert.NotNil(t, evidence, "the background turn can hold its own boundary")
	assert.Nil(t, evidenceFG, "a background boundary must not attribute to the foreground owner")
}

// Non-prefix rewrite in reconciliation is a conflict.
func TestZCodeTurnRecovery_NonPrefixRewriteReportsConflict(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1",
		contentByScope: map[string]string{
			zcodeTextScope("msg-1"): "hello world",
		},
		toolCallIDs: map[string]struct{}{},
	}

	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	err := a.emitZCodeRecoveredScopeLocked(turn, zcodeTextScope("msg-1"), "goodbye world")
	require.Error(t, err, "a non-prefix rewrite must be a conflict")
	assert.Contains(t, err.Error(), "reconciliation conflict")
}

// Multiple parts aggregate into message scopes. Already-persisted content is
// not duplicated.
func TestZCodeTurnRecovery_MultiplePartsAggregateAndDoNotDuplicate(t *testing.T) {
	t.Parallel()
	emitted := map[string][]string{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.recoverEmitContent = func(_ *zcodeTurnState, scopeID, content string) {
		emitted[scopeID] = append(emitted[scopeID], content)
	}
	turn := &zcodeTurnState{
		id: "turn-1",
		contentByScope: map[string]string{
			zcodeTextScope("msg-1"): "hello ",
		},
		toolCallIDs: map[string]struct{}{},
	}

	messages := []zcodeNativeMessage{
		{
			Info: zcodeNativeMessageInfo{ID: "msg-1", Role: "assistant"},
			Parts: []zcodeNativePart{
				{ID: "p1", Type: zcodeNativePartTypeText, Text: "hello "},
				{ID: "p2", Type: zcodeNativePartTypeText, Text: "world"},
				{ID: "p3", Type: zcodeNativePartTypeReasoning, Text: "thinking"},
			},
		},
	}

	a.dispatchMu.Lock()
	err := a.applyZCodeRecoveredMessagesLocked(turn, messages)
	a.dispatchMu.Unlock()
	require.NoError(t, err)

	textScope := zcodeTextScope("msg-1")
	reasoningScope := zcodeReasoningScope("msg-1")
	require.Len(t, emitted[textScope], 1, "only the missing suffix must be emitted")
	assert.Equal(t, "world", emitted[textScope][0], "the retained prefix must not be re-emitted")
	require.Len(t, emitted[reasoningScope], 1, "the reasoning aggregate must be emitted")
	assert.Equal(t, "thinking", emitted[reasoningScope][0])
}

// A 200-message truncated window triggers the unlimited read.
func TestZCodeTurnRecovery_TruncatedWindowTriggersUnlimitedRead(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	turn := &zcodeTurnState{
		id: "turn-1", anchorMessageID: "anchor",
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}

	// First read returns 200 messages without the anchor.
	fullMessages := make([]zcodeNativeMessage, zcodeRecoveredMessageWindow)
	for i := range fullMessages {
		fullMessages[i] = zcodeNativeMessage{
			Info: zcodeNativeMessageInfo{ID: "other-" + string(rune('a'+i%26)), Role: "assistant"},
		}
	}
	firstResp := zcodeMessagesResponse(t, fullMessages)

	// Second read (unlimited) returns messages including the anchor.
	allMessages := append([]zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "anchor", Role: "user"}},
	}, fullMessages...)
	secondResp := zcodeMessagesResponse(t, allMessages)

	// Answer two sequential session/messages requests.
	go func() {
		req := waitZCodeRequestNumber(t, stdin, MethodSessionMessages, 0)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(firstResp)))
	}()
	go func() {
		req := waitZCodeRequestNumber(t, stdin, MethodSessionMessages, 1)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(secondResp)))
	}()

	require.NoError(t, a.reconcileZCodeFinalMessages(context.Background(), turn))

	// The unlimited read was used: two requests were made.
	requests := stdin.Requests(t)
	sessionMessagesCount := 0
	for _, req := range requests {
		if req.Method == MethodSessionMessages {
			sessionMessagesCount++
		}
	}
	assert.Equal(t, 2, sessionMessagesCount, "the truncated window must trigger the unlimited read")
}

// An absent anchor after the full read fails reconciliation.
func TestZCodeTurnRecovery_AbsentAnchorAfterFullReadFailsReconciliation(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	turn := &zcodeTurnState{
		id: "turn-1", anchorMessageID: "anchor",
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}

	fullMessages := make([]zcodeNativeMessage, zcodeRecoveredMessageWindow)
	for i := range fullMessages {
		fullMessages[i] = zcodeNativeMessage{
			Info: zcodeNativeMessageInfo{ID: "other", Role: "assistant"},
		}
	}
	firstResp := zcodeMessagesResponse(t, fullMessages)
	secondResp := zcodeMessagesResponse(t, fullMessages) // still no anchor

	go func() {
		req := waitZCodeRequestNumber(t, stdin, MethodSessionMessages, 0)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(firstResp)))
	}()
	go func() {
		req := waitZCodeRequestNumber(t, stdin, MethodSessionMessages, 1)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(secondResp)))
	}()

	err := a.reconcileZCodeFinalMessages(context.Background(), turn)
	require.Error(t, err, "an absent anchor after the full read must fail reconciliation")
	assert.Contains(t, err.Error(), "anchor")
}

// A frozen watermark past the freeze limit reaches the error path.
func TestZCodeTurnRecovery_FrozenWatermarkReachesTheErrorPath(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}

	// First observation establishes the watermark and the freeze start.
	snap := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{ActiveTurnID: "turn-1"},
		Projection: &zcodeProjectionState{ContextUsed: 100, TurnCount: 1},
	}
	settled, err := a.applyZCodeRecoverySnapshot(turn, state, snap)
	require.NoError(t, err)
	assert.False(t, settled)

	// A frozen observation after the freeze limit.
	state.frozenSince = a.Clock().Now().Add(-zcodeTurnRecoveryFreezeLimit - time.Minute)
	settled, err = a.applyZCodeRecoverySnapshot(turn, state, snap)
	require.NoError(t, err)
	assert.True(t, settled, "a frozen watermark past the limit must reach the error path")
}

// Context replacement cancels pending RPCs and prevents late settlement.
func TestZCodeTurnRecovery_ContextReplacementCancelsWatchers(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	a.Mu.Lock()
	if a.turns == nil {
		a.turns = map[string]*zcodeTurnState{}
	}
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	a.turns["turn-1"] = turn
	a.foregroundTurnID = "turn-1"
	a.refreshTurnActiveLocked()
	a.startZCodeTurnWatcherLocked(turn)
	a.Mu.Unlock()

	a.Mu.Lock()
	watcher := a.recoverWatchers["turn-1"]
	a.Mu.Unlock()
	require.NotNil(t, watcher, "the watcher must be running")

	// Stop all watchers (as ClearContext does).
	a.stopAllZCodeTurnWatchers()

	a.Mu.Lock()
	watcher = a.recoverWatchers["turn-1"]
	a.Mu.Unlock()
	assert.Nil(t, watcher, "the watcher must be removed")
}

// Live output during reconciliation serializes comparison and emission under
// dispatchMu.
func TestZCodeTurnRecovery_ReconciliationSerializesWithLiveOutput(t *testing.T) {
	t.Parallel()
	emitted := map[string][]string{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.recoverEmitContent = func(_ *zcodeTurnState, scopeID, content string) {
		emitted[scopeID] = append(emitted[scopeID], content)
	}
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]string{},
		toolCallIDs: map[string]struct{}{},
	}

	messages := []zcodeNativeMessage{
		{
			Info:  zcodeNativeMessageInfo{ID: "msg-1", Role: "assistant"},
			Parts: []zcodeNativePart{{ID: "p1", Type: zcodeNativePartTypeText, Text: "hello"}},
		},
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.dispatchMu.Lock()
		_ = a.applyZCodeRecoveredMessagesLocked(turn, messages)
		a.dispatchMu.Unlock()
	}()
	go func() {
		defer wg.Done()
		a.dispatchMu.Lock()
		a.dispatchZCodeEventLocked(zcodeTestSequencedEvent(0, contracts.ZCodeEventSessionTitleUpdated, `{}`))
		a.dispatchMu.Unlock()
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("reconciliation and live output deadlocked")
	}

	assert.Len(t, emitted[zcodeTextScope("msg-1")], 1, "emission must happen exactly once")
}

// An ID-less boundary is usable for one unambiguous unsettled owner.
func TestZCodeTurnRecovery_IDlessBoundaryForSoleUnsettledOwner(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))

	a.Mu.Lock()
	if a.turns == nil {
		a.turns = map[string]*zcodeTurnState{}
	}
	sole := &zcodeTurnState{
		id: "turn-1", baselineTurnCount: 0, admissionRevision: 5,
		contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	a.turns["turn-1"] = sole
	a.Mu.Unlock()

	projection := zcodeProjectionState{TurnCount: 1}
	a.Mu.Lock()
	evidence := a.attributeZCodePromptBoundaryForUnsettledLocked(
		zcodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()
	assert.NotNil(t, evidence, "the sole unsettled owner takes the ID-less boundary")

	// Two unsettled owners: ambiguous, no attribution.
	a.Mu.Lock()
	second := &zcodeTurnState{
		id: "turn-2", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
	}
	a.turns["turn-2"] = second
	a.Mu.Unlock()
	a.Mu.Lock()
	evidence = a.attributeZCodePromptBoundaryForUnsettledLocked(
		zcodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()
	assert.Nil(t, evidence, "two unsettled owners make the ID-less boundary ambiguous")
}

// selectZCodeOwnerMessages picks the final-message reconciliation window of one
// owner: the messages after its anchor, or -- for a provisional accepted turn
// with no anchor -- the assistant messages created at or after the acceptance.
// A resumed owner without an anchor reconciles nothing until a replay
// establishes one.
func TestZCodeTurnRecovery_SelectsOnlyTheOwnersOwnMessages(t *testing.T) {
	t.Parallel()

	t.Run("a provisional owner takes assistant messages from its acceptance on", func(t *testing.T) {
		t.Parallel()
		owner := &zcodeTurnState{
			id: "turn-1", acceptedAt: time.UnixMilli(1700000000000),
			contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
		}
		messages := []zcodeNativeMessage{
			{Info: zcodeNativeMessageInfo{ID: "early", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1699999999.999}}},
			{Info: zcodeNativeMessageInfo{ID: "boundary", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1700000000.0}}},
			{Info: zcodeNativeMessageInfo{ID: "user-echo", Role: "user", Time: zcodeNativeMessageTime{Created: 1700000001.0}}},
			{Info: zcodeNativeMessageInfo{ID: "late", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1700000002.5}}},
		}
		selected, truncated := selectZCodeOwnerMessages(owner, messages)
		require.False(t, truncated, "the no-anchor selection never truncates")
		require.Len(t, selected, 2, "the earlier assistant message and the user message are not the turn's")
		assert.Equal(t, "boundary", selected[0].Info.ID, "a message created exactly at the acceptance belongs to the turn")
		assert.Equal(t, "late", selected[1].Info.ID)
	})

	t.Run("a resumed owner without an anchor reconciles nothing", func(t *testing.T) {
		t.Parallel()
		owner := &zcodeTurnState{
			id: "turn-1", contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
		}
		messages := []zcodeNativeMessage{
			{Info: zcodeNativeMessageInfo{ID: "m1", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1700000002.5}}},
		}
		selected, truncated := selectZCodeOwnerMessages(owner, messages)
		assert.Empty(t, selected, "an unanchored resumed owner must wait for the anchor")
		assert.False(t, truncated)
	})

	t.Run("only messages after the anchor belong to the turn", func(t *testing.T) {
		t.Parallel()
		owner := &zcodeTurnState{
			id: "turn-1", anchorMessageID: "anchor",
			contentByScope: map[string]string{}, toolCallIDs: map[string]struct{}{},
		}
		messages := []zcodeNativeMessage{
			{Info: zcodeNativeMessageInfo{ID: "before", Role: "assistant"}},
			{Info: zcodeNativeMessageInfo{ID: "anchor", Role: "user"}},
			{Info: zcodeNativeMessageInfo{ID: "after-1", Role: "assistant"}},
			{Info: zcodeNativeMessageInfo{ID: "after-2", Role: "assistant"}},
		}
		selected, truncated := selectZCodeOwnerMessages(owner, messages)
		require.False(t, truncated)
		require.Len(t, selected, 2)
		assert.Equal(t, "after-1", selected[0].Info.ID, "the anchor itself is not the turn's message")
		assert.Equal(t, "after-2", selected[1].Info.ID)
	})
}

// Helpers.

func zcodeMessagesResponse(t *testing.T, messages []zcodeNativeMessage) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	return string(data)
}
