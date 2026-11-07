package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}
	registerZCodeRecoveryForTest(a, turn, state)

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
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}
	registerZCodeRecoveryForTest(a, turn, state)

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
	a.Mu.Lock()
	watcher := owner.recovery
	a.Mu.Unlock()
	require.NotNil(t, watcher, "native adoption must retain the same recovery watcher")
	a.HandleOutput(zcodeTurnEventLine(t, 2, "native-turn-1", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	a.Mu.Lock()
	assert.Nil(t, owner.recovery, "native settlement must cancel and detach recovery")
	a.Mu.Unlock()
}

func TestZCodeTurnRecovery_ProvisionalSnapshotRequiresMatchingNativeInput(t *testing.T) {
	t.Parallel()
	for _, matching := range []bool{false, true} {
		t.Run(fmt.Sprintf("matching input %t", matching), func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
			a.createProvisionalZCodeTurn("sess-1", "input-a", 5)
			a.Mu.Lock()
			owner := a.turns[a.foregroundTurnID]
			state := owner.recovery
			a.Mu.Unlock()
			input := "input-b"
			if matching {
				input = "input-a"
			}
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method == MethodSessionMessages {
					return json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user","anchor":{"turnId":"native-turn","sourceCommandId":"` + input + `"}}}]}`)
				}
				return nil
			})
			snap := zcodeSessionReadSnapshot{Runtime: &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "native-turn", StateRevision: 10}, Projection: &zcodeProjectionState{TurnCount: 5, CurrentTurnID: "native-turn"}}
			settled, err := a.applyZCodeRecoverySnapshot(owner, state, snap)
			require.NoError(t, err)
			assert.False(t, settled)
			a.Mu.Lock()
			defer a.Mu.Unlock()
			if matching {
				assert.Same(t, owner, a.turns["native-turn"])
				assert.Equal(t, "user", owner.anchorMessageID)
			} else {
				assert.Nil(t, a.turns["native-turn"], "another active turn does not prove ownership of queued input")
			}
			assert.Same(t, state, owner.recovery)
		})
	}
}

func TestZCodeTurnRecovery_ObsoleteSnapshotCannotReinsertAnOwner(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	old := &zcodeTurnState{id: "#turn-old", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{}}
	state := &zcodeTurnRecoveryState{}
	a.Mu.Lock()
	a.turns = map[string]*zcodeTurnState{old.id: old}
	old.recovery = state
	state.sessionID = a.sessionID
	a.foregroundTurnID = old.id
	a.refreshTurnActiveLocked()
	a.Mu.Unlock()
	a.stopAllZCodeTurnWatchers()
	a.Mu.Lock()
	a.clearZCodeTurnsLocked()
	a.sessionID = "replacement"
	current := &zcodeTurnState{id: "current", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{}}
	a.turns[current.id] = current
	a.foregroundTurnID = current.id
	a.refreshTurnActiveLocked()
	a.Mu.Unlock()
	finished, err := a.applyZCodeRecoverySnapshot(old, state, zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "old-native"},
		Projection: &zcodeProjectionState{CurrentTurnID: "old-native", TurnCount: 1},
	})
	require.NoError(t, err)
	assert.True(t, finished, "a retired watcher must stop rather than mutate the replacement")
	a.Mu.Lock()
	assert.Equal(t, map[string]*zcodeTurnState{"current": current}, a.turns)
	assert.Equal(t, "current", a.foregroundTurnID)
	a.Mu.Unlock()
	assert.Zero(t, sink.MessageCount())
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
	assert.Empty(t, owner.contentByScope, "no historical content is emitted before anchoring")
	assert.Empty(t, owner.anchorMessageID, "no anchor until replay or a native start establishes one")
}

// A quiet progress advancing the projection watermark proves liveness.
func TestZCodeTurnRecovery_QuietProgressAdvancesTheWatermark(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}
	registerZCodeRecoveryForTest(a, turn, state)

	// First observation sets the watermark.
	snap1 := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "turn-1"},
		Projection: &zcodeProjectionState{ContextUsed: 100, TotalTokenCount: 500, TurnCount: 1, CurrentTurnID: "turn-1"},
	}
	settled, err := a.applyZCodeRecoverySnapshot(turn, state, snap1)
	require.NoError(t, err)
	assert.False(t, settled)
	assert.Equal(t, int64(100), state.lastWatermark.contextUsed)
	assert.Equal(t, int64(500), state.lastWatermark.totalTokenCount)

	// Second observation with advancing watermarks: still alive.
	snap2 := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "turn-1"},
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
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{},
		toolInvocationIDs: map[string]struct{}{"tool-1": {}},
	}
	state := &zcodeTurnRecoveryState{
		idleSnapshots: 2,
		boundary: &zcodePromptBoundaryEvidence{
			reason: contracts.ZCodeStateReasonPromptCompleted, raw: []byte(`{}`),
		},
	}
	registerZCodeRecoveryForTest(a, turn, state)
	// Seed an open tool call.
	a.Mu.Lock()
	a.toolInvocations["tool-1"] = &zcodeToolInvocation{spanID: "tool-1", nativeCallID: "tool-1",
		name: "Bash", lastFrame: []byte(`{}`),
	}
	a.Mu.Unlock()

	snap := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true},
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
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{idleSnapshots: 2}
	registerZCodeRecoveryForTest(a, turn, state)

	snap := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true},
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
		id: "turn-a", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
		admissionRevision: 5,
	}
	turnB := &zcodeTurnState{
		id: "turn-b", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
		admissionRevision: 15,
	}
	a.turns["turn-a"] = turnA
	a.turns["turn-b"] = turnB
	a.foregroundTurnID = "turn-b"
	a.Mu.Unlock()

	// The boundary identifies turn A. It must not be attributed to turn B.
	projection := zcodeProjectionState{CurrentTurnID: "turn-a", TurnCount: 1}
	a.Mu.Lock()
	evidenceA := a.attributeZCodePromptBoundaryLocked(turnA, contracts.ZCodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	evidenceB := a.attributeZCodePromptBoundaryLocked(turnB, contracts.ZCodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()

	assert.NotNil(t, evidenceA, "the boundary identifies turn A")
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
		id: "fg-turn", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	bg := &zcodeTurnState{
		id: "bg-turn", background: true, contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	a.turns["fg-turn"] = fg
	a.turns["bg-turn"] = bg
	a.foregroundTurnID = "fg-turn"
	a.Mu.Unlock()

	// The boundary identifies the background turn.
	projection := zcodeProjectionState{CurrentTurnID: "bg-turn", TurnCount: 1}
	a.Mu.Lock()
	evidence := a.attributeZCodePromptBoundaryLocked(bg, contracts.ZCodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	evidenceFG := a.attributeZCodePromptBoundaryLocked(fg, contracts.ZCodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
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
		contentByScope: map[string]*zcodeScopeContent{
			zcodeTextScope("msg-1"): {observed: []byte("hello world")},
		},
		toolInvocationIDs: map[string]struct{}{},
	}

	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	err := a.applyZCodeRecoveredMessagesLocked(turn, []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "msg-1", Role: "assistant"}, Parts: []zcodeNativePart{{Type: zcodeNativePartTypeText, Text: "goodbye world"}}}})
	require.Error(t, err, "a non-prefix rewrite must be a conflict")
	assert.Contains(t, err.Error(), "reconciliation conflict")
}

// Multiple parts aggregate into message scopes. Already-persisted content is
// not duplicated.
func TestZCodeTurnRecovery_MultiplePartsAggregateAndDoNotDuplicate(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1",
		contentByScope: map[string]*zcodeScopeContent{
			zcodeTextScope("msg-1"): {observed: []byte("hello "), submitted: len("hello ")},
		},
		toolInvocationIDs: map[string]struct{}{},
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
	textKind, text, exists := a.generationBuffer.ScopeSnapshot(textScope)
	require.True(t, exists)
	assert.Equal(t, agent.AssembledMessageKindText, textKind)
	assert.Equal(t, "world", text, "the persisted prefix must not be emitted again")
	reasoningKind, reasoning, exists := a.generationBuffer.ScopeSnapshot(reasoningScope)
	require.True(t, exists)
	assert.Equal(t, agent.AssembledMessageKindReasoning, reasoningKind)
	assert.Equal(t, "thinking", reasoning)
}

// A 200-message truncated window triggers the unlimited read.
func TestZCodeTurnRecovery_TruncatedWindowTriggersUnlimitedRead(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)

	turn := &zcodeTurnState{
		id: "turn-1", anchorMessageID: "anchor",
		contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
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

	_, err := a.fetchZCodeFinalMessages(context.Background(), "sess-1", turn)
	require.NoError(t, err)

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
		contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
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

	_, err := a.fetchZCodeFinalMessages(context.Background(), "sess-1", turn)
	require.Error(t, err, "an absent anchor after the full read must fail reconciliation")
	assert.Contains(t, err.Error(), "anchor")
}

// A frozen watermark past the freeze limit reaches the error path.
func TestZCodeTurnRecovery_FrozenWatermarkReachesTheErrorPath(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	state := &zcodeTurnRecoveryState{}
	registerZCodeRecoveryForTest(a, turn, state)

	// First observation establishes the watermark and the freeze start.
	snap := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "turn-1"},
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
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	a.turns["turn-1"] = turn
	a.foregroundTurnID = "turn-1"
	a.refreshTurnActiveLocked()
	a.startZCodeTurnWatcherLocked(turn)
	a.Mu.Unlock()

	a.Mu.Lock()
	watcher := turn.recovery
	a.Mu.Unlock()
	require.NotNil(t, watcher, "the watcher must be running")

	// Stop all watchers (as ClearContext does).
	a.stopAllZCodeTurnWatchers()

	a.Mu.Lock()
	watcher = turn.recovery
	a.Mu.Unlock()
	assert.Nil(t, watcher, "the watcher must be removed")
}

// Live output during reconciliation serializes comparison and emission under
// dispatchMu.
func TestZCodeTurnRecovery_ReconciliationSerializesWithLiveOutput(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	turn := &zcodeTurnState{
		id: "turn-1", contentByScope: map[string]*zcodeScopeContent{},
		toolInvocationIDs: map[string]struct{}{},
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

	_, text, exists := a.generationBuffer.ScopeSnapshot(zcodeTextScope("msg-1"))
	assert.True(t, exists)
	assert.Equal(t, "hello", text, "reconciliation must append exactly once")
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
		id: "turn-1", admissionRevision: 5,
		contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	a.turns["turn-1"] = sole
	a.Mu.Unlock()

	projection := zcodeProjectionState{TurnCount: 1}
	a.Mu.Lock()
	evidence := a.attributeZCodePromptBoundaryForSoleOwnerLocked(
		contracts.ZCodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()
	assert.NotNil(t, evidence, "the sole unsettled owner takes the ID-less boundary")

	// Two unsettled owners: ambiguous, no attribution.
	a.Mu.Lock()
	second := &zcodeTurnState{
		id: "turn-2", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
	}
	a.turns["turn-2"] = second
	a.Mu.Unlock()
	a.Mu.Lock()
	evidence = a.attributeZCodePromptBoundaryForSoleOwnerLocked(
		contracts.ZCodeStateReasonPromptCompleted, 5, projection, []byte(`{}`))
	a.Mu.Unlock()
	assert.Nil(t, evidence, "two unsettled owners make the ID-less boundary ambiguous")
}

// selectZCodeOwnerMessages picks the final-message reconciliation window of one
// owner: the messages after its anchor, or -- for a provisional accepted turn
// with no anchor -- the assistant messages created at or after the acceptance.
// A resumed owner without a user-message anchor needs an exact native turn anchor.
func TestZCodeTurnRecovery_SelectsOnlyTheOwnersOwnMessages(t *testing.T) {
	t.Parallel()

	t.Run("a provisional owner takes assistant messages from its acceptance on", func(t *testing.T) {
		t.Parallel()
		owner := &zcodeTurnState{
			id: "turn-1", acceptedAt: time.UnixMilli(1700000000000),
			contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
		}
		messages := []zcodeNativeMessage{
			{Info: zcodeNativeMessageInfo{ID: "early", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1699999999999}}},
			{Info: zcodeNativeMessageInfo{ID: "boundary", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1700000000000}}},
			{Info: zcodeNativeMessageInfo{ID: "user-echo", Role: "user", Time: zcodeNativeMessageTime{Created: 1700000001000}}},
			{Info: zcodeNativeMessageInfo{ID: "late", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1700000002500}}},
		}
		selected, truncated := selectZCodeOwnerMessages(owner, messages, false)
		require.False(t, truncated, "the no-anchor selection never truncates")
		require.Len(t, selected, 2, "the earlier assistant message and the user message are not the turn's")
		assert.Equal(t, "boundary", selected[0].Info.ID, "a message created exactly at the acceptance belongs to the turn")
		assert.Equal(t, "late", selected[1].Info.ID)
	})

	t.Run("a resumed owner without an anchor reconciles nothing", func(t *testing.T) {
		t.Parallel()
		owner := &zcodeTurnState{
			id: "turn-1", contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
		}
		messages := []zcodeNativeMessage{
			{Info: zcodeNativeMessageInfo{ID: "m1", Role: "assistant", Time: zcodeNativeMessageTime{Created: 1700000002500}}},
		}
		selected, truncated := selectZCodeOwnerMessages(owner, messages, false)
		assert.Empty(t, selected, "an unanchored resumed owner must wait for the anchor")
		assert.False(t, truncated)
	})

	t.Run("only messages after the anchor belong to the turn", func(t *testing.T) {
		t.Parallel()
		owner := &zcodeTurnState{
			id: "turn-1", anchorMessageID: "anchor",
			contentByScope: map[string]*zcodeScopeContent{}, toolInvocationIDs: map[string]struct{}{},
		}
		messages := []zcodeNativeMessage{
			{Info: zcodeNativeMessageInfo{ID: "before", Role: "assistant"}},
			{Info: zcodeNativeMessageInfo{ID: "anchor", Role: "user"}},
			{Info: zcodeNativeMessageInfo{ID: "after-1", Role: "assistant", ParentID: "anchor"}},
			{Info: zcodeNativeMessageInfo{ID: "after-2", Role: "assistant", ParentID: "anchor"}},
		}
		selected, truncated := selectZCodeOwnerMessages(owner, messages, false)
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

// registerZCodeRecoveryForTest supplies the owner and watcher identity a real admission establishes.
func registerZCodeRecoveryForTest(a *Agent, owner *zcodeTurnState, state *zcodeTurnRecoveryState) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.turns == nil {
		a.turns = make(map[string]*zcodeTurnState)
	}
	a.turns[owner.id] = owner
	owner.recovery = state
	state.sessionID = a.sessionID
	if !owner.background {
		a.foregroundTurnID = owner.id
	}
	a.refreshTurnActiveLocked()
}

func TestZCodeFinalMessageSelection_UsesNativeMillisecondTimestamps(t *testing.T) {
	t.Parallel()
	accepted := time.UnixMilli(1700000000000)
	owner := &zcodeTurnState{id: "turn-1", acceptedAt: accepted}
	messages := []zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "before", Role: "assistant", Time: zcodeNativeMessageTime{Created: float64(accepted.UnixMilli() - 1)}}},
		{Info: zcodeNativeMessageInfo{ID: "at", Role: "assistant", Time: zcodeNativeMessageTime{Created: float64(accepted.UnixMilli())}}},
	}
	selected, _ := selectZCodeOwnerMessages(owner, messages, false)
	require.Len(t, selected, 1)
	assert.Equal(t, "at", selected[0].Info.ID)
}

