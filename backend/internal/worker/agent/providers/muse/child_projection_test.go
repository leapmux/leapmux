package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildBootstrapRejectsAnArrivalBeyondItsMemoryLimit(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	recovery := &viewRecovery{}
	state.viewRecovery = recovery
	raw := feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "reply", "field": "text", "delta": strings.Repeat("x", maxChildProjectionBytes)})
	assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
	assert.Nil(t, state.viewRecovery)
	assert.Empty(t, recovery.buffer)
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, raw, root.Messages()[0].Content)
	assert.Empty(t, child.Messages())
	require.Len(t, root.LeapMuxNotifications(), 1)
}

func TestChildRetirementRetainsUnprojectedEvidence(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	state.childViewPrefix = childViewPrefixReady
	a.viewWork.CloseAndWait()
	raw := feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "reply", "field": "text", "delta": "unproved text"})
	require.Empty(t, root.Messages())
	require.Len(t, state.projection.frames, 1)
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	states := a.retireSessionTreeLocked("session")
	a.stateMu.Unlock()
	a.retireSessionStates(states, "session", agent.MessageCompletionInterrupted)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, raw, root.Messages()[0].Content)
	assert.Empty(t, child.Messages())
	assert.Empty(t, state.projection.frames)
	assert.ErrorIs(t, state.projection.failed, agent.ErrChildRouteNotReady)
	var supplemental map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root.Messages()[0].SupplementalContent, &supplemental))
	assert.Contains(t, supplemental, contracts.MuseSupplementFieldViewProjectionUnavailable)
}

func TestChildProjectionDoesNotBorrowAnotherItemsDurablePosition(t *testing.T) {
	held := []byte(`{"method":"item/delta","params":{"sessionId":"child","itemId":"A","field":"text","delta":"unanchored A"}}`)
	durable := childHistoryItemEvent(t, "B-cursor", json.RawMessage(`{"itemId":"B","revision":1,"kind":"agentMessage","status":"completed","text":"durable B"}`), 2)
	p := newChildProjection()
	p.frames = []childProjectionFrame{{raw: held, serial: 1}, {raw: durable, serial: 2}}
	segment, anchor, err := nextChildProjectionSegment(p)
	require.NoError(t, err)
	require.NotNil(t, anchor)
	require.Len(t, segment, 1, "only B has a proved source; A must keep waiting for its own durable item")
	assert.Equal(t, []byte(durable), segment[0].raw)
	assert.Equal(t, int64(2), anchor.Last.Sequence)
	assert.Len(t, p.frames, 2, "selection must not consume unproved evidence")
}

func TestChildProjectionKeepsItsResumeCursorBeforeUnresolvedItems(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	accepted := "before-A"
	state.acceptedViewCursor = &accepted
	state.childViewPrefix = childViewPrefixReady
	held := []byte(`{"method":"item/delta","params":{"sessionId":"child","viewCursor":"A-delta","itemId":"A","delta":"unanchored A"}}`)
	durableB := childHistoryItemEvent(t, "B-cursor", json.RawMessage(`{"itemId":"B","revision":1,"kind":"agentMessage","status":"completed","text":"durable B"}`), 2)
	durableA := childHistoryItemEvent(t, "A-completed", json.RawMessage(`{"itemId":"A","revision":1,"kind":"agentMessage","status":"completed","text":"authoritative A"}`), 3)
	p := newChildProjection()
	state.projection = p
	p.frames = []childProjectionFrame{{raw: held, serial: 1}, {raw: durableB, serial: 2}}
	p.bufferedBytes = len(held) + len(durableB)
	p.heads = []*childProjectionHead{{cursor: "A-delta", after: 1}}
	segment, _, err := nextChildProjectionSegment(p)
	require.NoError(t, err)
	require.NoError(t, a.applyChildProjectionSegment("child", state, segment))
	assert.Equal(t, "before-A", *state.acceptedViewCursor)
	require.Len(t, p.frames, 1)
	require.Len(t, p.heads, 1, "a later durable row cannot pass the unresolved earlier item")
	require.Len(t, child.Messages(), 1)
	assert.Equal(t, []byte(durableB), child.Messages()[0].Content)
	p.frames = append(p.frames, childProjectionFrame{raw: durableA, serial: 3})
	p.bufferedBytes += len(durableA)
	segment, _, err = nextChildProjectionSegment(p)
	require.NoError(t, err)
	require.NoError(t, a.applyChildProjectionSegment("child", state, segment))
	assert.Empty(t, p.frames)
	assert.Empty(t, p.heads, "the same-item completion covers its own ephemeral cursor")
	assert.Equal(t, "A-completed", *state.acceptedViewCursor)
	require.Len(t, child.Messages(), 2)
	assert.Equal(t, []byte(durableA), child.Messages()[1].Content)
	assert.Empty(t, root.Messages(), "authoritative text supersedes transient deltas without another conversation row")
}