func TestZCodeFinalMessageSelection_RequiresAssistantOwnership(t *testing.T) {
	t.Parallel()
	owner := &zcodeTurnState{id: "turn-1", anchorMessageID: "user-1"}
	var messages []zcodeNativeMessage
	require.NoError(t, json.Unmarshal([]byte(`[
 {"info":{"id":"user-1","role":"user"}},
 {"info":{"id":"owned","role":"assistant","parentID":"user-1","anchor":{"turnId":"turn-1"},"semantics":{"kind":"assistant_response"}}},
 {"info":{"id":"other-user","role":"user"}},
 {"info":{"id":"foreign","role":"assistant","parentID":"other-user","anchor":{"turnId":"turn-2"}}},
 {"info":{"id":"timeline","role":"assistant","parentID":"user-1","anchor":{"turnId":"turn-1"},"semantics":{"kind":"timeline_event"}}}
 ]`), &messages))
	selected, _ := selectZCodeOwnerMessages(owner, messages, false)
	require.Len(t, selected, 1)
	assert.Equal(t, "owned", selected[0].Info.ID)
}

func TestZCodeFinalMessageSelection_MissingAnchorAlwaysRequiresAFullRead(t *testing.T) {
	t.Parallel()
	owner := &zcodeTurnState{id: "turn-1", anchorMessageID: "missing"}
	_, incomplete := selectZCodeOwnerMessages(owner, []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "other", Role: "assistant"}}}, false)
	assert.True(t, incomplete, "a short response still cannot establish the requested anchor")
}

func TestZCodeFinalMessageSelection_FullUnanchoredWindowRequiresAFullRead(t *testing.T) {
	t.Parallel()
	owner := &zcodeTurnState{id: "turn-1", acceptedAt: time.UnixMilli(1)}
	messages := make([]zcodeNativeMessage, zcodeRecoveredMessageWindow)
	for index := range messages {
		messages[index].Info = zcodeNativeMessageInfo{ID: "message", Role: "assistant", Time: zcodeNativeMessageTime{Created: 2}}
	}
	_, incomplete := selectZCodeOwnerMessages(owner, messages, false)
	assert.True(t, incomplete, "the newest tail may omit earlier messages of the accepted turn")
}

func TestZCodeTurnRecovery_ResumeDoesNotCreateANewAcceptanceTime(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	var snap zcodeStateSnapshot
	snap.Runtime.ActiveTurnID = "resumed"
	a.Mu.Lock()
	a.createResumedZCodeTurnLocked(snap)
	owner := a.turns["resumed"]
	a.Mu.Unlock()
	require.NotNil(t, owner)
	assert.True(t, owner.acceptedAt.IsZero(), "a resume cannot claim historical messages from a new local timestamp")
}

func TestZCodeTurnRecovery_EmptyNativeScopeConflictsWithRetainedContent(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	scope := zcodeTextScope("message")
	owner := &zcodeTurnState{contentByScope: map[string]*zcodeScopeContent{scope: {observed: []byte("already delivered")}}}
	a.dispatchMu.Lock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "message", Role: "assistant"}, Parts: []zcodeNativePart{{Type: zcodeNativePartTypeText, Text: ""}}}})
	a.dispatchMu.Unlock()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconciliation conflict")
	assert.Equal(t, "already delivered", owner.contentByScope[scope].text())
}

func TestZCodeFinalMessages_PersistTheMissingSuffixThroughTheRealBuffer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"hello "}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	messages := []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "message", Role: "assistant"}, Parts: []zcodeNativePart{{ID: "text", Type: zcodeNativePartTypeText, Text: "hello world"}}}}
	a.dispatchMu.Lock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
	a.dispatchMu.Unlock()
	require.NoError(t, err)
	a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	texts, _ := zcodePersistedTextsAndClosings(t, sink)
	assert.Equal(t, []string{"hello world"}, texts, "recovered bytes must reach normal persistence without a test callback")
}

func TestZCodeFinalMessages_ValidateEveryScopeBeforeAppendingAnySuffix(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"prefix"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"second","delta":"kept"}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	messages := []zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "first", Role: "assistant"}, Parts: []zcodeNativePart{{ID: "a", Type: zcodeNativePartTypeText, Text: "prefix missing"}}},
		{Info: zcodeNativeMessageInfo{ID: "second", Role: "assistant"}, Parts: []zcodeNativePart{{ID: "b", Type: zcodeNativePartTypeText, Text: "rewritten"}}},
	}
	a.dispatchMu.Lock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
	a.dispatchMu.Unlock()
	require.ErrorContains(t, err, "reconciliation conflict")
	a.Mu.Lock()
	assert.Equal(t, "prefix", owner.contentByScope[zcodeTextScope("first")].text())
	a.Mu.Unlock()
	_, buffered, _ := a.generationBuffer.ScopeSnapshot(zcodeTextScope("first"))
	assert.Equal(t, "prefix", buffered, "a later conflict must leave earlier scopes unchanged")
}

func TestZCodeRecoveryEvidence_MissingSnapshotFieldsCannotConfirmIdle(t *testing.T) {
	t.Parallel()
	for name, snapshot := range map[string]string{
		"missing runtime":    `{"projection":{"currentTurnId":"turn","turnCount":1,"status":"idle"}}`,
		"missing projection": `{"runtime":{"stateRevision":2}}`,
		"empty snapshot":     `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"messageId":"user"}`))
			a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":2,"patch":{}}`))
			a.Mu.Lock()
			owner := a.turns["turn"]
			state := owner.recovery
			a.Mu.Unlock()
			var snap zcodeSessionReadSnapshot
			require.NoError(t, json.Unmarshal([]byte(snapshot), &snap))
			for observation := 0; observation < 2; observation++ {
				settled, err := a.applyZCodeRecoverySnapshot(owner, state, snap)
				require.NoError(t, err)
				assert.False(t, settled, "absent state is unknown, rather than evidence of inactivity")
			}
			assert.True(t, a.PublishTurnActive().Active)
		})
	}
}

func TestZCodeRecoveryEvidence_AStaleBoundaryCannotCompleteItsSuccessor(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "successor", contracts.ZCodeEventTurnStarted, `{"turnNumber":2,"messageId":"user-b"}`))
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":100,"patch":{}}`))
	a.Mu.Lock()
	owner := a.turns["successor"]
	state := owner.recovery
	a.Mu.Unlock()
	var snap zcodeSessionReadSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"runtime":{"stateRevision":100},"projection":{"currentTurnId":"previous","turnCount":1,"status":"idle"}}`), &snap))
	for observation := 0; observation < 2; observation++ {
		settled, err := a.applyZCodeRecoverySnapshot(owner, state, snap)
		require.NoError(t, err)
		assert.False(t, settled, "the read must identify the successor, regardless of the boundary revision")
	}
	assert.True(t, a.PublishTurnActive().Active)
}

func TestZCodeRecoveryEvidence_UnknownExplicitOwnerDoesNotFallBackToAProvisionalTurn(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.createProvisionalZCodeTurn("sess-1", "input", 1)
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":2,"patch":{"projection":{"currentTurnId":"other","turnCount":1}}}`))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	owner := a.turns[a.foregroundTurnID]
	require.NotNil(t, owner)
	require.NotNil(t, owner.recovery)
	assert.Nil(t, owner.recovery.boundary, "an explicit unknown turn cannot become ID-less evidence")
}

func TestZCodeRecoveryWatcher_ReconcilesNativeMessagesBeforeSettlement(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"messageId":"user"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"hello "}`))
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":2,"patch":{}}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	state := owner.recovery
	a.Mu.Unlock()
	fetched := 0
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionRead:
			return json.RawMessage(`{"runtime":{"eventSeq":2,"stateRevision":2,"activeTurnId":""},"projection":{"currentTurnId":"turn","turnCount":1,"status":"idle"}}`)
		case MethodSessionMessages:
			fetched++
			return json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user"}},{"info":{"id":"message","role":"assistant","parentID":"user","anchor":{"turnId":"turn"}},"parts":[{"id":"text","type":"text","text":"hello world"}]}]}`)
		}
		return nil
	})
	first, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
	require.NoError(t, err)
	assert.False(t, first)
	settled, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
	require.NoError(t, err)
	assert.True(t, settled)
	assert.Equal(t, 1, fetched, "the production watcher must request the native final messages")
	texts, _ := zcodePersistedTextsAndClosings(t, sink)
	assert.Equal(t, []string{"hello world"}, texts)
}

func TestZCodeRecoveryWatcher_AFailedMessageReadKeepsTheOwnerActive(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"messageId":"user"}`))
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":2,"patch":{}}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	state := owner.recovery
	a.Mu.Unlock()
	a.SetStdinForTest(agenttest.NopStdin(zcodeInputWriter(func(raw []byte) (int, error) {
		var request zcodeSentRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		switch request.Method {
		case MethodSessionRead:
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"runtime":{"eventSeq":1,"stateRevision":2,"activeTurnId":""},"projection":{"currentTurnId":"turn","turnCount":1,"status":"idle"}}`)))
		case MethodSessionMessages:
			a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, request), -32603, "native store unavailable"))
		}
		return len(raw), nil
	})))
	_, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
	require.NoError(t, err)
	settled, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
	require.ErrorContains(t, err, "native store unavailable")
	assert.False(t, settled)
	assert.True(t, a.PublishTurnActive().Active)
	a.Mu.Lock()
	assert.Same(t, state, owner.recovery, "failed reconciliation must preserve retry ownership")
	a.Mu.Unlock()
}

func TestZCodeRecoveryLost_AlwaysReportsAnUnprovenTurnAsAnError(t *testing.T) {
	t.Parallel()
	for _, pendingSuccess := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending success %t", pendingSuccess), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
			if pendingSuccess {
				a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":2,"patch":{}}`))
			}
			a.Mu.Lock()
			owner := a.turns["turn"]
			state := owner.recovery
			a.Mu.Unlock()
			var snap zcodeSessionReadSnapshot
			require.NoError(t, json.Unmarshal([]byte(`{"runtime":{"eventSeq":1,"stateRevision":2,"activeTurnId":"turn"},"projection":{"currentTurnId":"turn","turnCount":1,"status":"running"}}`), &snap))
			settled, err := a.applyZCodeRecoverySnapshot(owner, state, snap)
			require.NoError(t, err)
			assert.False(t, settled)
			a.Mu.Lock()
			state.frozenSince = a.Clock().Now().Add(-zcodeTurnRecoveryFreezeLimit - time.Minute)
			a.Mu.Unlock()
			settled, err = a.applyZCodeRecoverySnapshot(owner, state, snap)
			require.NoError(t, err)
			assert.True(t, settled)
			notices := sink.LeapMuxNotifications()
			require.Len(t, notices, 1, "a lost turn needs a visible failure even without native end evidence")
			assert.Equal(t, contracts.NotificationTypeAgentError, notices[0][contracts.NotificationFieldType])
			assert.NotEmpty(t, notices[0][contracts.NotificationFieldError])
			for _, row := range sink.Messages() {
				assert.False(t, row.TurnEnd, "unconfirmed old prompt evidence cannot report successful completion")
			}
		})
	}
}

func TestZCodeRecoveryEvidence_PreservesNativeEnvelopeAndRecordedOutcome(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed %t", failed), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"messageId":"user"}`))
			reason, status, completion := "prompt_completed", "idle", agent.MessageCompletionComplete
			if failed {
				reason, status, completion = "prompt_failed", "error", agent.MessageCompletionError
			}
			params := json.RawMessage(fmt.Sprintf(`{"scope":"session","sessionId":"sess-1","reason":%q,"revision":2,"future":"preserved","patch":{"unknown":{"value":3}}}`, reason))
			a.handleZCodeStateUpdated(params)
			a.Mu.Lock()
			owner := a.turns["turn"]
			state := owner.recovery
			a.Mu.Unlock()
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method == MethodSessionRead {
					return json.RawMessage(fmt.Sprintf(`{"runtime":{"eventSeq":1,"stateRevision":2,"activeTurnId":""},"projection":{"currentTurnId":"turn","turnCount":1,"status":%q}}`, status))
				}
				if request.Method == MethodSessionMessages {
					return json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user"}}]}`)
				}
				return nil
			})
			_, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
			require.NoError(t, err)
			settled, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
			require.NoError(t, err)
			require.True(t, settled)
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.True(t, rows[0].TurnEnd)
			assert.Equal(t, completion, rows[0].Completion)
			var envelope struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(rows[0].Content, &envelope))
			assert.Equal(t, contracts.ZCodeMethodStateUpdated, envelope.Type)
			assert.JSONEq(t, string(params), string(envelope.Payload), "all native attribution and unknown fields must survive")
		})
	}
}

func TestZCodeRecoveredMessages_AbsentNativeScopesRejectRetainedContent(t *testing.T) {
	t.Parallel()
	for _, messages := range [][]zcodeNativeMessage{nil, {{Info: zcodeNativeMessageInfo{ID: "message", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "reasoning", Text: "thought"}}}}} {
		a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
		owner := &zcodeTurnState{id: "turn", contentByScope: map[string]*zcodeScopeContent{zcodeTextScope("message"): {observed: []byte("retained")}}}
		err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
		assert.ErrorContains(t, err, "reconciliation conflict")
		assert.Empty(t, a.generationBuffer.ScopeIDs(), "validation must finish before any suffix is appended")
	}
}

func TestZCodeStreamRecovery_DiscardRemovesRetainedRetryContent(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"discarded"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventStreamRecoveryUpdated, `{"assistantMessageId":"message","discardedTextBytes":9}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	require.NoError(t, a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "message", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "replacement"}}}}))
	_, text, _ := a.generationBuffer.ScopeSnapshot(zcodeTextScope("message"))
	assert.Equal(t, "replacement", text)
}

func TestZCodeRecoveryCoverage_CompletedResponsesAreNotRecoveredAgain(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream %t", stream), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
			seq := int64(2)
			if stream {
				a.HandleOutput(zcodeTurnEventLine(t, seq, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"hello "}`))
				seq++
			}
			a.HandleOutput(zcodeTurnEventLine(t, seq, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"hello world","querySource":"main_turn","stopReason":"stop"}`))
			a.Mu.Lock()
			owner := a.turns["turn"]
			a.Mu.Unlock()
			messages := []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "message", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "hello world"}}}}
			require.NoError(t, a.applyZCodeRecoveredMessagesLocked(owner, messages))
			require.NoError(t, a.applyZCodeRecoveredMessagesLocked(owner, messages), "repeating the same canonical evidence must preserve accepted coverage")
			texts, _ := zcodePersistedTextsAndClosings(t, sink)
			assert.Empty(t, texts)
			assert.Empty(t, a.generationBuffer.ScopeIDs(), "native response coverage already represents the entire answer")
		})
	}
}

func TestZCodeRecoveryCoverage_EmptyArchiveResponseDoesNotCoverCanonicalText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"messageId":"user"}`))
	a.dispatchMu.Lock()
	a.markZCodeCanonicalRecoveryLocked()
	a.dispatchZCodeEventLocked(zcodeEventEnvelope{
		Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", archive: true,
		Payload: json.RawMessage(`{"content":"","querySource":"main_turn","stopReason":"end_turn"}`),
	})
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{{
		Info:  zcodeNativeMessageInfo{ID: "message", Role: "assistant"},
		Parts: []zcodeNativePart{{Type: "text", Text: "The final answer."}},
	}})
	if err == nil {
		a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	}
	a.unlockZCodeDispatch()
	require.NoError(t, err)
	texts, _ := zcodePersistedTextsAndClosings(t, sink)
	assert.Equal(t, []string{"The final answer."}, texts)
}

func TestZCodeRecoveryCoverage_EmptyCompletionPreservesStreamedText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, text string }{
		{"plain", "The final answer."},
		{"unicode", "Hello 世界 🙂"},
		{"whitespace", " "},
		{"large", strings.Repeat("The final answer. ", 4096)},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
			payload, err := json.Marshal(zcodeModelStreaming{Kind: StreamTextDelta, AssistantMessageID: "message", Delta: tc.text})
			require.NoError(t, err)
			a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, string(payload)))
			a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"","querySource":"main_turn","stopReason":"end_turn"}`))
			a.HandleOutput(zcodeTurnEventLine(t, 4, "turn", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
			texts, _ := zcodePersistedTextsAndClosings(t, sink)
			if tc.text == "" {
				assert.Empty(t, texts)
			} else {
				assert.Equal(t, []string{tc.text}, texts)
			}
		})
	}
}

func TestZCodeRecoveryCoverage_EmptyToolResponseDoesNotMisalignTheFinalAnswer(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream %t", stream), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
			a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"tool_input_start","assistantMessageId":"tool-message","toolCallId":"call"}`))
			a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"","querySource":"main_turn","stopReason":"tool-calls"}`))
			if stream {
				a.HandleOutput(zcodeTurnEventLine(t, 4, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"answer","delta":"The final "}`))
			}
			a.HandleOutput(zcodeTurnEventLine(t, 0, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"The final answer.","querySource":"main_turn","stopReason":"stop"}`))
			a.Mu.Lock()
			owner := a.turns["turn"]
			a.Mu.Unlock()
			messages := []zcodeNativeMessage{
				{Info: zcodeNativeMessageInfo{ID: "tool-message", Role: "assistant"}},
				{Info: zcodeNativeMessageInfo{ID: "answer", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "The final answer."}}},
			}
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
			a.unlockZCodeDispatch()
			require.NoError(t, err)
			assert.Empty(t, a.generationBuffer.ScopeIDs(), "the accepted final answer must not be emitted twice")
			require.Len(t, sink.Messages(), 1)
		})
	}
}

func TestZCodeRecoveryCoverage_EmptyCompletionPreservesOrderAndOtherOwners(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "foreground", contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "foreground", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"foreground-message","delta":"Foreground answer."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "foreground", contracts.ZCodeEventModelStreaming, `{"kind":"reasoning_delta","assistantMessageId":"foreground-message","delta":"Foreground reasoning."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "background", contracts.ZCodeEventTurnStarted, `{"inputSource":"background_task"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 5, "background", contracts.ZCodeEventModelStreaming, `{"kind":"reasoning_delta","assistantMessageId":"background-message","delta":"Background reasoning."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 6, "background", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"background-message","delta":"Background answer."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 7, "background", contracts.ZCodeEventSessionUpdated, `{"content":"","querySource":"main_turn","stopReason":"end_turn"}`))
	texts, _ := zcodePersistedTextsAndClosings(t, sink)
	require.Equal(t, []string{"Background reasoning.", "Background answer."}, texts, "an empty completion must persist its stream before a later response")
	_, foregroundText, found := a.generationBuffer.ScopeSnapshot(zcodeTextScope("foreground-message"))
	assert.True(t, found)
	assert.Equal(t, "Foreground answer.", foregroundText)
	_, foregroundReasoning, found := a.generationBuffer.ScopeSnapshot(zcodeReasoningScope("foreground-message"))
	assert.True(t, found)
	assert.Equal(t, "Foreground reasoning.", foregroundReasoning)
	a.HandleOutput(zcodeTurnEventLine(t, 8, "background", contracts.ZCodeEventSessionUpdated, `{"content":"Later background response.","querySource":"main_turn","stopReason":"stop"}`))
	require.Len(t, sink.Messages(), 3)
	var response struct {
		Payload struct {
			Content string `json:"content"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(sink.Messages()[2].Content, &response))
	assert.Equal(t, "Later background response.", response.Payload.Content)
}

func TestZCodeRecoveryCoverage_EmptyCompletionPreservesStreamOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	const messageCount = 32
	var expected []string
	for index := range messageCount {
		text := fmt.Sprintf("Message %02d.", index)
		expected = append(expected, text)
		payload, err := json.Marshal(zcodeModelStreaming{
			Kind: StreamTextDelta, AssistantMessageID: fmt.Sprintf("message-%02d", index), Delta: text,
		})
		require.NoError(t, err)
		a.HandleOutput(zcodeTurnEventLine(t, int64(index+2), "turn", contracts.ZCodeEventModelStreaming, string(payload)))
	}
	a.HandleOutput(zcodeTurnEventLine(t, messageCount+2, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"","querySource":"main_turn","stopReason":"end_turn"}`))
	texts, _ := zcodePersistedTextsAndClosings(t, sink)
	assert.Equal(t, expected, texts)
}

func TestZCodeRecoveryCoverage_EmptyCompletionPreservesMixedScopeOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"Earlier answer."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"reasoning_delta","assistantMessageId":"second","delta":"Later reasoning."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"second","delta":"Later answer."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 5, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"","querySource":"main_turn","stopReason":"end_turn"}`))
	texts, _ := zcodePersistedTextsAndClosings(t, sink)
	assert.Equal(t, []string{"Earlier answer.", "Later reasoning.", "Later answer."}, texts)
}

func TestZCodeRecoveryCoverage_CanonicalPartsPreserveNativeOrder(t *testing.T) {
	t.Parallel()
	for _, parts := range [][]zcodeNativePart{
		{{ID: "r", Type: "reasoning", Text: "Think"}, {ID: "t", Type: "text", Text: "Answer"}},
		{{ID: "t", Type: "text", Text: "Answer"}, {ID: "r", Type: "reasoning", Text: "Think"}},
		{{ID: "r", Type: "reasoning", Text: "Think"}},
		{{ID: "t", Type: "text", Text: "Answer"}},
		{{ID: "r", Type: "reasoning", Text: "Think"}, {ID: "r", Type: "text", Text: "Duplicate"}, {ID: "t", Type: "text", Text: "Answer"}},
	} {
		t.Run(fmt.Sprint(parts), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			owner := &zcodeTurnState{}
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "message"}, Parts: parts}})
			if err == nil {
				a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
			}
			a.unlockZCodeDispatch()
			require.NoError(t, err)
			var want []string
			seen := make(map[string]bool)
			for _, part := range parts {
				if !seen[part.ID] {
					want = append(want, part.Text)
					seen[part.ID] = true
				}
			}
			texts, _ := zcodePersistedTextsAndClosings(t, sink)
			assert.Equal(t, want, texts)
		})
	}
}

func TestZCodeRecoveryCoverage_UncertainResponsesWaitForCanonicalOrder(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.dispatchMu.Lock()
	a.markZCodeCanonicalRecoveryLocked()
	a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"Accepted answer.","querySource":"main_turn","stopReason":"stop"}`)})
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	assert.Empty(t, sink.Messages(), "an uncertain later response cannot overtake missing native output")
	err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "earlier"}, Parts: []zcodeNativePart{{Type: "text", Text: "Recovered earlier response."}}},
		{Info: zcodeNativeMessageInfo{ID: "answer"}, Parts: []zcodeNativePart{{Type: "reasoning", Text: "Final reasoning."}, {Type: "text", Text: "Accepted answer."}}},
	})
	if err == nil {
		a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	}
	a.unlockZCodeDispatch()
	require.NoError(t, err)
	assert.Equal(t, []string{"Recovered earlier response.", "Final reasoning.", "Accepted answer."}, zcodeRecoveryTranscriptTexts(t, sink))
}