func TestFailedChildProjectionStillObservesNativeTurnCompletion(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	state.childViewPrefix = childViewPrefixFailed
	state.turnID = "active"
	a.viewWork.CloseAndWait()
	raw := feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "child", "turnId": "active", "terminal": contracts.MuseTurnOutcomeCompleted})
	assert.False(t, a.ActiveChildTurnState(state.childKey).Active)
	assert.Equal(t, []bool{false}, child.TurnActiveCalls)
	assert.Empty(t, child.Messages(), "lifecycle cleanup must not invent a transcript completion")
	assert.Equal(t, turnCompletionObserved, state.completed["active"], "a successful proof retry still owns normal transcript finalization")
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, raw, root.Messages()[0].Content)
}

func TestChildProjectionKeepsTheOldestUnresolvedDeadline(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, root := testAgentWithClock(t, clock)
	state, child := nativeHistoryChild(t, a, root)
	state.childViewPrefix = childViewPrefixReady
	a.handshake.GrantedCapabilities = []string{"rawLog"}
	seedChildNativeSources(t, a, "child", state, 1)
	created, stopped := testutil.NewTimerTraps(t, clock, childProjectionTimerTag)
	ctx := testutil.DeadlineContext(t)
	raw := feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "viewCursor": "A-delta", "itemId": "A", "delta": "unproved A"})
	firstDelay := testutil.WaitForTimer(t, ctx, created)
	assert.Equal(t, a.APITimeout(), firstDelay)
	clock.Advance(5 * time.Second).MustWait(ctx)
	durable := childHistoryItemEvent(t, "B", json.RawMessage(`{"itemId":"B","revision":1,"kind":"agentMessage","status":"completed","text":"proved B"}`), 1)
	a.HandleOutput(durable)
	stopped.MustWait(ctx).MustRelease(ctx)
	secondDelay := testutil.WaitForTimer(t, ctx, created)
	assert.Equal(t, firstDelay-5*time.Second, secondDelay, "unrelated B must not extend A's original deadline")
	testutil.AdvanceAndAwaitStop(t, ctx, clock, secondDelay, stopped)
	a.viewWork.CloseAndWait()
	require.Len(t, child.Messages(), 1)
	assert.Equal(t, []byte(durable), child.Messages()[0].Content)
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, raw, root.Messages()[0].Content)
	require.Len(t, root.LeapMuxNotifications(), 1)
	require.Error(t, state.projection.failed)
	assert.Contains(t, state.projection.failed.Error(), "deadline exceeded")
}