func TestZCodeRecoveryCoverage_OlderReasoningPrecedesTheFinalAnswer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.dispatchMu.Lock()
	a.markZCodeCanonicalRecoveryLocked()
	a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"Final answer.","querySource":"main_turn","stopReason":"stop"}`)})
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "first"}, Parts: []zcodeNativePart{{Type: "reasoning", Text: "Old reasoning 1."}}},
		{Info: zcodeNativeMessageInfo{ID: "second"}, Parts: []zcodeNativePart{{Type: "reasoning", Text: "Old reasoning 2."}}},
		{Info: zcodeNativeMessageInfo{ID: "answer"}, Parts: []zcodeNativePart{{Type: "text", Text: "Final answer."}}},
	})
	if err == nil {
		a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	}
	a.unlockZCodeDispatch()
	require.NoError(t, err)
	assert.Equal(t, []string{"Old reasoning 1.", "Old reasoning 2.", "Final answer."}, zcodeRecoveryTranscriptTexts(t, sink))
}

func TestZCodeRecoveryCoverage_DefersAResponseWithAStaleMessageIdentity(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		if request.Method == MethodSessionEvents {
			return json.RawMessage(`{"sessionId":"sess-1","eventSeq":5,"events":[]}`)
		}
		return nil
	})
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"Earlier response."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 5, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"Final response.","querySource":"main_turn","stopReason":"stop"}`))
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	assert.Empty(t, sink.Messages())
	a.Mu.Lock()
	owner := a.turns["turn"]
	assert.Empty(t, owner.acceptedResponses)
	a.Mu.Unlock()
	a.dispatchMu.Lock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "first"}, Parts: []zcodeNativePart{{Type: "text", Text: "Earlier response."}}},
		{Info: zcodeNativeMessageInfo{ID: "last"}, Parts: []zcodeNativePart{{Type: "text", Text: "Final response."}}},
	})
	if err == nil {
		a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	}
	a.unlockZCodeDispatch()
	require.NoError(t, err)
	assert.Equal(t, []string{"Earlier response.", "Final response."}, zcodeRecoveryTranscriptTexts(t, sink))
}

func TestZCodeRecoveryCoverage_RejectsMissingTextBeforeAnAcceptedResponse(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "First "} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
			a.Mu.Lock()
			owner := a.turns["turn"]
			a.Mu.Unlock()
			if prefix != "" {
				a.HandleOutput(zcodeTurnEventLine(t, 0, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"First "}`))
				a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
			}
			a.HandleOutput(zcodeTurnEventLine(t, 0, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"second","delta":"Second"}`))
			a.HandleOutput(zcodeTurnEventLine(t, 0, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"Second","querySource":"main_turn","stopReason":"stop"}`))
			before := zcodeRecoveryTranscriptTexts(t, sink)
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{
				{Info: zcodeNativeMessageInfo{ID: "first"}, Parts: []zcodeNativePart{{Type: "text", Text: "First answer"}}},
				{Info: zcodeNativeMessageInfo{ID: "second"}, Parts: []zcodeNativePart{{Type: "text", Text: "Second"}}},
			})
			a.unlockZCodeDispatch()
			require.ErrorContains(t, err, "order")
			assert.Empty(t, a.generationBuffer.ScopeIDs())
			assert.Equal(t, before, zcodeRecoveryTranscriptTexts(t, sink))
		})
	}
}

func TestZCodeRecoveryCoverage_UncertainResponsesSurviveTeardown(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"exit", "stop", "session replacement", "expiry"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method == MethodSessionStop {
					return json.RawMessage(`{}`)
				}
				return nil
			})
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
			a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"Done"}`))
			a.dispatchMu.Lock()
			a.markZCodeCanonicalRecoveryLocked()
			a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"","querySource":"main_turn","stopReason":"stop"}`)})
			a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"Done","querySource":"main_turn","stopReason":"stop","future":"preserved"}`)})
			a.unlockZCodeDispatch()
			assert.Empty(t, sink.Messages(), "uncertain content must wait for reconciliation or teardown")
			switch action {
			case "exit":
				a.SimulateExitForTest()
				require.NoError(t, a.Wait())
			case "stop":
				a.SimulateExitForTest()
				a.Stop()
			case "session replacement":
				var replacement zcodeStateSnapshot
				replacement.Session.SessionID = "replacement"
				a.adoptCompleteZCodeSessionSnapshot(replacement, true)
			case "expiry":
				a.Mu.Lock()
				owner := a.turns["turn"]
				pending := &zcodePendingNativeSettlement{settlement: zcodeTurnSettlement{persistEnd: func(int, bool) {}}}
				owner.pendingNative = pending
				state := owner.recovery
				a.Mu.Unlock()
				settled, err := a.expireZCodeNativeSettlement(t.Context(), owner, state, pending)
				require.NoError(t, err)
				assert.True(t, settled)
			}
			assert.Equal(t, []string{"Done", "Done"}, zcodeRecoveryTranscriptTexts(t, sink))
			rows := sink.Messages()
			require.Len(t, rows, 2)
			assert.Contains(t, string(rows[1].Content), `"future":"preserved"`)
			assert.Equal(t, "sess-1", rows[1].AgentSessionID)
		})
	}
}

func TestZCodeRecoveryCoverage_AnonymousResponseCannotConsumeSubmittedText(t *testing.T) {
	t.Parallel()
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprint(later), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
			a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"Same answer."}`))
			a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"","querySource":"main_turn","stopReason":"stop"}`))
			a.HandleOutput(zcodeTurnEventLine(t, 4, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"Same answer.","querySource":"main_turn","stopReason":"stop"}`))
			messages := []zcodeNativeMessage{
				{Info: zcodeNativeMessageInfo{ID: "first"}, Parts: []zcodeNativePart{{Type: "text", Text: "Same answer."}}},
				{Info: zcodeNativeMessageInfo{ID: "second"}, Parts: []zcodeNativePart{{Type: "text", Text: "Same answer."}}},
			}
			want := []string{"Same answer.", "Same answer."}
			if later {
				a.HandleOutput(zcodeTurnEventLine(t, 5, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"third","delta":"Last answer."}`))
				a.HandleOutput(zcodeTurnEventLine(t, 6, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"Last answer.","querySource":"main_turn","stopReason":"stop"}`))
				messages = append(messages, zcodeNativeMessage{Info: zcodeNativeMessageInfo{ID: "third"}, Parts: []zcodeNativePart{{Type: "text", Text: "Last answer."}}})
				want = append(want, "Last answer.")
			}
			a.Mu.Lock()
			owner := a.turns["turn"]
			a.Mu.Unlock()
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
			if err == nil {
				err = a.applyZCodeRecoveredMessagesLocked(owner, messages)
			}
			if err == nil {
				a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
				err = a.applyZCodeRecoveredMessagesLocked(owner, messages)
			}
			a.unlockZCodeDispatch()
			require.NoError(t, err)
			assert.Equal(t, want, zcodeRecoveryTranscriptTexts(t, sink))
		})
	}
}

func TestZCodeRecoveryCoverage_RejectsAmbiguousAnonymousResponses(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	owner := &zcodeTurnState{acceptedResponses: []zcodeModelResponseText{{text: "Same answer."}}}
	a.dispatchMu.Lock()
	err := a.applyZCodeRecoveredMessagesLocked(owner, []zcodeNativeMessage{
		{Info: zcodeNativeMessageInfo{ID: "first"}, Parts: []zcodeNativePart{{Type: "text", Text: "Same answer."}}},
		{Info: zcodeNativeMessageInfo{ID: "second"}, Parts: []zcodeNativePart{{Type: "text", Text: "Same answer."}}},
	})
	a.unlockZCodeDispatch()
	require.ErrorContains(t, err, "ambiguous")
	assert.Empty(t, owner.contentByScope)
	assert.Empty(t, a.generationBuffer.ScopeIDs())
}

func TestZCodeRecoveryCoverage_RejectsAbsentOrDuplicateNativeMessageIDs(t *testing.T) {
	t.Parallel()
	for _, ids := range [][]string{{""}, {"same", "same"}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
			responses := []zcodeModelResponseText{{text: "Answer"}}
			owner := &zcodeTurnState{acceptedResponses: responses}
			var messages []zcodeNativeMessage
			for _, id := range ids {
				messages = append(messages, zcodeNativeMessage{Info: zcodeNativeMessageInfo{ID: id}, Parts: []zcodeNativePart{{Type: "text", Text: "Answer"}}})
			}
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
			a.unlockZCodeDispatch()
			require.ErrorContains(t, err, "reconciliation conflict")
			assert.Empty(t, owner.contentByScope)
			assert.Equal(t, responses, owner.acceptedResponses)
			assert.Empty(t, a.generationBuffer.ScopeIDs())
		})
	}
}

func TestZCodeRecoveryCoverage_FallbackScopeIdentitySurvivesPartialFlush(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.dispatchMu.Lock()
	a.markZCodeCanonicalRecoveryLocked()
	for _, text := range []string{"First", "Second"} {
		a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(fmt.Sprintf(`{"content":%q,"querySource":"main_turn","stopReason":"stop"}`, text))})
	}
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	require.True(t, a.prepareZCodeDeferredResponses(owner))
	scopes := a.zcodeTurnBufferedScopes(owner, nil)
	require.Len(t, scopes, 2)
	require.True(t, a.persistZCodeGenerationScopes(owner, scopes[:1], agent.MessageCompletionComplete))
	a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"Third","querySource":"main_turn","stopReason":"stop"}`)})
	require.True(t, a.prepareZCodeDeferredResponses(owner))
	a.Mu.Lock()
	second := owner.contentByScope[scopes[1]].text()
	a.Mu.Unlock()
	assert.Equal(t, "Second", second, "a new response cannot replace an earlier pending scope")
	a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	a.unlockZCodeDispatch()
	assert.Equal(t, []string{"First", "Second", "Third"}, zcodeRecoveryTranscriptTexts(t, sink))
}

func TestZCodeRecoveryCoverage_GlobalFlushRetriesPendingOutputWithoutOpenScopes(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.generationBuffer.Append("scope", agent.AssembledMessageKindText, "Retained", providerkit.JoinVerbatim)
	sink.PersistErr = errors.New("storage unavailable")
	a.flushZCodeGeneration(agent.MessageCompletionInterrupted)
	assert.Empty(t, a.generationBuffer.ScopeIDs())
	assert.True(t, a.generationBuffer.HasPendingWrites())
	sink.PersistErr = nil
	a.flushZCodeGeneration(agent.MessageCompletionComplete)
	var stored []agenttest.Message
	for _, row := range sink.Messages() {
		if row.Stored() {
			stored = append(stored, row)
		}
	}
	require.Len(t, stored, 1)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"Retained","completion":"interrupted"}`, string(stored[0].Content))
	assert.False(t, a.generationBuffer.HasPendingWrites())
}

func TestZCodeRecoveryCoverage_UnprovenStreamIdentityCannotCoverAnotherResponse(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"first","delta":"Same"}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
	a.dispatchMu.Lock()
	a.markZCodeCanonicalRecoveryLocked()
	a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"Same","querySource":"main_turn","stopReason":"stop"}`)})
	a.unlockZCodeDispatch()
	a.Mu.Lock()
	pending := &zcodePendingNativeSettlement{settlement: zcodeTurnSettlement{persistEnd: func(int, bool) {}}}
	owner.pendingNative = pending
	state := owner.recovery
	a.Mu.Unlock()
	settled, err := a.expireZCodeNativeSettlement(t.Context(), owner, state, pending)
	require.NoError(t, err)
	assert.True(t, settled)
	assert.Equal(t, []string{"Same", "Same"}, zcodeRecoveryTranscriptTexts(t, sink))
}

func TestZCodeRecoveryCoverage_DeferredIdentitySurvivesAnotherCanonicalMessage(t *testing.T) {
	t.Parallel()
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
			a.dispatchMu.Lock()
			defer a.unlockZCodeDispatch()
			a.markZCodeCanonicalRecoveryLocked()
			a.dispatchZCodeEventLocked(zcodeEventEnvelope{Type: contracts.ZCodeEventSessionUpdated, TurnID: "turn", Payload: json.RawMessage(`{"content":"Same","querySource":"main_turn","stopReason":"stop"}`)})
			a.Mu.Lock()
			owner := a.turns["turn"]
			a.Mu.Unlock()
			messages := []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "first"}, Parts: []zcodeNativePart{{Type: "text", Text: "Same"}}}}
			if prepared {
				require.True(t, a.prepareZCodeDeferredResponses(owner))
			}
			require.NoError(t, a.applyZCodeRecoveredMessagesLocked(owner, messages))
			messages = append(messages, zcodeNativeMessage{Info: zcodeNativeMessageInfo{ID: "second"}, Parts: []zcodeNativePart{{Type: "text", Text: "Same"}}})
			require.NoError(t, a.applyZCodeRecoveredMessagesLocked(owner, messages), "a proven response identity must not become ambiguous on retry")
			a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionComplete)
			assert.Equal(t, []string{"Same", "Same"}, zcodeRecoveryTranscriptTexts(t, sink))
		})
	}
}

func TestZCodeNativeEnd_LocalStopBeforeNativeEndPreservesCanonicalOrder(t *testing.T) {
	t.Parallel()
	a, sink, stdin := zcodeCanonicalReplayFixtureWithClock(t, quartz.NewReal(), false)
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, owner.id, owner.generation)
	generation := a.stopWindow.generation
	a.Mu.Unlock()
	a.endStoppedZCodeTurn(generation)
	require.NotContains(t, zcodeRecoveryTranscriptTexts(t, sink.ControlSink), "Answer", "local stop must keep uncertain output for reconciliation")
	a.HandleOutput(zcodeTurnEventLine(t, 5, "turn-A", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	request := waitZCodeRequest(t, stdin, MethodSessionMessages)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), zcodeCanonicalMessages()))
	waitZCodeCanonicalEnd(t, sink)
	assert.Equal(t, []string{"H", "ello", "Answer"}, zcodeRecoveryTranscriptTexts(t, sink.ControlSink))
	assert.Len(t, zcodeNativeTurnEndRows(t, sink.Messages()), 1)
	sink.workerMu.Lock()
	assert.Empty(t, sink.workerMessages)
	sink.workerMu.Unlock()
}

func TestZCodeRecoveryCoverage_LocalStopRetainsRecoveryForALateBoundary(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, sink, _ := zcodeCanonicalReplayFixtureWithClock(t, clock, false)
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	state := owner.recovery
	a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, owner.id, owner.generation)
	generation := a.stopWindow.generation
	a.Mu.Unlock()
	a.endStoppedZCodeTurn(generation)
	a.Mu.Lock()
	watcher := owner.recovery
	a.Mu.Unlock()
	require.Same(t, state, watcher, "a locally stopped turn still needs its canonical output")
	a.HandleOutput([]byte(`{"method":"state.updated","params":{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":10,"patch":{"projection":{"currentTurnId":"turn-A","status":"idle"}}}}`))
	zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionRead:
			return json.RawMessage(`{"runtime":{"eventSeq":4,"stateRevision":10,"activeTurnId":""},"projection":{"currentTurnId":"turn-A","status":"idle"}}`)
		case MethodSessionMessages:
			return zcodeCanonicalMessages()
		}
		return nil
	})
	settled, err := a.checkZCodeTurnRecovery(t.Context(), owner, state)
	require.NoError(t, err)
	assert.False(t, settled)
	settled, err = a.checkZCodeTurnRecovery(t.Context(), owner, state)
	require.NoError(t, err)
	assert.True(t, settled)
	assert.Equal(t, []string{"H", "ello", "Answer"}, zcodeRecoveryTranscriptTexts(t, sink.ControlSink))
}

func zcodeRecoveryTranscriptTexts(t *testing.T, sink *agenttest.ControlSink) []string {
	t.Helper()
	var texts []string
	for _, row := range sink.Messages() {
		_, text, _, assembled := agenttest.DecodeAssembledMessage(row.Content)
		if !assembled {
			var envelope struct {
				Payload struct {
					Content string `json:"content"`
				} `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(row.Content, &envelope))
			text = envelope.Payload.Content
			if len(row.SupplementalContent) > 0 {
				var supplement map[string]map[string]string
				require.NoError(t, json.Unmarshal(row.SupplementalContent, &supplement))
				text = strings.TrimPrefix(text, supplement[contracts.ZCodeSupplementResponseText][contracts.ZCodeResponseTextAcceptedPrefix])
			}
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func TestZCodeRecoveryCoverage_SkipsUnmatchedMessageBodies(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	owner := &zcodeTurnState{acceptedResponses: []zcodeModelResponseText{{messageID: "missing", text: "Answer"}}}
	messages := []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "other"}, Parts: []zcodeNativePart{{Type: "reasoning", Text: strings.Repeat("x", 10*1024*1024)}}}}
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := a.applyZCodeRecoveredMessagesLocked(owner, messages); err == nil {
				b.Fatal("a missing accepted response must fail reconciliation")
			}
		}
	})
	assert.Less(t, result.AllocedBytesPerOp(), int64(1024*1024))
	assert.Empty(t, owner.contentByScope)
	assert.Empty(t, a.generationBuffer.ScopeIDs())
}

func TestZCodeRecoveryCoverage_OwnerSelectionDoesNotAllocateForOtherOwners(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	owner := &zcodeTurnState{contentByScope: map[string]*zcodeScopeContent{"owned": {}}}
	a.generationBuffer.Append("owned", agent.AssembledMessageKindText, "Owned", providerkit.JoinVerbatim)
	for index := range 2048 {
		a.generationBuffer.Append(fmt.Sprint(index), agent.AssembledMessageKindText, "Foreign", providerkit.JoinVerbatim)
	}
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if scopes := a.zcodeTurnBufferedScopes(owner, nil); len(scopes) != 1 || scopes[0] != "owned" {
				b.Fatal("owner selection changed")
			}
		}
	})
	assert.Less(t, result.AllocedBytesPerOp(), int64(4096))
}

func TestZCodeRecoveryCoverage_MatchesAcceptedResponsesWithFollowingMissingMessages(t *testing.T) {
	t.Parallel()
	for _, messageID := range []string{"answer", ""} {
		t.Run(fmt.Sprintf("message ID %q", messageID), func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
			owner := &zcodeTurnState{acceptedResponses: []zcodeModelResponseText{{messageID: messageID, text: "Accepted answer."}}}
			messages := []zcodeNativeMessage{
				{Info: zcodeNativeMessageInfo{ID: "answer", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "Accepted answer."}}},
				{Info: zcodeNativeMessageInfo{ID: "missing", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "Recovered later response."}}},
			}
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
			a.unlockZCodeDispatch()
			require.NoError(t, err)
			_, text, found := a.generationBuffer.ScopeSnapshot(zcodeTextScope("missing"))
			assert.True(t, found)
			assert.Equal(t, "Recovered later response.", text)
			_, _, found = a.generationBuffer.ScopeSnapshot(zcodeTextScope("answer"))
			assert.False(t, found)
		})
	}
}

func TestZCodeRecoveryCoverage_RejectsContradictoryAcceptedResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		responses []zcodeModelResponseText
	}{
		{"missing", []zcodeModelResponseText{{messageID: "missing", text: "Accepted answer."}}},
		{"rewritten", []zcodeModelResponseText{{messageID: "answer", text: "Different accepted answer."}}},
		{"unidentified missing", []zcodeModelResponseText{{text: "Different accepted answer."}}},
		{"reordered", []zcodeModelResponseText{{messageID: "answer", text: "Accepted answer."}, {messageID: "earlier", text: "Earlier answer."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
			owner := &zcodeTurnState{acceptedResponses: tc.responses}
			messages := []zcodeNativeMessage{
				{Info: zcodeNativeMessageInfo{ID: "earlier", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "Earlier answer."}}},
				{Info: zcodeNativeMessageInfo{ID: "answer", Role: "assistant"}, Parts: []zcodeNativePart{{Type: "text", Text: "Accepted answer."}}},
			}
			a.dispatchMu.Lock()
			err := a.applyZCodeRecoveredMessagesLocked(owner, messages)
			a.unlockZCodeDispatch()
			require.Error(t, err)
			assert.Empty(t, a.generationBuffer.ScopeIDs())
			assert.Empty(t, owner.contentByScope)
		})
	}
}

func TestZCodeRecoveryCoverage_LateResponseExcludesAnAcceptedInterruptedPrefix(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"hello "}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	a.flushZCodeGenerationForTurn(owner, agent.MessageCompletionInterrupted)
	raw := zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"hello world","querySource":"main_turn","stopReason":"stop"}`)
	a.HandleOutput(raw)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	var supplement map[string]map[string]string
	require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &supplement))
	assert.Equal(t, "hello ", supplement["zcodeResponseText"]["acceptedPrefix"])
	var original zcodeEventEnvelope
	require.NoError(t, json.Unmarshal(rows[1].Content, &original))
	assert.Contains(t, string(original.Payload), `"content":"hello world"`, "the native response remains unchanged")
}

func TestZCodeRecoveryCoverage_RejectedResponsePreservesPendingReasoning(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"Accepted "}`))
	a.Mu.Lock()
	owner := a.turns["turn"]
	a.Mu.Unlock()
	a.flushZCodeGenerationKindForTurn(owner, agent.AssembledMessageKindText, agent.MessageCompletionInterrupted)
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"reasoning_delta","assistantMessageId":"message","delta":"Pending reasoning."}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"Conflicting answer","querySource":"main_turn","stopReason":"stop"}`))
	assert.Equal(t, []string{"Accepted "}, zcodeRecoveryTranscriptTexts(t, sink), "a rejected response must not commit reasoning")
	_, reasoning, present := a.generationBuffer.ScopeSnapshot(zcodeReasoningScope("message"))
	assert.True(t, present)
	assert.Equal(t, "Pending reasoning.", reasoning)
	a.Mu.Lock()
	assert.Zero(t, owner.contentByScope[zcodeReasoningScope("message")].submitted)
	a.Mu.Unlock()
	a.HandleOutput(zcodeTurnEventLine(t, 5, "turn", contracts.ZCodeEventSessionUpdated, `{"content":"Accepted suffix","querySource":"main_turn","stopReason":"stop"}`))
	assert.Equal(t, []string{"Accepted ", "Pending reasoning.", "suffix"}, zcodeRecoveryTranscriptTexts(t, sink))
}

func TestZCodeRecoveryEvidence_AbsentRuntimeProofDoesNotConfirmIdle(t *testing.T) {
	t.Parallel()
	for _, runtime := range []string{`{"eventSeq":1,"activeTurnId":""}`, `{"stateRevision":2,"activeTurnId":""}`} {
		a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
		a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":1}`))
		a.Mu.Lock()
		owner := a.turns["turn"]
		state := owner.recovery
		a.Mu.Unlock()
		var snap zcodeSessionReadSnapshot
		require.NoError(t, json.Unmarshal([]byte(`{"runtime":`+runtime+`,"projection":{"currentTurnId":"turn","turnCount":1,"status":"idle"}}`), &snap))
		settled, err := a.applyZCodeRecoverySnapshot(owner, state, snap)
		require.NoError(t, err)
		assert.False(t, settled)
		a.Mu.Lock()
		assert.Zero(t, state.idleSnapshots, "an absent revision or sequence cannot confirm native idle")
		a.Mu.Unlock()
	}
}

func TestZCodeRecoveryEvidence_ForeignProjectionDoesNotProveOwnerLiveness(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	owner := &zcodeTurnState{id: "turn"}
	state := &zcodeTurnRecoveryState{frozenSince: a.Clock().Now().Add(-zcodeTurnRecoveryFreezeLimit - time.Minute)}
	registerZCodeRecoveryForTest(a, owner, state)
	settled, err := a.applyZCodeRecoverySnapshot(owner, state, zcodeSessionReadSnapshot{Runtime: &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "other"}, Projection: &zcodeProjectionState{CurrentTurnID: "other", ContextUsed: 999, TurnCount: 2}})
	require.NoError(t, err)
	assert.True(t, settled, "another turn's projection cannot extend this owner's freeze clock")
}

func TestZCodeTurnRecovery_ProvisionalIdleOwnerReachesTheErrorPath(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	owner := &zcodeTurnState{id: "#turn-1", inputID: "accepted"}
	state := &zcodeTurnRecoveryState{frozenSince: a.Clock().Now().Add(-zcodeTurnRecoveryFreezeLimit - time.Minute)}
	registerZCodeRecoveryForTest(a, owner, state)
	snap := zcodeSessionReadSnapshot{Runtime: &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true}, Projection: &zcodeProjectionState{Status: "idle"}}
	settled, err := a.applyZCodeRecoverySnapshot(owner, state, snap)
	require.NoError(t, err)
	assert.True(t, settled)
	assert.False(t, a.PublishTurnActive().Active)
	notices := sink.LeapMuxNotifications()
	require.Len(t, notices, 1)
	assert.Equal(t, contracts.NotificationTypeAgentError, notices[0][contracts.NotificationFieldType])
}

func TestZCodeTurnRecovery_AcceptsNativeIdleShapeWithoutAnActiveID(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"idle", "error"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method == MethodSessionMessages {
					return json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user","anchor":{"turnId":"turn"}}},{"info":{"id":"assistant","role":"assistant","parentID":"user","anchor":{"turnId":"turn"}},"parts":[{"id":"text","type":"text","text":"Recovered native answer"}]}]}`)
				}
				return nil
			})
			a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":0,"messageId":"user"}`))
			reason := contracts.ZCodeStateReasonPromptCompleted
			if status == "error" {
				reason = contracts.ZCodeStateReasonPromptFailed
			}
			a.HandleOutput([]byte(`{"method":"state.updated","params":{"scope":"session","sessionId":"sess-1","revision":2,"reason":"` + reason + `","patch":{}}}`))
			a.Mu.Lock()
			owner := a.turns["turn"]
			state := owner.recovery
			a.Mu.Unlock()
			var snapshot zcodeSessionReadSnapshot
			require.NoError(t, json.Unmarshal([]byte(`{"runtime":{"stateRevision":2,"eventSeq":1},"projection":{"currentTurnId":"turn","turnCount":1,"status":"`+status+`"}}`), &snapshot))
			settled, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
			require.NoError(t, err)
			assert.False(t, settled)
			settled, err = a.applyZCodeRecoverySnapshot(owner, state, snapshot)
			require.NoError(t, err)
			assert.True(t, settled)
			ends := 0
			for _, row := range sink.Messages() {
				if row.TurnEnd {
					ends++
					if status == "error" {
						assert.Equal(t, agent.MessageCompletionError, row.Completion)
					}
				}
			}
			assert.Equal(t, 1, ends)
		})
	}
}