func TestFailedChildProjectionWithdrawsSettledControlsWithoutUnprovedRows(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	recorder := &museControlPublicationSink{Sink: child}
	state.sink = agent.NewProviderServices(recorder)
	request := approvalRequest("child", 0)
	key := testApprovalKey("child", "approval", 0)
	a.dispatcher.mu.Lock()
	a.publishControl(providerkit.ParseLine(request))
	a.dispatcher.mu.Unlock()
	a.drainControls()
	require.Contains(t, recorder.pending, key)
	state.childViewPrefix = childViewPrefixFailed
	settlement := rewriteNativeInputJSON(t, museApprovalSettlement(t, 0), json.RawMessage(`"child"`), "params", "sessionId")
	a.HandleOutput(settlement)
	assert.Equal(t, []string{key}, recorder.canceled)
	assert.Empty(t, recorder.pending)
	assert.Empty(t, child.PersistedNotifications(), "the failed view cannot persist an unproved settlement row")
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, []byte(settlement), root.Messages()[0].Content)
	// A publication already queued when settlement arrived must not reopen it.
	a.dispatcher.mu.Lock()
	a.publishControl(providerkit.ParseLine(request))
	a.dispatcher.mu.Unlock()
	a.drainControls()
	assert.Len(t, recorder.requests, 1)
	assert.Empty(t, recorder.pending)
	// A later successful proof still owns the genuine settlement notification.
	state.childViewPrefix = childViewPrefixReady
	a.dispatcher.mu.Lock()
	a.applySessionOutput(providerkit.ParseLine(settlement), state)
	a.dispatcher.mu.Unlock()
	a.drainControls()
	require.Len(t, child.PersistedNotifications(), 1)
	assert.Equal(t, []byte(settlement), child.PersistedNotifications()[0].Content)
}