func TestZCodePromptBoundary_DoesNotCompareIndependentNativeCounters(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	owner := &zcodeTurnState{id: "turn", admissionRevision: 2}
	a.Mu.Lock()
	evidence := a.attributeZCodePromptBoundaryLocked(owner, contracts.ZCodeStateReasonPromptCompleted, 3, zcodeProjectionState{CurrentTurnID: "turn", TurnCount: 7}, nil)
	a.Mu.Unlock()
	assert.NotNil(t, evidence, "exact turn identity and revision own the boundary; the runtime ordinal is not a projection count")
}

func TestZCodeProvisionalRecoveryStartsItsFullFreezeAfterNativeIdle(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithClock(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{}, clock)
	owner := &zcodeTurnState{id: "#turn-1", inputID: "accepted"}
	state := &zcodeTurnRecoveryState{}
	registerZCodeRecoveryForTest(a, owner, state)
	foreign := zcodeSessionReadSnapshot{Runtime: &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "older"}, Projection: &zcodeProjectionState{Status: "running"}}
	idle := zcodeSessionReadSnapshot{Runtime: &zcodeRuntimeState{stateRevisionKnown: true, eventSeqKnown: true}, Projection: &zcodeProjectionState{Status: "idle"}}
	observe := func(snapshot zcodeSessionReadSnapshot, want bool) {
		t.Helper()
		settled, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
		require.NoError(t, err)
		require.Equal(t, want, settled)
	}
	observe(foreign, false)
	clock.Advance(2 * zcodeTurnRecoveryFreezeLimit).MustWait(ctx)
	observe(foreign, false)
	clock.Advance(zcodeTurnRecoveryQuietInterval).MustWait(ctx)
	observe(idle, false)
	clock.Advance(zcodeTurnRecoveryFreezeLimit - zcodeTurnRecoveryQuietInterval).MustWait(ctx)
	observe(idle, false)
	clock.Advance(zcodeTurnRecoveryQuietInterval).MustWait(ctx)
	observe(idle, true)
	notices := sink.LeapMuxNotifications()
	require.Len(t, notices, 1)
	assert.Equal(t, contracts.NotificationTypeAgentError, notices[0][contracts.NotificationFieldType])
}

func TestZCodeTurnRecovery_OnlyOwnerProgressRestartsTheFreezeInterval(t *testing.T) {
	t.Parallel()
	for _, ownProgress := range []bool{true, false} {
		t.Run(fmt.Sprintf("own progress %t", ownProgress), func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			ctx := testutil.DeadlineContext(t)
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithClock(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{}, clock)
			owner := &zcodeTurnState{id: "foreground", contentByScope: make(map[string]*zcodeScopeContent), toolInvocationIDs: make(map[string]struct{})}
			state := &zcodeTurnRecoveryState{}
			registerZCodeRecoveryForTest(a, owner, state)
			background := &zcodeTurnState{id: "background", background: true, contentByScope: make(map[string]*zcodeScopeContent), toolInvocationIDs: make(map[string]struct{})}
			registerZCodeRecoveryForTest(a, background, &zcodeTurnRecoveryState{})
			snapshot := zcodeSessionReadSnapshot{
				Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "foreground"},
				Projection: &zcodeProjectionState{CurrentTurnID: "foreground", TurnCount: 1, Status: "running"},
			}
			settled, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
			require.NoError(t, err)
			require.False(t, settled)
			streamOwner := "background"
			if ownProgress {
				streamOwner = "foreground"
			}
			for index := 1; index <= 4; index++ {
				clock.Advance(zcodeTurnRecoveryFreezeLimit / 3).MustWait(ctx)
				a.HandleOutput(zcodeTurnEventLine(t, int64(index), streamOwner, contracts.ZCodeEventModelStreaming,
					`{"kind":"text_delta","assistantMessageId":"message","delta":"progress "}`))
				snapshot.Runtime.EventSeq = int64(index)
				settled, err = a.applyZCodeRecoverySnapshot(owner, state, snapshot)
				require.NoError(t, err)
				if !ownProgress && index == 3 {
					require.True(t, settled)
					break
				}
				require.False(t, settled)
			}
			a.Mu.Lock()
			ownerSettled, backgroundSettled := owner.settled, background.settled
			a.Mu.Unlock()
			assert.Equal(t, !ownProgress, ownerSettled)
			assert.False(t, backgroundSettled)
			assert.True(t, a.PublishTurnActive().Active)
			if ownProgress {
				assert.Empty(t, sink.Notifications())
				assert.Equal(t, 4*len("progress "), a.generationBuffer.RetainedTextLenForTest(zcodeTextScope("message")))
			} else {
				notices := sink.Notifications()
				require.Len(t, notices, 1)
				assert.Equal(t, contracts.NotificationTypeAgentError, notices[0][contracts.NotificationFieldType])
				assert.Equal(t, 3*len("progress "), a.generationBuffer.RetainedTextLenForTest(zcodeTextScope("message")), "foreground settlement must preserve the background stream")
			}
		})
	}
}

func TestZCodeTurnRecovery_HeldFinalReadRejectsChangedOwnership(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"owner progress", "replay gap", "native lifetime", "context cancellation", "owner replacement"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
			owner := &zcodeTurnState{id: "turn", anchorMessageID: "user", admissionRevision: 1, contentByScope: make(map[string]*zcodeScopeContent), toolInvocationIDs: make(map[string]struct{})}
			state := &zcodeTurnRecoveryState{boundary: &zcodePromptBoundaryEvidence{
				reason: contracts.ZCodeStateReasonPromptCompleted, revision: 2,
				raw: []byte(`{"type":"state.updated","payload":{"scope":"session","sessionId":"sess-1","reason":"prompt_completed","revision":2}}`),
			}}
			registerZCodeRecoveryForTest(a, owner, state)
			a.Mu.Lock()
			identity := a.captureZCodeObservationIdentityLocked()
			a.Mu.Unlock()
			snapshot := zcodeSessionReadSnapshot{
				identity:   &identity,
				Runtime:    &zcodeRuntimeState{stateRevisionKnown: true, eventSeqKnown: true, StateRevision: 2},
				Projection: &zcodeProjectionState{CurrentTurnID: "turn", TurnCount: 1, Status: "idle"},
			}
			first, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
			require.NoError(t, err)
			require.False(t, first)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type recoveryResult struct {
				settled bool
				err     error
			}
			done := make(chan recoveryResult, 1)
			go func() {
				settled, err := a.applyZCodeRecoverySnapshotContext(ctx, owner, state, snapshot)
				done <- recoveryResult{settled, err}
			}()
			request := waitZCodeRequest(t, stdin, MethodSessionMessages)
			wantFinished := false
			var replacementOwner *zcodeTurnState
			switch change {
			case "owner progress":
				a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"fresh","delta":"fresh output"}`))
			case "replay gap":
				a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"gapped","delta":"not dispatched"}`))
			case "native lifetime":
				var fresh zcodeStateSnapshot
				require.NoError(t, json.Unmarshal([]byte(`{"session":{"sessionId":"sess-1"}}`), &fresh))
				a.adoptCompleteZCodeSessionSnapshot(fresh, true)
				wantFinished = true
			case "context cancellation":
				cancel()
				wantFinished = true
			case "owner replacement":
				a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"turnNumber":2,"messageId":"replacement-user"}`))
				a.Mu.Lock()
				replacementOwner = a.turns["turn"]
				a.Mu.Unlock()
				require.NotSame(t, owner, replacementOwner)
				wantFinished = true
			}
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user"}},{"info":{"id":"stale","role":"assistant","parentID":"user","anchor":{"turnId":"turn"}},"parts":[{"id":"text","type":"text","text":"stale recovered output"}]}]}`)))
			var result recoveryResult
			select {
			case result = <-done:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the held reconciliation did not finish")
			}
			require.NoError(t, result.err)
			assert.Equal(t, wantFinished, result.settled)
			texts, _ := zcodePersistedTextsAndClosings(t, sink)
			assert.Empty(t, texts)
			assert.Empty(t, sink.LeapMuxNotifications())
			for _, row := range sink.Messages() {
				assert.False(t, row.TurnEnd)
			}
			switch change {
			case "owner progress":
				a.Mu.Lock()
				assert.Same(t, owner, a.turns["turn"])
				assert.Same(t, state, owner.recovery)
				assert.False(t, owner.settled)
				a.Mu.Unlock()
				assert.Equal(t, len("fresh output"), a.generationBuffer.RetainedTextLenForTest(zcodeTextScope("fresh")))
			case "replay gap":
				a.dispatchMu.Lock()
				a.Mu.Lock()
				assert.Equal(t, int64(0), a.lastSeq)
				assert.False(t, owner.settled)
				a.Mu.Unlock()
				assert.Len(t, a.replay.pending, 1)
				a.unlockZCodeDispatch()
			case "native lifetime":
				a.Mu.Lock()
				assert.NotEqual(t, identity.epoch, a.sessionEpoch)
				assert.Empty(t, a.turns)
				a.Mu.Unlock()
			case "context cancellation":
				a.Mu.Lock()
				assert.False(t, owner.settled)
				a.Mu.Unlock()
			case "owner replacement":
				a.Mu.Lock()
				assert.Same(t, replacementOwner, a.turns["turn"])
				assert.False(t, replacementOwner.settled)
				a.Mu.Unlock()
			}
		})
	}
}

func TestZCodeTurnRecovery_HeldAnchorReadPreservesANativeIDCollision(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	owner := &zcodeTurnState{id: "#turn-accepted", inputID: "accepted-input", contentByScope: make(map[string]*zcodeScopeContent), toolInvocationIDs: make(map[string]struct{})}
	state := &zcodeTurnRecoveryState{}
	registerZCodeRecoveryForTest(a, owner, state)
	snapshot := zcodeSessionReadSnapshot{
		Runtime:    &zcodeRuntimeState{activeTurnIDKnown: true, stateRevisionKnown: true, eventSeqKnown: true, ActiveTurnID: "native", StateRevision: 2},
		Projection: &zcodeProjectionState{CurrentTurnID: "native", TurnCount: 1, Status: "running"},
	}
	type recoveryResult struct {
		settled bool
		err     error
	}
	done := make(chan recoveryResult, 1)
	go func() {
		settled, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
		done <- recoveryResult{settled, err}
	}()
	request := waitZCodeRequest(t, stdin, MethodSessionMessages)
	a.HandleOutput(zcodeTurnEventLine(t, 1, "native", contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"inputSource":"background_task"}`))
	a.Mu.Lock()
	nativeOwner := a.turns["native"]
	a.Mu.Unlock()
	require.NotNil(t, nativeOwner)
	require.NotSame(t, owner, nativeOwner)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"messages":[{"info":{"id":"accepted-user","role":"user","anchor":{"turnId":"native","sourceCommandId":"accepted-input"}}}]}`)))
	var result recoveryResult
	select {
	case result = <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the held anchor read did not finish")
	}
	require.NoError(t, result.err)
	assert.False(t, result.settled)
	a.Mu.Lock()
	assert.Same(t, owner, a.turns["#turn-accepted"])
	assert.Same(t, nativeOwner, a.turns["native"])
	assert.Equal(t, "#turn-accepted", owner.id)
	assert.Empty(t, owner.anchorMessageID)
	assert.Same(t, state, owner.recovery)
	assert.False(t, owner.settled)
	assert.True(t, nativeOwner.background)
	a.Mu.Unlock()
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestZCodeTurnRecovery_ResumedOwnerRecoversExactNativeTurnMessages(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "Recovered "} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			var resumed zcodeStateSnapshot
			resumed.Runtime.ActiveTurnID = "resumed"
			a.Mu.Lock()
			a.createResumedZCodeTurnLocked(resumed)
			owner := a.turns["resumed"]
			a.Mu.Unlock()
			require.NotNil(t, owner)
			state := &zcodeTurnRecoveryState{}
			registerZCodeRecoveryForTest(a, owner, state)
			if prefix != "" {
				a.HandleOutput(zcodeTurnEventLine(t, 1, "resumed", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"assistant","delta":"Recovered "}`))
			}
			a.HandleOutput([]byte(`{"method":"state.updated","params":{"scope":"session","sessionId":"sess-1","revision":2,"reason":"prompt_completed","patch":{}}}`))
			zcodeRespondToRequestsForTest(t, a, func(request zcodeSentRequest) json.RawMessage {
				if request.Method == MethodSessionMessages {
					return json.RawMessage(`{"messages":[{"info":{"id":"history","role":"assistant","anchor":{"turnId":"old"}},"parts":[{"id":"old-text","type":"text","text":"Historical answer"}]},{"info":{"id":"assistant","role":"assistant","anchor":{"turnId":"resumed"}},"parts":[{"id":"text","type":"text","text":"Recovered answer"}]},{"info":{"id":"foreign","role":"assistant","anchor":{"turnId":"background"}},"parts":[{"id":"foreign-text","type":"text","text":"Foreign answer"}]}]}`)
				}
				return nil
			})
			var snapshot zcodeSessionReadSnapshot
			require.NoError(t, json.Unmarshal([]byte(`{"runtime":{"stateRevision":2,"eventSeq":1},"projection":{"currentTurnId":"resumed","status":"idle"}}`), &snapshot))
			settled, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
			require.NoError(t, err)
			assert.False(t, settled)
			settled, err = a.applyZCodeRecoverySnapshot(owner, state, snapshot)
			require.NoError(t, err)
			assert.True(t, settled)
			texts, _ := zcodePersistedTextsAndClosings(t, sink)
			assert.Equal(t, []string{"Recovered answer"}, texts)
			ends := 0
			for _, row := range sink.Messages() {
				if row.TurnEnd {
					ends++
				}
			}
			assert.Equal(t, 1, ends)
		})
	}
}

func TestZCodeBackgroundRecoveryRecordsOwnCompletion(t *testing.T) {
	t.Parallel()
	for _, outcome := range []struct {
		completion agent.MessageCompletion
		ordinal    leapmuxv1.MessageCompletion
	}{
		{agent.MessageCompletionComplete, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE},
		{agent.MessageCompletionError, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_ERROR},
		{agent.MessageCompletionInterrupted, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED},
	} {
		t.Run(string(outcome.completion), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			original := []byte(" {\"method\":\"state.updated\", \"params\":{\"scope\":\"session\",\"reason\":\"prompt_completed\",\"future\":\"retained\"}} ")
			a.persistZCodeRecoveryEvidence(&zcodeTurnState{background: true}, outcome.completion, original)
			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1)
			var record struct {
				Type    string `json:"type"`
				Payload struct {
					Original   string                      `json:"original"`
					Completion leapmuxv1.MessageCompletion `json:"completion"`
				} `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(rows[0].Content, &record))
			assert.Equal(t, "zcode_background_turn_end", record.Type)
			assert.Equal(t, string(original), record.Payload.Original)
			assert.Equal(t, outcome.ordinal, record.Payload.Completion)
			assert.Empty(t, rows[0].Completion, "each notification retains its own outcome")
		})
	}
}

type zcodeCanonicalEndSink struct {
	*agenttest.ControlSink
	ended          chan struct{}
	endOnce        sync.Once
	retryMu        sync.Mutex
	retryCancels   int
	workerMu       sync.Mutex
	workerMessages []agent.MessageContent
	order          []string
	inactive       chan struct{}
	inactiveOnce   sync.Once
}

func (sink *zcodeCanonicalEndSink) observeNativeEnd(content agent.MessageContent) {
	var record struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content.Original, &record) == nil && (record.Type == contracts.ZCodeEventTurnCompleted || record.Type == contracts.ZCodeEventTurnFailed) {
		sink.endOnce.Do(func() { close(sink.ended) })
	}
}
func (sink *zcodeCanonicalEndSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	err := sink.ControlSink.PersistMessage(source, content, span)
	if err == nil {
		sink.observeNativeEnd(content)
	}
	return err
}
func (sink *zcodeCanonicalEndSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	err := sink.ControlSink.PersistTurnEnd(content, span)
	if err == nil {
		sink.observeNativeEnd(content)
	}
	return err
}

type zcodeCanonicalRetryWriter struct {
	agent.AutoRetryWriter
	sink *zcodeCanonicalEndSink
}

func (writer zcodeCanonicalRetryWriter) CancelServerErrorRetry() {
	writer.sink.retryMu.Lock()
	writer.sink.retryCancels++
	writer.sink.order = append(writer.sink.order, "retry")
	writer.sink.retryMu.Unlock()
	writer.AutoRetryWriter.CancelServerErrorRetry()
}
func (sink *zcodeCanonicalEndSink) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	stored, err := sink.ControlSink.PersistWorkerNotification(content)
	if err == nil && stored {
		sink.workerMu.Lock()
		sink.workerMessages = append(sink.workerMessages, content.Clone())
		sink.workerMu.Unlock()
	}
	return stored, err
}
func (sink *zcodeCanonicalEndSink) CaptureAutoRetry() agent.AutoRetryWriter {
	return zcodeCanonicalRetryWriter{AutoRetryWriter: sink.ControlSink.CaptureAutoRetry(), sink: sink}
}
func (sink *zcodeCanonicalEndSink) SetTurnState(state agent.TurnState, seq uint64) {
	sink.ControlSink.SetTurnState(state, seq)
	if !state.Active {
		sink.retryMu.Lock()
		sink.order = append(sink.order, "inactive")
		sink.retryMu.Unlock()
		if sink.inactive != nil {
			sink.inactiveOnce.Do(func() { close(sink.inactive) })
		}
	}
}

func (sink *zcodeCanonicalEndSink) cancelCount() int {
	sink.retryMu.Lock()
	defer sink.retryMu.Unlock()
	return sink.retryCancels
}

func zcodeCanonicalReplayFixture(t *testing.T) (*Agent, *zcodeCanonicalEndSink, *zcodeRecordedStdin) {
	t.Helper()
	return zcodeCanonicalReplayFixtureWithClock(t, quartz.NewReal(), true)
}

func zcodeCanonicalReplayFixtureWithClock(t *testing.T, clock quartz.Clock, emitNativeEnd bool) (*Agent, *zcodeCanonicalEndSink, *zcodeRecordedStdin) {
	t.Helper()
	sink := &zcodeCanonicalEndSink{ControlSink: &agenttest.ControlSink{}, ended: make(chan struct{}), inactive: make(chan struct{})}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithClock(t, agent.NewProviderServices(sink), stdin, clock)
	sink.UpdateSessionID("sess-1")
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn-A", contracts.ZCodeEventTurnStarted, `{"messageId":"user"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn-A", contracts.ZCodeEventModelStreaming, `{"kind":"reasoning_delta","assistantMessageId":"message","delta":"H"}`))
	a.dispatchMu.Lock()
	a.replay.highWatermark = 3
	a.dispatchMu.Unlock()
	respond := func(request zcodeSentRequest) json.RawMessage {
		switch request.Method {
		case MethodSessionEvents:
			return json.RawMessage(`{"events":[{"sessionId":"sess-1","turnId":"turn-A","seq":3,"eventId":"source-e","type":"model.streaming","payload":{"kind":"reasoning_delta","assistantMessageId":"message","partId":"reasoning","delta":"e"}}]}`)
		default:
			return nil
		}
	}
	a.SetStdinForTest(agenttest.NopStdin(zcodeInputWriter(func(raw []byte) (int, error) {
		if _, err := stdin.Write(raw); err != nil {
			return 0, err
		}
		var request zcodeSentRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		if response := respond(request); response != nil {
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), response))
		}
		return len(raw), nil
	})))
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	observed := owner.contentByScope[zcodeReasoningScope("message")].text()
	a.Mu.Unlock()
	require.Equal(t, "H", observed, "an archive batch must not alter the accepted live prefix")
	// A forced flush proves that replay does not merely repair an unsubmitted duplicate.
	a.dispatchMu.Lock()
	a.flushZCodeGenerationKindForTurn(owner, agent.AssembledMessageKindReasoning, agent.MessageCompletionComplete)
	a.unlockZCodeDispatch()
	a.HandleOutput(zcodeTurnEventLine(t, 4, "turn-A", contracts.ZCodeEventSessionUpdated, `{"content":"Answer","stopReason":"end_turn"}`))
	a.Mu.Lock()
	a.latestContextUsage = map[string]any{"input_tokens": int64(3), "output_tokens": int64(0), "context_tokens": int64(3)}
	a.sessionCostUsd, a.sessionCostKnown = 1, true
	a.Mu.Unlock()
	if emitNativeEnd {
		a.HandleOutput(zcodeTurnEventLine(t, 5, "turn-A", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	}
	return a, sink, stdin
}

func zcodeCanonicalMessages() json.RawMessage {
	return json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user"}},{"info":{"id":"message","role":"assistant","parentID":"user","anchor":{"turnId":"turn-A"}},"parts":[{"id":"reasoning","type":"reasoning","text":"Hello"},{"id":"text","type":"text","text":"Answer"}]}]}`)
}

func waitZCodeCanonicalEnd(t *testing.T, sink *zcodeCanonicalEndSink) {
	t.Helper()
	select {
	case <-sink.ended:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the canonical native end did not persist")
	}
}

func TestZCodeNativeEnd_ReconcilesCanonicalReasoningBeforeItsDivider(t *testing.T) {
	t.Parallel()
	a, sink, stdin := zcodeCanonicalReplayFixture(t)
	request := waitZCodeRequest(t, stdin, MethodSessionMessages)
	select {
	case <-sink.ended:
		t.Fatal("the native end persisted before canonical messages arrived")
	default:
	}
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), zcodeCanonicalMessages()))
	waitZCodeCanonicalEnd(t, sink)
	var reasoning string
	ends := 0
	for _, row := range sink.Messages() {
		kind, text, _, assembled := agenttest.DecodeAssembledMessage(row.Content)
		if assembled && kind == agent.AssembledMessageKindReasoning {
			reasoning += text
		}
		var record struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(row.Content, &record))
		if record.Type == contracts.ZCodeEventTurnCompleted {
			ends++
		}
	}
	assert.Equal(t, "Hello", reasoning)
	assert.Equal(t, 1, ends)
	assert.False(t, a.PublishTurnActive().Active)
}

func TestZCodeNativeEnd_DelayedCanonicalOutputPreservesTheNewForeground(t *testing.T) {
	t.Parallel()
	for _, localStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("local stop %t", localStop), func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := zcodeCanonicalReplayFixture(t)
			request := waitZCodeRequest(t, stdin, MethodSessionMessages)
			if localStop {
				a.Mu.Lock()
				owner := a.turns["turn-A"]
				a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, owner.id, owner.generation)
				generation := a.stopWindow.generation
				a.Mu.Unlock()
				a.endStoppedZCodeTurn(generation)
				assert.NotContains(t, zcodeRecoveryTranscriptTexts(t, sink.ControlSink), "Answer", "a local stop must retain the answer for the outstanding canonical read")
			}
			a.HandleOutput(zcodeTurnEventLine(t, 6, "turn-B", contracts.ZCodeEventTurnStarted, `{"messageId":"user-B"}`))
			a.HandleOutput(zcodeTurnEventLine(t, 7, "turn-B", contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message-B","delta":"B keeps working"}`))
			a.Mu.Lock()
			a.latestContextUsage = map[string]any{"context_tokens": int64(99)}
			a.sessionCostUsd = 99
			a.Mu.Unlock()
			progress, cancels := sink.ProgressSnapshot(), sink.cancelCount()
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), zcodeCanonicalMessages()))
			waitZCodeCanonicalEnd(t, sink)
			assert.Equal(t, []string{"H", "ello", "Answer"}, zcodeRecoveryTranscriptTexts(t, sink.ControlSink))
			assert.Equal(t, progress, sink.ProgressSnapshot(), "the older output must not reset the new foreground")
			assert.Equal(t, cancels, sink.cancelCount(), "the older end must not cancel the new foreground's retry")
			a.Mu.Lock()
			assert.Equal(t, "turn-B", a.foregroundTurnID)
			assert.False(t, a.turns["turn-B"].settled)
			a.Mu.Unlock()
			ends := 0
			for _, row := range sink.Messages() {
				var record struct {
					Type   string `json:"type"`
					TurnID string `json:"turnId"`
				}
				require.NoError(t, json.Unmarshal(row.Content, &record))
				if record.Type != contracts.ZCodeEventTurnCompleted || record.TurnID != "turn-A" {
					continue
				}
				ends++
				assert.False(t, row.TurnEnd, "a historical divider cannot settle the current service activity")
				var metadata map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(row.Metadata, &metadata))
				assert.JSONEq(t, `{"input_tokens":3,"output_tokens":0,"context_tokens":3}`, string(metadata[contracts.SessionInfoKeyContextUsage]))
				assert.JSONEq(t, `1`, string(metadata[contracts.SessionInfoKeyTotalCostUsd]))
			}
			assert.Equal(t, 1, ends)
		})
	}
}

func TestZCodeNativeEnd_CanonicalReadCannotEnterAReplacementSession(t *testing.T) {
	t.Parallel()
	a, sink, stdin := zcodeCanonicalReplayFixture(t)
	request := waitZCodeRequest(t, stdin, MethodSessionMessages)
	var replacement zcodeStateSnapshot
	replacement.Session.SessionID = "replacement"
	a.adoptCompleteZCodeSessionSnapshot(replacement, true)
	before := sink.MessageCount()
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), zcodeCanonicalMessages()))
	a.Mu.Lock()
	assert.Empty(t, a.turns)
	assert.Equal(t, "replacement", a.sessionID)
	a.Mu.Unlock()
	assert.Equal(t, before, sink.MessageCount())
	select {
	case <-sink.ended:
		t.Fatal("the old native end entered the replacement")
	default:
	}
}