func TestProvedInProgressChildTextSurvivesAnInterruptedTurn(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.childViewPrefix = childViewPrefixReady
	a.dispatcher.mu.Lock()
	a.startTurnLocked("child", "turn")
	a.dispatcher.mu.Unlock()
	held := []byte(`{"method":"item/delta","params":{"sessionId":"child","itemId":"A","field":"text","delta":"Proved A"}}`)
	original := childHistoryItemEvent(t, "A-source", json.RawMessage(`{"itemId":"A","turnId":"turn","revision":2,"kind":"agentMessage","status":"inProgress","text":"Proved A","futureNativeField":9007199254740993}`), 1)
	p := newChildProjection()
	state.projection = p
	p.frames = []childProjectionFrame{{raw: held, serial: 1}, {raw: original, serial: 2}}
	p.bufferedBytes = len(held) + len(original)
	segment, _, err := nextChildProjectionSegment(p)
	require.NoError(t, err)
	require.NoError(t, a.applyChildProjectionSegment("child", state, segment))
	require.Empty(t, child.Messages())
	a.dispatcher.mu.Lock()
	completed := []byte(`{"method":"turn/completed","params":{"sessionId":"child","turnId":"turn","terminal":"cancelled"}}`)
	a.applySessionOutput(providerkit.ParseLine(completed), state)
	a.dispatcher.mu.Unlock()
	rows := child.Messages()
	require.Len(t, rows, 2, "the proved native text must survive before the interrupted divider")
	assert.Equal(t, []byte(original), rows[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.Equal(t, completed, rows[1].Content)
}

func TestOneChildProjectionRetrySurvivesThePreviousProofOperation(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	state.childViewPrefix = childViewPrefixReady
	state.log = newNativeLog("child")
	state.log.subscriptionID = 1
	a.handshake.GrantedCapabilities = []string{"rawLog"}
	record := json.RawMessage(`{"schema_version":1,"id":"source-1","sequence":1,"stream":{"kind":"session","id":"child"},"record_type":"event","durability":"durable","payload_type":"future.native","payload_schema_version":1,"payload":{"kind":"future_native_event"}}`)
	item := json.RawMessage(`{"itemId":"answer","revision":1,"kind":"agentMessage","status":"completed","text":"The recovered answer"}`)
	frame := childHistoryItemEvent(t, "head", item, 1)
	entered, release, retried := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodLogPage:
			close(entered)
			<-release
			result, err := json.Marshal(map[string]any{"records": []json.RawMessage{record}, "nextCursor": map[string]any{"stream": nativeStream{Kind: "session", ID: "child"}, "after": nativePosition{ID: "source-1", Sequence: 1}}})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: result}
		case methodSessionRead:
			result, err := json.Marshal(map[string]any{"session": map[string]string{"sessionId": "child"}, "viewCursor": "head", "history": map[string]any{"mode": "inline", "items": []json.RawMessage{item}}})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: result}
		case methodViewPage:
			result, err := json.Marshal(map[string]any{"events": []json.RawMessage{frame}, "nextCursor": "head"})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: result}
		case methodViewSubscribe:
			close(retried)
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"head"}`)}
		default:
			t.Errorf("unexpected retry method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
		}
	}})
	a.HandleOutput(frame)
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the old proof request did not start")
	}
	a.failChildProjection("child", state, errors.New("the received view exceeded its limit"))
	a.requestChildView("child")
	a.stateMu.Lock()
	stillFailed := state.projection.failed != nil
	a.stateMu.Unlock()
	assert.True(t, stillFailed, "only the serialized next attempt may reset its predecessor's failure")
	close(release)
	select {
	case <-retried:
	case <-ctx.Done():
		t.Fatal("the requested retry did not start")
	}
	a.viewWork.CloseAndWait()
	a.stateMu.Lock()
	failure := state.projection.failed
	prefix := state.childViewPrefix
	a.stateMu.Unlock()
	require.NoError(t, failure)
	assert.Equal(t, childViewPrefixReady, prefix)
	require.Len(t, child.Messages(), 1)
	assert.Equal(t, []byte(frame), child.Messages()[0].Content)
}

func TestChildProjectionFailureCannotWarnAReplacementRoot(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.childViewPrefix = childViewPrefixReady
	raw := feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "A", "delta": "unproved old text"})
	a.dispatcher.mu.Lock()
	a.failChildProjectionLocked("child", state, errors.New("the old view cannot be proved"), nil)
	a.dispatcher.mu.Unlock()
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement"}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	_, err := a.ClearContext()
	require.NoError(t, err)
	a.drainDeferredOutputActions()
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, raw, root.Messages()[0].Content)
	assert.Equal(t, "child", root.Messages()[0].AgentSessionID)
	assert.Empty(t, root.LeapMuxNotifications(), "the retired child's warning must not acquire replacement authority")
}

func TestChildBootstrapOwnsLiveFramesBeforeItsActorStarts(t *testing.T) {
	a, root := testAgent(t)
	a.viewWork.CloseAndWait()
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "spawn", Kind: museItemSubagent, Revision: 1, Status: contracts.MuseItemStatusInProgress, SubagentID: "sub", ChildSessionID: "child", Objective: "Original assignment"}})
	state := a.sessions["child"]
	require.NotNil(t, state)
	seedChildNativeSources(t, a, "child", state, 2)
	prefix := childHistoryItemEvent(t, "prefix", json.RawMessage(`{"itemId":"first","revision":1,"kind":"agentMessage","status":"completed","text":"Stored first"}`), 1)
	live := childHistoryItemEvent(t, "live", json.RawMessage(`{"itemId":"second","revision":1,"kind":"agentMessage","status":"completed","text":"Received second"}`), 2)
	a.HandleOutput(live)
	a.stateMu.Lock()
	recovery := state.viewRecovery
	buffered := recovery != nil && len(recovery.buffer) == 1
	a.stateMu.Unlock()
	assert.True(t, buffered, "publishing child ownership must already install its initial recovery hold")
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child"},"viewCursor":"live","history":{"mode":"none","items":null,"snapshot":null}}`)}
		case methodViewPage:
			raw, err := json.Marshal(map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": "prefix"})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: raw}
		case methodViewSubscribe:
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"live"}`)}
		default:
			t.Errorf("unexpected bootstrap method %s", method)
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	}})
	a.syncView("child")
	rows := root.Child(state.childID).Messages()
	require.Len(t, rows, 3)
	assert.Equal(t, []byte(prefix), rows[1].Content)
	assert.Equal(t, []byte(live), rows[2].Content)
}

func TestChildRetirementRetainsItsInitialRecoveryBuffer(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.viewRecovery = &viewRecovery{}
	raw := feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "A", "delta": "received during bootstrap"})
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	retired := a.retireSessionTreeLocked("session")
	a.stateMu.Unlock()
	a.retireSessionStates(retired, "session", agent.MessageCompletionInterrupted)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	require.Len(t, root.Messages(), 1)
	assert.Equal(t, raw, root.Messages()[0].Content)
	assert.Empty(t, child.Messages())
	assert.Nil(t, state.viewRecovery)
}

// Match each held delta to the complete native item that supersedes it.
func BenchmarkSupersededChildFrames(b *testing.B) {
	for _, count := range []int{100, 1000, 3000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			delta := []byte(`{"method":"item/delta","params":{"sessionId":"child","itemId":"A","delta":"` + strings.Repeat("x", 64) + `"}}`)
			completed, err := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"sessionId": "child", "sourceRange": nativeSourceRange{Stream: nativeStream{Kind: "session", ID: "child"}, First: nativePosition{ID: "source", Sequence: 1}, Last: nativePosition{ID: "source", Sequence: 1}}, "item": map[string]any{"itemId": "A", "revision": 2, "kind": "agentMessage", "status": "completed", "text": strings.Repeat("x", count*64)}}})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				durable := readChildProjectionMetadata(completed)
				for range count {
					if !supersededChildEphemeral(readChildProjectionMetadata(delta), durable) {
						b.Fatal("the same-item completion did not supersede its delta")
					}
				}
			}
		})
	}
}

func TestChildProjectionWaitsForEvidenceInsteadOfUnanchoredDeltas(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	state.childViewPrefix = childViewPrefixReady
	a.viewWork.CloseAndWait()
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "A", "delta": "first", "viewCursor": "first"})
	a.stateMu.Lock()
	changed := state.projection.changed
	a.stateMu.Unlock()
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "A", "delta": "second", "viewCursor": "second"})
	select {
	case <-changed:
		t.Error("another unanchored delta cannot resolve a native proof wait")
	default:
	}
	require.Len(t, state.projection.frames, 2, "both received deltas remain retained")
	a.HandleOutput(childHistoryItemEvent(t, "completed", json.RawMessage(`{"itemId":"A","kind":"agentMessage","revision":2,"status":"completed","text":"firstsecond"}`), 1))
	select {
	case <-changed:
	default:
		t.Error("the genuine native completion must release the evidence wait")
	}
	assert.Empty(t, root.Messages())
}

func TestChildProjectionDoesNotDiscardANewerStartedRevision(t *testing.T) {
	before := readChildProjectionMetadata([]byte(`{"method":"item/started","params":{"sessionId":"child","item":{"itemId":"A","revision":3,"kind":"agentMessage"}}}`))
	after := readChildProjectionMetadata([]byte(`{"method":"item/completed","params":{"sessionId":"child","item":{"itemId":"A","revision":2,"kind":"agentMessage"}}}`))
	assert.False(t, supersededChildEphemeral(before, after), "an older full item cannot replace a newer native revision")
	after.revision = 3
	assert.True(t, supersededChildEphemeral(before, after))
	after.sessionID = "another-child"
	assert.False(t, supersededChildEphemeral(before, after))
}

func TestChildProjectionKeepsDeltasOfANewerStartedRevision(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	state.childViewPrefix = childViewPrefixReady
	a.viewWork.CloseAndWait()
	feed(t, a, contracts.MuseMethodItemStarted, map[string]any{"sessionId": "child", "item": map[string]any{"itemId": "A", "revision": 3, "kind": "agentMessage", "status": "inProgress"}})
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "A", "delta": "newer text"})
	old := childHistoryItemEvent(t, "old", json.RawMessage(`{"itemId":"A","revision":2,"kind":"agentMessage","status":"completed","text":"old text"}`), 1)
	a.HandleOutput(old)
	segment, _, err := nextChildProjectionSegment(state.projection)
	require.NoError(t, err)
	require.Len(t, segment, 1)
	require.NoError(t, a.applyChildProjectionSegment("child", state, segment))
	require.Len(t, state.projection.frames, 2, "an older native completion cannot consume a newer item's deltas")
	assert.Empty(t, root.Messages())
}

func TestChildProjectionRejectsMalformedKnownItemFrames(t *testing.T) {
	for _, item := range []string{
		"{\"itemId\":\"A\",\"revision\":\"invalid\",\"kind\":\"agentMessage\"}",
		"{\"itemId\":\"A\",\"revision\":0,\"kind\":\"agentMessage\"}",
		"{\"itemId\":\"A\",\"revision\":-1,\"kind\":\"agentMessage\"}",
		"{\"itemId\":\"A\",\"revision\":1,\"kind\":false}",
		"{\"itemId\":\"A\",\"revision\":1,\"kind\":\"agentMessage\",\"status\":false}",
		"{\"itemId\":\"A\",\"revision\":1}",
		"null",
	} {
		t.Run(item, func(t *testing.T) {
			raw := childHistoryItemEvent(t, "invalid", json.RawMessage(item), 1)
			metadata := readChildProjectionMetadata(raw)
			assert.Error(t, metadata.err, "a malformed known frame cannot be consumed without preserving its diagnostic bytes")
		})
	}
}

func TestChildProjectionDoesNotImposeAnItemShapeOnFutureMethods(t *testing.T) {
	raw := []byte("{\"method\":\"future/native\",\"params\":{\"sessionId\":\"child\",\"viewCursor\":\"future\",\"item\":\"a future field shape\",\"sourceRange\":{\"stream\":{\"kind\":\"session\",\"id\":\"child\"},\"first\":{\"id\":\"source-1\",\"sequence\":1},\"last\":{\"id\":\"source-1\",\"sequence\":1}}}}")
	metadata := readChildProjectionMetadata(raw)
	require.NoError(t, metadata.err)
	require.NotNil(t, metadata.anchor)
	assert.Equal(t, "child", metadata.sessionID)
	require.NotNil(t, metadata.cursor)
	assert.Equal(t, "future", *metadata.cursor)
}

func TestProvedChildControlWaitsForDispatcherRelease(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	recorder := &museControlPublicationSink{Sink: child}
	queue := child.OutputQueue()
	state.sink = agent.NewOrderedProviderServices(queue, agent.NewProviderServices(recorder))
	receipt := agent.NewTranscriptWriteReceipt()
	input := agent.MessageContent{Original: []byte(`{"content":"Input before the child control"}`), AgentSessionID: "child", WriteReceipt: receipt}
	child.PersistErr = assert.AnError
	err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, input, agent.SpanInfo{})
	require.ErrorIs(t, err, assert.AnError)
	require.True(t, agent.OutputRetained(err))

	a.dispatcher.mu.Lock()
	err = a.queueProvedChildControlLocked("child", state, approvalRequest("child", 0))
	a.dispatcher.mu.Unlock()
	require.NoError(t, err)
	a.drainDeferredOutputActions()
	require.True(t, queue.HasPending())
	require.Empty(t, recorder.requests, "the refused child input must precede control publication")
	_, stored := receipt.StoredMessageSequence()
	require.False(t, stored)
	child.PersistErr = nil
	inputStoredWhenPublished := false
	recorder.onPublish = func(agent.ControlRequest) {
		_, inputStoredWhenPublished = receipt.StoredMessageSequence()
	}

	a.dispatcher.mu.Lock()
	locked := true
	defer func() {
		if locked {
			a.dispatcher.mu.Unlock()
		}
	}()
	flushed := make(chan error, 1)
	go func() { flushed <- queue.Flush() }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case err = <-flushed:
		assert.ErrorIs(t, err, errMuseOutputLocked)
		assert.Empty(t, recorder.requests, "the held dispatcher must defer the control callback")
	case <-ctx.Done():
		a.dispatcher.mu.Unlock()
		locked = false
		// Release the mutex before failing so the old callback can finish.
		cleanupCtx, cleanupCancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cleanupCancel()
		select {
		case <-flushed:
		case <-cleanupCtx.Done():
			t.Fatal("the child control callback did not finish after dispatcher release")
		}
		t.Fatal("the child queue flush waited for the dispatcher held by its caller")
	}
	a.dispatcher.mu.Unlock()
	locked = false
	require.NoError(t, queue.Flush())
	require.Len(t, recorder.requests, 1)
	assert.True(t, inputStoredWhenPublished, "the input must commit before its dependent control becomes visible")
	assert.Equal(t, testApprovalKey("child", "approval", 0), recorder.requests[0].RequestID)
	assert.False(t, queue.HasPending())
}