func TestZCodeNativeEnd_RetryBookkeepingPrecedesActivityRelease(t *testing.T) {
	t.Parallel()
	sink := &zcodeCanonicalEndSink{ControlSink: &agenttest.ControlSink{}, ended: make(chan struct{}), inactive: make(chan struct{})}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn", contracts.ZCodeEventTurnStarted, `{"messageId":"user"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	select {
	case <-sink.inactive:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native end did not release activity")
	}
	sink.retryMu.Lock()
	order := append([]string(nil), sink.order...)
	sink.retryMu.Unlock()
	assert.Equal(t, []string{"retry", "inactive"}, order)
}

func TestZCodeNativeEnd_RearmsAWatcherThatWasAlreadyExiting(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	quiet := clock.Trap().NewTimer(zcodeTurnRecoveryTimerTag)
	t.Cleanup(quiet.Close)
	a, sink, stdin := zcodeCanonicalReplayFixtureWithClock(t, clock, false)
	firstTimer := quiet.MustWait(ctx)
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	oldState := owner.recovery
	a.Mu.Unlock()
	require.NotNil(t, oldState)
	originalCancel := oldState.cancel
	var announceOnce sync.Once
	a.Mu.Lock()
	oldState.cancel = func() {
		announceOnce.Do(func() {
			a.HandleOutput(zcodeTurnEventLine(t, 5, "turn-A", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
		})
		originalCancel()
	}
	a.Mu.Unlock()
	firstTimer.MustRelease(ctx)
	clock.Advance(zcodeTurnRecoveryQuietInterval).MustWait(ctx)
	_ = waitZCodeRequest(t, stdin, MethodSessionRead)
	originalCancel()
	replacementTimer := quiet.MustWait(ctx)
	a.Mu.Lock()
	newState, pending := owner.recovery, owner.pendingNative
	a.Mu.Unlock()
	require.NotNil(t, pending)
	require.NotNil(t, newState)
	require.NotSame(t, oldState, newState)
	replacementTimer.MustRelease(ctx)
	messages := waitZCodeRequest(t, stdin, MethodSessionMessages)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, messages), zcodeCanonicalMessages()))
	zcodeTestReceive(t, sink.inactive)
	ends := 0
	var reasoning string
	for _, row := range sink.Messages() {
		kind, text, _, assembled := agenttest.DecodeAssembledMessage(row.Content)
		if assembled && kind == agent.AssembledMessageKindReasoning {
			reasoning += text
		}
		var event struct {
			Type   string `json:"type"`
			TurnID string `json:"turnId"`
		}
		require.NoError(t, json.Unmarshal(row.Content, &event))
		if event.Type == contracts.ZCodeEventTurnCompleted && event.TurnID == "turn-A" {
			ends++
		}
	}
	assert.Equal(t, "Hello", reasoning)
	assert.Equal(t, 1, ends)
	assert.Len(t, stdin.RequestsFor(MethodSessionMessages), 1)
	a.Mu.Lock()
	assert.True(t, owner.finalized)
	assert.Nil(t, owner.pendingNative)
	assert.Nil(t, owner.recovery)
	a.Mu.Unlock()
}

func TestZCodeNativeEnd_DeadlinePreservesItsEndAndReportsMissingOutput(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	quiet := clock.Trap().NewTimer(zcodeTurnRecoveryTimerTag)
	t.Cleanup(quiet.Close)
	a, sink, stdin := zcodeCanonicalReplayFixtureWithClock(t, clock, true)
	firstTimer := quiet.MustWait(ctx)
	firstTimer.MustRelease(ctx)
	firstRead := waitZCodeRequest(t, stdin, MethodSessionMessages)
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	pending := owner.pendingNative
	a.Mu.Unlock()
	require.NotNil(t, pending)
	require.NotNil(t, pending.source.Publication)
	expectedOwner := pending.source.Publication.Owner()
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, firstRead), ErrInternal, "the native message store is unavailable"))
	retryTimer := quiet.MustWait(ctx)
	assert.Equal(t, zcodeTurnRecoveryQuietInterval, retryTimer.Duration)
	clock.Advance(zcodeTurnRecoveryFreezeLimit - zcodeTurnRecoveryQuietInterval).MustWait(ctx)
	retryTimer.MustRelease(ctx)
	clock.Advance(zcodeTurnRecoveryQuietInterval).MustWait(ctx)
	zcodeTestReceive(t, sink.inactive)
	assert.Len(t, stdin.RequestsFor(MethodSessionMessages), 1, "an expired settlement must issue no further canonical read")
	ends := 0
	for _, row := range sink.Messages() {
		var event struct {
			Type    string `json:"type"`
			TurnID  string `json:"turnId"`
			Payload struct {
				ResultType string `json:"resultType"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(row.Content, &event))
		if event.Type == contracts.ZCodeEventTurnCompleted && event.TurnID == "turn-A" {
			ends++
			assert.Equal(t, "success", event.Payload.ResultType)
		}
	}
	assert.Equal(t, 1, ends)
	sink.workerMu.Lock()
	workers := append([]agent.MessageContent(nil), sink.workerMessages...)
	sink.workerMu.Unlock()
	require.Len(t, workers, 1)
	assert.Equal(t, "sess-1", workers[0].AgentSessionID)
	require.NotNil(t, workers[0].Publication)
	assert.Same(t, expectedOwner, workers[0].Publication.Owner())
	var failure map[string]any
	require.NoError(t, json.Unmarshal(workers[0].Original, &failure))
	assert.Equal(t, contracts.NotificationTypeAgentError, failure[contracts.NotificationFieldType])
	assert.Equal(t, "ZCode ended this turn, but LeapMux could not restore all of its output.", failure[contracts.NotificationFieldError])
	require.Len(t, sink.Notifications(), 1)
	a.Mu.Lock()
	assert.True(t, owner.finalized)
	assert.True(t, owner.settled)
	assert.Nil(t, owner.pendingNative)
	assert.Nil(t, owner.recovery)
	a.Mu.Unlock()
}

func TestZCodeNativeEnd_AnOwnerFirstSeenInReplayRestoresCanonicalReasoning(t *testing.T) {
	t.Parallel()
	sink := &zcodeCanonicalEndSink{ControlSink: &agenttest.ControlSink{}, ended: make(chan struct{}), inactive: make(chan struct{})}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	sink.UpdateSessionID("sess-1")
	a.dispatchMu.Lock()
	a.replay.highWatermark = 4
	a.dispatchMu.Unlock()
	a.SetStdinForTest(agenttest.NopStdin(zcodeInputWriter(func(raw []byte) (int, error) {
		if _, err := stdin.Write(raw); err != nil {
			return 0, err
		}
		var request zcodeSentRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		if request.Method == MethodSessionEvents {
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{"events":[
{"sessionId":"sess-1","turnId":"turn-A","seq":1,"type":"turn.started","payload":{"messageId":"user"}},
{"sessionId":"sess-1","turnId":"turn-A","seq":2,"type":"model.streaming","payload":{"kind":"reasoning_delta","assistantMessageId":"message","delta":"Hello"}},
{"sessionId":"sess-1","turnId":"turn-A","seq":3,"type":"session.updated","payload":{"content":"Answer","stopReason":"end_turn"}},
{"sessionId":"sess-1","turnId":"turn-A","seq":4,"type":"turn.completed","payload":{"resultType":"success"}}
]}`)))
		}
		return len(raw), nil
	})))
	require.NoError(t, a.replayZCodeEvents(t.Context()))
	request := waitZCodeRequest(t, stdin, MethodSessionMessages)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), zcodeCanonicalMessages()))
	zcodeTestReceive(t, sink.inactive)
	var reasoning string
	for _, row := range sink.Messages() {
		kind, text, _, assembled := agenttest.DecodeAssembledMessage(row.Content)
		if assembled && kind == agent.AssembledMessageKindReasoning {
			reasoning += text
		}
	}
	assert.Equal(t, "Hello", reasoning)
	assert.Len(t, zcodeNativeTurnEndRows(t, sink.Messages()), 1)
}

func TestZCodeNativeEnd_SupersedesAnOlderIdleRecoveryRead(t *testing.T) {
	t.Parallel()
	a, sink, stdin := zcodeCanonicalReplayFixtureWithClock(t, quartz.NewReal(), false)
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	state := owner.recovery
	state.boundary = &zcodePromptBoundaryEvidence{reason: contracts.ZCodeStateReasonPromptCompleted, revision: 10, raw: []byte(`{"type":"state.updated","payload":{"scope":"session","reason":"prompt_completed"}}`)}
	a.Mu.Unlock()
	snapshot := zcodeSessionReadSnapshot{Runtime: &zcodeRuntimeState{StateRevision: 10, EventSeq: 4, stateRevisionKnown: true, eventSeqKnown: true}, Projection: &zcodeProjectionState{CurrentTurnID: "turn-A", Status: "idle"}}
	settled, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
	require.NoError(t, err)
	require.False(t, settled)
	type result struct {
		settled bool
		err     error
	}
	oldRead := make(chan result, 1)
	go func() {
		done, err := a.applyZCodeRecoverySnapshot(owner, state, snapshot)
		oldRead <- result{done, err}
	}()
	first := waitZCodeRequestNumber(t, stdin, MethodSessionMessages, 0)
	a.HandleOutput(zcodeTurnEventLine(t, 5, "turn-A", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	second := waitZCodeRequestNumber(t, stdin, MethodSessionMessages, 1)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, first), json.RawMessage(`{"messages":[{"info":{"id":"user","role":"user"}},{"info":{"id":"message","role":"assistant","parentID":"user","anchor":{"turnId":"turn-A"}},"parts":[{"id":"reasoning","type":"reasoning","text":"stale output"},{"id":"text","type":"text","text":"stale answer"}]}]}`)))
	old := zcodeTestReceive(t, oldRead)
	require.NoError(t, old.err)
	assert.False(t, old.settled)
	select {
	case <-sink.ended:
		t.Fatal("the older read settled the native end")
	default:
	}
	for _, row := range sink.Messages() {
		assert.NotContains(t, string(row.Content), "stale output")
	}
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second), zcodeCanonicalMessages()))
	zcodeTestReceive(t, sink.inactive)
	assert.Len(t, zcodeNativeTurnEndRows(t, sink.Messages()), 1)
}

func TestZCodeNativeEnd_PreservesToolsOfALocallySettledOwner(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn-A", contracts.ZCodeEventTurnStarted, `{"messageId":"user-A"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 2, "turn-A", contracts.ZCodeEventToolUpdated, `{"kind":"scheduled","assistantMessageId":"message-A","toolCallId":"tool-A","toolName":"Bash","input":{"command":"pwd"}}`))
	a.Mu.Lock()
	owner := a.turns["turn-A"]
	a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, owner.id, owner.generation)
	generation := a.stopWindow.generation
	a.Mu.Unlock()
	a.endStoppedZCodeTurn(generation)
	a.HandleOutput(zcodeTurnEventLine(t, 3, "turn-B", contracts.ZCodeEventTurnStarted, `{"messageId":"user-B"}`))
	a.HandleOutput(zcodeTurnEventLine(t, 4, "turn-B", contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
	dividers := zcodeNativeTurnEndRows(t, sink.Messages())
	require.Len(t, dividers, 1)
	assert.False(t, dividers[0].TurnEnd, "an older launched tool still owns activity")
	a.Mu.Lock()
	invocation := a.resolveZCodeInvocationLocked(owner, "tool-A", "message-A", false)
	open := invocation != nil && !invocation.final
	a.Mu.Unlock()
	assert.True(t, open)
	assert.Equal(t, "Bash", a.sink.GetSpanType(zcodeTestSpanID(t, &sink.Sink, "tool-A")))
}

func TestZCodeRecoveryCoverage_ReusesCanonicalStringsForRecoveredSuffixes(t *testing.T) {
	const size = 1024 * 1024
	text := strings.Repeat("x", size)
	a := newZCodeTestAgent(t, agent.NewProviderServices(agenttest.Nop()))
	owner := &zcodeTurnState{}
	messages := []zcodeNativeMessage{{Info: zcodeNativeMessageInfo{ID: "message"}, Parts: []zcodeNativePart{{Type: "text", Text: text}}}}
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			a.generationBuffer.Reset()
			owner.contentByScope = nil
			if err := a.applyZCodeRecoveredMessagesLocked(owner, messages); err != nil {
				b.Fatal(err)
			}
		}
	})
	// The budget covers canonical aggregation, retained bytes, and the open buffer.
	// A recovered suffix reuses the immutable canonical string.
	assert.Less(t, result.AllocedBytesPerOp(), int64(size*7/2))
	_, buffered, present := a.generationBuffer.ScopeSnapshot(zcodeTextScope("message"))
	assert.True(t, present)
	assert.Equal(t, text, buffered)
}
