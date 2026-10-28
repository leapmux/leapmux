package muse

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childLifecycleFrame(t *testing.T, method, cursor, turn string) json.RawMessage {
	t.Helper()
	return childLifecycleFrameAtSource(t, method, cursor, turn, 1)
}

func childLifecycleFrameAtSource(t *testing.T, method, cursor, turn string, sequence int64) json.RawMessage {
	t.Helper()
	position := nativePosition{ID: fmt.Sprintf("source-%d", sequence), Sequence: sequence}
	params := map[string]any{"sessionId": "child", "viewCursor": cursor, "sourceRange": nativeSourceRange{Stream: nativeStream{Kind: "session", ID: "child"}, First: position, Last: position}}
	if method == methodTurnStarted {
		params["commandId"] = "command-" + turn
	}
	if method == methodSessionNameChanged {
		params["name"] = "Child name"
	}
	if turn != "" {
		params["turnId"] = turn
	}
	if method == contracts.MuseMethodTurnCompleted {
		params["terminal"] = "completed"
	}
	raw, err := json.Marshal(map[string]any{"method": method, "params": params})
	require.NoError(t, err)
	return raw
}

func TestChildHeadActivitySurvivesHistoricalLifecycle(t *testing.T) {
	for _, started := range []bool{false, true} {
		for _, active := range []bool{false, true} {
			t.Run(fmt.Sprintf("started=%v/active=%v", started, active), func(t *testing.T) {
				a, root := testAgent(t)
				state, child := nativeHistoryChild(t, a, root)
				a.viewWork.CloseAndWait()
				state.viewRecovery = &viewRecovery{}
				state.childViewRequested = true
				prefix := childHistoryItemEvent(t, "prefix", json.RawMessage("{\"itemId\":\"history\",\"revision\":1,\"kind\":\"agentMessage\",\"status\":\"completed\",\"text\":\"Old stored text\"}"), 1)
				var reads int
				var methods []string
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
					methods = append(methods, method)
					var result any
					switch method {
					case methodSessionRead:
						reads++
						var turn any
						status := "idle"
						if active {
							turn = "new-turn"
							status = "running"
						}
						result = map[string]any{"session": map[string]any{"sessionId": "child", "status": status, "activeTurnId": turn}, "viewCursor": "a-coherent-cut", "history": map[string]any{"mode": "none", "items": nil}}
					case methodViewPage:
						result = map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": "prefix"}
					case methodViewSubscribe:
						if started {
							a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "z-old-start", "old-turn"))
						}
						a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "z-old-completed", "old-turn"))
						a.HandleOutput(childLifecycleFrame(t, methodSessionNameChanged, "a-coherent-cut", ""))
						// Reject transcript proof after receiving the historical overlap.
						a.handshake.GrantedCapabilities = nil
						result = map[string]any{"viewCursor": "a-coherent-cut"}
					default:
						t.Errorf("unexpected child bootstrap method %s", method)
						return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"unexpected bootstrap request\"}")}
					}
					raw, err := json.Marshal(result)
					require.NoError(t, err)
					return agenttest.RPCReply{Result: raw}
				}})
				a.syncView("child")
				require.Equal(t, 1, reads)
				require.NotNil(t, state.projection)
				require.Error(t, state.projection.failed)
				assert.Equal(t, active, a.ActiveChildTurnState(state.childKey).Active)
				for _, published := range child.TurnActiveCalls {
					assert.Equal(t, active, published, "historical lifecycle cannot contradict the coherent head")
				}
				require.ErrorIs(t, a.SendChildInput(state.childKey, "cannot bypass failed proof", nil), agent.ErrChildRouteNotReady)
				assert.NotContains(t, methods, methodChildFollowup)
				assert.NotContains(t, methods, methodChildSend)
			})
		}
	}
}

func TestChildNativeHeadAlreadyOccursInFirstPage(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.viewRecovery = &viewRecovery{}
	state.childViewRequested = true
	cut := childLifecycleFrame(t, methodSessionNameChanged, "z-coherent-cut", "")
	newer := childLifecycleFrame(t, methodTurnStarted, "a-after-cut", "newer-turn")
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			return agenttest.RPCReply{Result: json.RawMessage("{\"session\":{\"sessionId\":\"child\",\"status\":\"running\",\"activeTurnId\":\"head-turn\"},\"viewCursor\":\"z-coherent-cut\",\"history\":{\"mode\":\"none\",\"items\":null}}")}
		case methodViewPage:
			result, err := json.Marshal(map[string]any{"events": []json.RawMessage{cut, newer}, "nextCursor": "a-after-cut"})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: result}
		case methodViewSubscribe:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.Equal(t, "a-after-cut", params["after"])
			// Fail the transcript independently of native runtime activity.
			a.handshake.GrantedCapabilities = nil
			a.HandleOutput(childHistoryItemEvent(t, "unproved", json.RawMessage("{\"itemId\":\"answer\",\"revision\":1,\"kind\":\"agentMessage\",\"status\":\"completed\",\"text\":\"Unproved\"}"), 3))
			return agenttest.RPCReply{Result: json.RawMessage("{\"viewCursor\":\"unproved\"}")}
		default:
			t.Errorf("unexpected child bootstrap method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"unexpected bootstrap request\"}")}
		}
	}})
	a.syncView("child")
	require.Error(t, state.projection.failed)
	assert.True(t, a.ActiveChildTurnState(state.childKey).Active)
	assert.Equal(t, "newer-turn", state.nativeTurn.turnID)
	assert.NotContains(t, child.TurnActiveCalls, false)
	a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "0-after-subscribe", "newer-turn"))
	assert.False(t, a.ActiveChildTurnState(state.childKey).Active)
}

func TestReadyChildRefreshUsesACoherentNativeHead(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	accepted := "accepted-prefix"
	state.acceptedViewCursor = &accepted
	state.childViewPrefix = childViewPrefixReady
	state.viewRecovery = &viewRecovery{}
	state.childViewRequested = true
	state.nativeTurn = childNativeTurnState{known: true, turnID: "new-turn"}
	reads := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		switch method {
		case methodSessionRead:
			reads++
			return agenttest.RPCReply{Result: json.RawMessage("{\"session\":{\"sessionId\":\"child\",\"status\":\"running\",\"activeTurnId\":\"new-turn\"},\"viewCursor\":\"new-cut\",\"history\":{\"mode\":\"none\",\"items\":null}}")}
		case methodViewSubscribe:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.Equal(t, accepted, params["after"])
			a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "old-start", "old-turn"))
			a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "old-completed", "old-turn"))
			a.HandleOutput(childLifecycleFrame(t, methodSessionNameChanged, "new-cut", ""))
			a.handshake.GrantedCapabilities = nil
			a.HandleOutput(childHistoryItemEvent(t, "unproved", json.RawMessage("{\"itemId\":\"answer\",\"revision\":1,\"kind\":\"agentMessage\",\"status\":\"completed\",\"text\":\"Unproved\"}"), 3))
			return agenttest.RPCReply{Result: json.RawMessage("{\"viewCursor\":\"unproved\"}")}
		default:
			t.Errorf("unexpected ready child method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"unexpected refresh request\"}")}
		}
	}})
	a.syncView("child")
	assert.Equal(t, 1, reads)
	require.Error(t, state.projection.failed)
	assert.True(t, a.ActiveChildTurnState(state.childKey).Active)
	assert.Equal(t, "new-turn", state.nativeTurn.turnID)
	a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "0-new-live-start", "genuine-turn"))
	assert.Equal(t, "genuine-turn", state.nativeTurn.turnID)
	a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "1-new-live-completed", "genuine-turn"))
	assert.False(t, a.ActiveChildTurnState(state.childKey).Active)
}

func TestChildNativeHeadDistinguishesAbsentAndEmptyCursors(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	recovery := &viewRecovery{}
	state.viewRecovery = recovery
	head := &childNativeHead{cursor: "", collecting: true, covered: make(map[[32]byte]struct{}), observed: make(map[[32]byte]struct{})}
	state.nativeHead = head
	a.dispatcher.mu.Lock()
	a.reconcileChildNativeReplayLocked("child", state, recovery, head, [][]byte{[]byte("{\"method\":\"future/native\",\"params\":{\"sessionId\":\"child\"}}")})
	a.dispatcher.mu.Unlock()
	assert.False(t, head.passed, "an absent cursor supplies no evidence of an explicit empty head")
	head.passed = false
	head.covered = make(map[[32]byte]struct{})
	a.dispatcher.mu.Lock()
	a.reconcileChildNativeReplayLocked("child", state, recovery, head, [][]byte{[]byte("{\"method\":\"future/native\",\"params\":{\"sessionId\":\"child\",\"viewCursor\":\"\"}}")})
	a.dispatcher.mu.Unlock()
	assert.True(t, head.passed)
}

func TestChildNativeHeadKeepsDifferentLifecycleFramesAtTheSameCursor(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	state.nativeHead = &childNativeHead{cursor: "head", passed: true, collecting: true, covered: make(map[[32]byte]struct{}), observed: make(map[[32]byte]struct{})}
	a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "shared-later-cursor", "later-turn"))
	require.True(t, a.ActiveChildTurnState(state.childKey).Active)
	a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "shared-later-cursor", "later-turn"))
	assert.False(t, a.ActiveChildTurnState(state.childKey).Active, "a cursor alone does not identify a native lifecycle event")
	a.viewWork.CloseAndWait()
}

func TestChildNativeHeadRetainsOnlyLifecycleReplayIdentities(t *testing.T) {
	a, root := testAgent(t)
	state, _ := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	recovery := &viewRecovery{}
	state.viewRecovery = recovery
	started := childLifecycleFrame(t, methodTurnStarted, "old-start", "old-turn")
	completed := childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "old-completed", "old-turn")
	frames := [][]byte{started}
	for index := range 128 {
		item := map[string]any{
			"itemId": fmt.Sprintf("history-%d", index), "revision": 1,
			"kind": contracts.MuseItemKindAgentMessage, "status": contracts.MuseItemStatusCompleted,
			"text": "Historical child text", "turnId": "old-turn",
		}
		if index%2 == 0 {
			item["kind"] = contracts.MuseItemKindToolCall
			item["tool"], item["args"] = "shell", "{}"
			item["callId"] = fmt.Sprintf("call-%d", index)
			item["visibleOutput"] = "Historical tool output"
			delete(item, "text")
		}
		raw, err := json.Marshal(item)
		require.NoError(t, err)
		frames = append(frames, childHistoryItemEvent(t, fmt.Sprintf("history-%d", index), raw, int64(index+2)))
	}
	cut := childLifecycleFrame(t, methodSessionNameChanged, "coherent-head", "")
	frames = append(frames, completed, cut)
	var head *childNativeHead
	var seedErr error
	func() {
		a.dispatcher.mu.Lock()
		defer a.dispatcher.mu.Unlock()
		head, seedErr = a.seedChildNativeHeadLocked("child", state, recovery, "coherent-head", json.RawMessage(`"current-turn"`))
		if seedErr != nil {
			return
		}
		a.reconcileChildNativeReplayLocked("child", state, recovery, head, frames)
		a.observeChildNativeTurn(providerkit.ParseLine(started), state)
		a.observeChildNativeTurn(providerkit.ParseLine(completed), state)
	}()
	require.NoError(t, seedErr)
	require.NotNil(t, head)
	assert.Len(t, head.covered, 2, "only lifecycle replay checks consume retained fingerprints")
	for _, raw := range [][]byte{started, completed} {
		key, valid := viewFrameKey(raw)
		require.True(t, valid)
		assert.Contains(t, head.covered, key)
	}
	cutKey, valid := viewFrameKey(cut)
	require.True(t, valid)
	assert.NotContains(t, head.covered, cutKey)
	assert.True(t, head.passed, "a non-lifecycle frame still establishes the coherent cursor cut")
	assert.Equal(t, "current-turn", state.nativeTurn.turnID, "historical lifecycle cannot replace the snapshot's activity")
	assert.True(t, a.ActiveChildTurnState(state.childKey).Active)
}

func TestNotLoadedChildHeadDerivesActivityFromItsOrderedLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, status, activeHead, wantTurn string
		openTurn, refuseSubscription       bool
	}{
		{name: "not loaded with an open turn", status: "notLoaded", openTurn: true, wantTurn: "current-turn"},
		{name: "not loaded running child refuses subscription", status: "notLoaded", openTurn: true, refuseSubscription: true, wantTurn: "current-turn"},
		{name: "missing status with an open turn", openTurn: true, wantTurn: "current-turn"},
		{name: "unknown status with an open turn", status: "futureLoadState", openTurn: true, wantTurn: "current-turn"},
		{name: "running null with an open turn", status: "running", openTurn: true, wantTurn: "current-turn"},
		{name: "not loaded with a completed prefix", status: "notLoaded"},
		{name: "not loaded completed child refuses subscription", status: "notLoaded", refuseSubscription: true},
		{name: "loaded idle remains authoritative", status: "idle", openTurn: true},
		{name: "nonnull active head remains authoritative", status: "running", activeHead: "head-turn", wantTurn: "head-turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			state.childViewRequested = true
			oldStarted := childLifecycleFrame(t, methodTurnStarted, "old-start", "old-turn")
			oldCompleted := childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "old-completed", "old-turn")
			currentStarted := childLifecycleFrame(t, methodTurnStarted, "current-start", "current-turn")
			prefix := []json.RawMessage{oldStarted, oldCompleted}
			if tc.openTurn {
				prefix = append(prefix, currentStarted)
			}
			prefix = append(prefix, childLifecycleFrame(t, methodSessionNameChanged, "coherent-cut", ""))
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				var result any
				switch method {
				case methodSessionRead:
					var active any
					if tc.activeHead != "" {
						active = tc.activeHead
					}
					session := map[string]any{"sessionId": "child", "kind": "subagent", "activeTurnId": active}
					if tc.status != "" {
						session["status"] = tc.status
					}
					result = map[string]any{"session": session, "viewCursor": "coherent-cut", "history": map[string]any{"mode": "none", "items": nil}}
				case methodViewPage:
					result = map[string]any{"events": prefix, "nextCursor": "coherent-cut"}
				case methodViewSubscribe:
					// Activity must survive independently when transcript proof fails.
					a.handshake.GrantedCapabilities = nil
					if tc.refuseSubscription {
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"the native child is not loaded"}`)}
					}
					result = map[string]any{"viewCursor": "coherent-cut"}
				default:
					t.Errorf("unexpected child activity method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected activity request"}`)}
				}
				raw, err := json.Marshal(result)
				require.NoError(t, err)
				return agenttest.RPCReply{Result: raw}
			}})
			a.syncView("child")
			require.NotNil(t, state.projection)
			require.Error(t, state.projection.failed)
			if tc.refuseSubscription && tc.openTurn {
				assert.ErrorContains(t, state.projection.failed, "the native child is not loaded", "a proved active cut cannot authorize the completed-child fallback")
			}
			assert.True(t, state.nativeTurn.known, "the exact cut lifecycle supplies activity even when the session is not loaded")
			assert.Equal(t, tc.wantTurn, state.nativeTurn.turnID)
			wantActive := tc.wantTurn != ""
			assert.Equal(t, agent.TurnState{Active: wantActive, Steerable: wantActive}, a.ActiveChildTurnState(state.childKey))
			for _, published := range child.TurnActiveCalls {
				assert.Equal(t, wantActive, published, "covered historical activity must not contradict the accepted head")
			}
			for _, frame := range prefix[:len(prefix)-1] {
				a.HandleOutput(frame)
			}
			assert.Equal(t, tc.wantTurn, state.nativeTurn.turnID, "covered starts and completions cannot replace the accepted head activity")
			assert.Empty(t, child.Messages(), "activity cannot authorize unproved transcript rows")
		})
	}
}

func TestNotLoadedChildHeadPreservesLiveActivityObservedBeforeReplayReconciliation(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%v", complete), func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			state.childViewRequested = true
			oldStarted := childLifecycleFrame(t, methodTurnStarted, "old-start", "old-turn")
			oldCompleted := childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "old-completed", "old-turn")
			currentStarted := childLifecycleFrame(t, methodTurnStarted, "current-start", "current-turn")
			prefix := []json.RawMessage{oldStarted, oldCompleted, currentStarted}
			wantTurn := "later-turn"
			if complete {
				wantTurn = ""
			}
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				var result any
				switch method {
				case methodSessionRead:
					result = map[string]any{"session": map[string]any{"sessionId": "child", "kind": "subagent", "status": "notLoaded", "activeTurnId": nil}, "viewCursor": "coherent-cut", "history": map[string]any{"mode": "none", "items": nil}}
				case methodViewPage:
					result = map[string]any{"events": prefix, "nextCursor": "current-start"}
				case methodViewSubscribe:
					a.HandleOutput(childLifecycleFrame(t, methodSessionNameChanged, "coherent-cut", ""))
					if complete {
						a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "after-cut-completed", "current-turn"))
					} else {
						a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "after-cut-started", "later-turn"))
					}
					a.stateMu.Lock()
					assert.Empty(t, state.nativeHead.covered, "the coherent cut has not reached replay reconciliation yet")
					assert.NotEmpty(t, state.nativeHead.observed, "the live lifecycle receipt precedes replay reconciliation")
					a.stateMu.Unlock()
					a.handshake.GrantedCapabilities = nil
					result = map[string]any{"viewCursor": "after-cut-head"}
				default:
					t.Errorf("unexpected live child activity method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected live activity request"}`)}
				}
				raw, err := json.Marshal(result)
				require.NoError(t, err)
				return agenttest.RPCReply{Result: raw}
			}})
			a.syncView("child")
			require.NotNil(t, state.projection)
			require.Error(t, state.projection.failed)
			assert.Equal(t, wantTurn, state.nativeTurn.turnID, "reconciling the older cut cannot replace a live observation")
			wantActive := wantTurn != ""
			assert.Equal(t, agent.TurnState{Active: wantActive, Steerable: wantActive}, a.ActiveChildTurnState(state.childKey))
			for _, published := range child.TurnActiveCalls {
				assert.Equal(t, wantActive, published)
			}
			for _, frame := range prefix {
				a.HandleOutput(frame)
			}
			assert.Equal(t, wantTurn, state.nativeTurn.turnID)
			a.HandleOutput(childLifecycleFrame(t, methodTurnStarted, "new-live-start", "next-turn"))
			assert.Equal(t, "next-turn", state.nativeTurn.turnID)
			a.HandleOutput(childLifecycleFrame(t, contracts.MuseMethodTurnCompleted, "new-live-completed", "next-turn"))
			assert.False(t, a.ActiveChildTurnState(state.childKey).Active)
			assert.Empty(t, child.Messages(), "live activity cannot authorize unproved transcript rows")
		})
	}
}

func TestChildHeadActivityAuthorityRequiresNativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, session, wantTurn string
		known, invalid          bool
	}{
		{name: "idle without an active member", session: `{"sessionId":"child","status":"idle"}`},
		{name: "null without a status", session: `{"sessionId":"child","activeTurnId":null}`},
		{name: "null with an empty status", session: `{"sessionId":"child","status":"","activeTurnId":null}`},
		{name: "null with an unknown status", session: `{"sessionId":"child","status":"futureLoadState","activeTurnId":null}`},
		{name: "null in a running session", session: `{"sessionId":"child","status":"running","activeTurnId":null}`},
		{name: "null in a not loaded session", session: `{"sessionId":"child","status":"notLoaded","activeTurnId":null}`},
		{name: "explicit loaded idle", session: `{"sessionId":"child","status":"idle","activeTurnId":null}`, known: true},
		{name: "positive activity without a status", session: `{"sessionId":"child","activeTurnId":"active-turn"}`, known: true, wantTurn: "active-turn"},
		{name: "positive activity with an unknown status", session: `{"sessionId":"child","status":"futureLoadState","activeTurnId":"active-turn"}`, known: true, wantTurn: "active-turn"},
		{name: "empty active turn", session: `{"sessionId":"child","status":"running","activeTurnId":""}`, invalid: true},
		{name: "invalid active turn", session: `{"sessionId":"child","status":"running","activeTurnId":7}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, root := testAgent(t)
			state, _ := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			recovery := &viewRecovery{}
			state.viewRecovery = recovery
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodSessionRead, method)
				result, err := json.Marshal(map[string]any{"session": json.RawMessage(tc.session), "viewCursor": "coherent-cut", "history": map[string]any{"mode": "inline", "items": []any{}}})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: result}
			}})
			start, err := a.readChildViewHead("child", state, recovery, a.Clock().Now().Add(a.childProofTimeout()))
			if tc.invalid {
				require.Error(t, err)
				assert.Nil(t, start.nativeHead)
				assert.False(t, state.nativeTurn.known)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, start.nativeHead)
			assert.Equal(t, tc.known, start.nativeHead.activityKnown)
			assert.Equal(t, tc.wantTurn, start.nativeHead.activeTurnID)
			assert.Equal(t, tc.known, state.nativeTurn.known)
			assert.Equal(t, tc.wantTurn, state.nativeTurn.turnID)
		})
	}
}

func TestReadyNotLoadedCompletedChildRefreshKeepsItsProvedLifecycle(t *testing.T) {
	for _, olderHead := range []bool{false, true} {
		t.Run(fmt.Sprintf("older metadata head=%v", olderHead), func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			state.childViewRequested = true
			const startedCursor = "opaque-start"
			const completedCursor = "opaque-completed"
			initialHead := completedCursor
			if olderHead {
				initialHead = startedCursor
			}
			prefix := []json.RawMessage{
				childLifecycleFrameAtSource(t, methodTurnStarted, startedCursor, "completed-turn", 1),
				childHistoryItemEvent(t, "opaque-answer", json.RawMessage(`{"itemId":"answer","revision":1,"kind":"agentMessage","status":"completed","turnId":"completed-turn","text":"The original child answer."}`), 2),
				childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, completedCursor, "completed-turn", 3),
			}
			refresh := false
			refreshPages, humanCommands := 0, 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				var result any
				switch method {
				case methodSessionRead:
					head := initialHead
					if refresh {
						head = completedCursor
					}
					result = map[string]any{"session": map[string]any{"sessionId": "child", "kind": "subagent", "status": "notLoaded", "activeTurnId": nil}, "viewCursor": head, "history": map[string]any{"mode": "none", "items": nil}}
				case methodViewPage:
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.NotContains(t, params, "cursor", "re-establishing an unknown cut requires genuine history before the accepted cursor")
					if refresh {
						refreshPages++
					}
					result = map[string]any{"events": prefix, "nextCursor": nil}
				case methodViewSubscribe:
					if refresh {
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"session child not found","data":{"kind":"sessionNotFound","retryable":false,"sessionId":"child"}}`)}
					}
					result = map[string]any{"viewCursor": completedCursor}
				case methodChildFollowup:
					humanCommands++
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.Equal(t, "session", params["sessionId"])
					assert.Equal(t, "sub", params["subagentId"])
					assert.Equal(t, "The later human note.", params["body"])
					assert.NotEmpty(t, params["commandId"])
					result = map[string]any{"commandId": params["commandId"], "status": "accepted"}
				default:
					t.Errorf("unexpected completed child refresh method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected completed child refresh request"}`)}
				}
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				return agenttest.RPCReply{Result: encoded}
			}})
			a.syncView("child")
			require.Equal(t, childViewPrefixReady, state.childViewPrefix)
			require.NotNil(t, state.acceptedViewCursor)
			require.Equal(t, completedCursor, *state.acceptedViewCursor)
			require.NotNil(t, state.nativeHead)
			require.Equal(t, initialHead, state.nativeHead.cursor)
			require.True(t, state.nativeHead.activityKnown)
			require.False(t, a.ActiveChildTurnState(state.childKey).Active)
			before := child.Messages()
			require.Len(t, before, 2)
			require.True(t, before[1].TurnEnd)
			assert.Contains(t, string(before[0].Content), "The original child answer.")
			require.Empty(t, root.LeapMuxNotifications())

			refresh = true
			a.requestChildView("child")
			a.syncView("child")
			assert.Equal(t, childViewPrefixReady, state.childViewPrefix, "a fresh unknown summary cannot discard a genuine completed cut")
			assert.Equal(t, before, child.Messages(), "the fake's stored sequence IDs and every accepted row must stay unchanged")
			ends := 0
			for _, message := range child.Messages() {
				if message.TurnEnd {
					ends++
				}
			}
			assert.Equal(t, 1, ends)
			assert.Empty(t, root.LeapMuxNotifications(), "refresh must not report a previously proved completed child as unavailable")
			if olderHead {
				assert.Positive(t, refreshPages, "an older metadata head cannot prove inactivity at the newer accepted completion cursor")
			}
			assert.NoError(t, a.SendChildInput(state.childKey, "The later human note.", nil))
			assert.Equal(t, 1, humanCommands, "the original child must receive exactly one native human command")
		})
	}
}

func TestEmptyChildItemHistoryReadsUnknownActivityFromLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, status, activeHead string
		completed, wantActive    bool
	}{
		{name: "not loaded with an open turn", status: "notLoaded", wantActive: true},
		{name: "not loaded with a completed turn", status: "notLoaded", completed: true},
		{name: "empty status with an open turn", wantActive: true},
		{name: "unknown status with an open turn", status: "futureLoadState", wantActive: true},
		{name: "loaded idle retains its native completion", status: "idle", completed: true},
		{name: "loaded active publishes its native start", status: "running", activeHead: "head-turn", wantActive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			state.childViewRequested = true
			const head = "opaque-child-head"
			startCursor := head
			if tc.completed {
				startCursor = "opaque-child-start"
			}
			turnID := "historical-turn"
			if tc.activeHead != "" {
				turnID = tc.activeHead
			}
			prefix := []json.RawMessage{childLifecycleFrameAtSource(t, methodTurnStarted, startCursor, turnID, 1)}
			if tc.completed {
				prefix = append(prefix, childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, head, turnID, 2))
			}
			pages := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				var result any
				switch method {
				case methodSessionRead:
					var active any
					if tc.activeHead != "" {
						active = tc.activeHead
					}
					result = map[string]any{
						"session":    map[string]any{"sessionId": "child", "kind": "subagent", "status": tc.status, "activeTurnId": active},
						"viewCursor": head, "history": map[string]any{"mode": "inline", "items": []any{}},
					}
				case methodViewPage:
					pages++
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.NotContains(t, params, "cursor", "unknown activity requires lifecycle before the folded item head")
					result = map[string]any{"events": prefix, "nextCursor": nil}
				case methodViewSubscribe:
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.Equal(t, head, params["after"])
					// The native child stays held after this cut. No later event
					// can repair a lifecycle prefix that bootstrap omitted.
					result = map[string]any{"viewCursor": head}
				default:
					t.Errorf("unexpected empty child history method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected empty child history request"}`)}
				}
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				return agenttest.RPCReply{Result: encoded}
			}})
			a.syncView("child")
			assert.Equal(t, 1, pages)
			assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
			require.NotNil(t, state.nativeHead)
			assert.True(t, state.nativeHead.activityKnown, "empty folded items do not establish an idle lifecycle")
			assert.Equal(t, agent.TurnState{Active: tc.wantActive, Steerable: tc.wantActive}, a.ActiveChildTurnState(state.childKey))
			published := child.TurnStates()
			require.NotEmpty(t, published, "the genuine lifecycle must publish the child's activity")
			for _, turn := range published {
				assert.Equal(t, agent.TurnState{Active: tc.wantActive, Steerable: tc.wantActive}, turn)
			}
			require.NotNil(t, state.acceptedViewCursor)
			assert.Equal(t, head, *state.acceptedViewCursor)
			assert.Empty(t, root.LeapMuxNotifications())
			if tc.completed {
				rows := child.Messages()
				require.Len(t, rows, 1, "the completed lifecycle retains only its genuine native turn end")
				assert.True(t, rows[0].TurnEnd)
				assert.Equal(t, []byte(prefix[len(prefix)-1]), rows[0].Content)
				assert.Empty(t, rows[0].SupplementalContent)
			} else {
				assert.Empty(t, child.Messages(), "activity proof cannot fabricate a chat item")
			}
		})
	}
}

func TestReadyChildHeadPublishesItsAuthoritativeActivityWithoutLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                                                      string
		active, confirmStart, superseded, earlyEnd, earlyNextTurn bool
	}{
		{name: "idle head", active: false},
		{name: "active head with confirming start", active: true, confirmStart: true},
		{name: "active head completes without a start", active: true},
		{name: "a newer turn supersedes the old head", active: true, superseded: true},
		{name: "completion arrives before readiness", active: true, earlyEnd: true},
		{name: "a newer live turn follows bootstrap completion", active: true, earlyEnd: true, earlyNextTurn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active := tc.active
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			state.childViewRequested = true
			const head = "opaque-metadata-head"
			const turn = "authoritative-current-turn"
			prefix := childLifecycleFrameAtSource(t, methodSessionNameChanged, head, "", 1)
			earlyEnd := childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, "opaque-early-end", turn, 2)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				var result any
				switch method {
				case methodSessionRead:
					var current any
					status := "idle"
					if active {
						current, status = turn, "running"
					}
					result = map[string]any{
						"session":    map[string]any{"sessionId": "child", "status": status, "activeTurnId": current},
						"viewCursor": head, "history": map[string]any{"mode": "none", "items": nil, "snapshot": nil},
					}
				case methodViewPage:
					result = map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": nil}
				case methodViewSubscribe:
					if tc.earlyEnd {
						a.HandleOutput(earlyEnd)
					}
					if tc.earlyNextTurn {
						a.HandleOutput(childLifecycleFrameAtSource(t, methodTurnStarted, "opaque-bootstrap-next-start", "newer-live-turn", 3))
					}
					result = map[string]any{"viewCursor": head}
				default:
					t.Errorf("unexpected child head publication method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected child head publication request"}`)}
				}
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				return agenttest.RPCReply{Result: encoded}
			}})
			a.syncView("child")
			require.Equal(t, childViewPrefixReady, state.childViewPrefix)
			if tc.earlyEnd {
				assert.Equal(t, agent.TurnState{Active: tc.earlyNextTurn, Steerable: tc.earlyNextTurn}, a.ActiveChildTurnState(state.childKey))
				wantLifecycle := []string{"turn_end", "turn_active:false"}
				if tc.earlyNextTurn {
					wantLifecycle = []string{"turn_end", "turn_active:true"}
				}
				assert.Equal(t, wantLifecycle, child.TurnLifecycle(), "bootstrap must preserve the genuine completion before publishing current native activity")
				rows := child.Messages()
				require.Len(t, rows, 1)
				assert.Equal(t, []byte(earlyEnd), rows[0].Content)
				assert.True(t, rows[0].TurnEnd)
				assert.Empty(t, root.LeapMuxNotifications())
				return
			}
			require.Equal(t, agent.TurnState{Active: active, Steerable: active}, a.ActiveChildTurnState(state.childKey))
			want := []agent.TurnState{{Active: active, Steerable: active}}
			require.Equal(t, want, child.TurnStates(), "the actual child sink must reconcile queue activity even without a current-turn start")
			assert.Empty(t, child.Messages(), "a metadata head cannot fabricate a native turn end")
			assert.Empty(t, root.LeapMuxNotifications())

			a.syncView("child")
			a.HandleOutput(childLifecycleFrameAtSource(t, methodSessionNameChanged, "opaque-later-metadata", "", 2))
			a.syncView("child")
			assert.Equal(t, want, child.TurnStates(), "draining unchanged activity must not republish it")
			assert.Empty(t, child.Messages())
			if active {
				if tc.superseded {
					a.HandleOutput(childLifecycleFrameAtSource(t, methodTurnStarted, "opaque-newer-start", "newer-turn", 2))
					a.syncView("child")
					newerEnd := childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, "opaque-newer-end", "newer-turn", 3)
					a.HandleOutput(newerEnd)
					a.syncView("child")
					before := child.Messages()
					require.Len(t, before, 1)
					assert.Equal(t, []byte(newerEnd), before[0].Content)
					activity := child.TurnLifecycle()
					a.HandleOutput(childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, "opaque-late-old-end", turn, 3))
					a.syncView("child")
					assert.Equal(t, before, child.Messages(), "a stale head cannot authorize another turn's late completion")
					assert.Equal(t, activity, child.TurnLifecycle())
					return
				}
				if tc.confirmStart {
					a.HandleOutput(childLifecycleFrameAtSource(t, methodTurnStarted, "opaque-current-start", turn, 2))
					a.syncView("child")
					assert.Equal(t, want, child.TurnStates(), "a genuine start confirming the published head must not duplicate its activity")
				}
				end := childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, "opaque-current-end", turn, 3)
				a.HandleOutput(end)
				a.syncView("child")
				assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, child.TurnLifecycle())
				rows := child.Messages()
				require.Len(t, rows, 1)
				assert.Equal(t, []byte(end), rows[0].Content)
				assert.True(t, rows[0].TurnEnd)
			}
		})
	}
}

func TestReadyChildHeadKeepsRetainedTurnEndBeforeIdle(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.viewRecovery = &viewRecovery{}
	state.childViewRequested = true
	const head = "opaque-running-head"
	const turn = "current-turn"
	prefix := childLifecycleFrameAtSource(t, methodSessionNameChanged, head, "", 1)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		var result any
		switch method {
		case methodSessionRead:
			result = map[string]any{
				"session":    map[string]any{"sessionId": "child", "status": "running", "activeTurnId": turn},
				"viewCursor": head, "history": map[string]any{"mode": "none", "items": nil, "snapshot": nil},
			}
		case methodViewPage:
			result = map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": nil}
		case methodViewSubscribe:
			result = map[string]any{"viewCursor": head}
		default:
			t.Errorf("unexpected retained child turn method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected retained child turn request"}`)}
		}
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		return agenttest.RPCReply{Result: encoded}
	}})
	a.syncView("child")
	require.Equal(t, []agent.TurnState{{Active: true, Steerable: true}}, child.TurnStates())
	a.HandleOutput(childLifecycleFrameAtSource(t, methodTurnStarted, "opaque-current-start", turn, 2))
	a.syncView("child")
	child.PersistErr = assert.AnError
	defer func() {
		child.PersistErr = nil
		_ = agent.FlushPendingOutput(state.sink)
	}()
	end := childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, "opaque-current-end", turn, 3)
	a.HandleOutput(end)
	a.syncView("child")
	require.True(t, child.OutputQueue().HasPending(), "the native end remains owned by the original child queue")
	attempts := child.Messages()
	require.NotEmpty(t, attempts, "the fake records the refused native write attempts")
	for _, attempt := range attempts {
		assert.Equal(t, []byte(end), attempt.Content)
		assert.True(t, attempt.TurnEnd, "the fake preserves the attempted operation type")
		assert.False(t, attempt.Stored(), "a refused write is not a stored native divider")
	}
	assert.Equal(t, []string{"turn_active:true"}, child.TurnLifecycle(), "the retained divider must precede idle publication")
	child.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(state.sink))
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, child.TurnLifecycle())
	rows := child.Messages()
	storedEnds := 0
	for _, row := range rows {
		assert.Equal(t, []byte(end), row.Content, "every retry preserves the original native completion")
		if row.Stored() && row.TurnEnd {
			storedEnds++
		}
	}
	assert.Equal(t, 1, storedEnds, "recovery stores the native divider exactly once")
	assert.False(t, child.OutputQueue().HasPending())
	assert.Empty(t, root.LeapMuxNotifications())
}

func TestReadyChildHeadKeepsAnUnsubmittedTurnEndBeforeIdle(t *testing.T) {
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.viewRecovery = &viewRecovery{}
	state.childViewRequested = true
	const head = "opaque-current-start"
	const turn = "current-turn"
	prefix := childLifecycleFrameAtSource(t, methodTurnStarted, head, turn, 1)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		var result any
		switch method {
		case methodSessionRead:
			result = map[string]any{
				"session":    map[string]any{"sessionId": "child", "status": "running", "activeTurnId": turn},
				"viewCursor": head, "history": map[string]any{"mode": "none", "items": nil, "snapshot": nil},
			}
		case methodViewPage:
			result = map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": nil}
		case methodViewSubscribe:
			result = map[string]any{"viewCursor": head}
		default:
			t.Errorf("unexpected unsubmitted child turn method %s", method)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected unsubmitted child turn request"}`)}
		}
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		return agenttest.RPCReply{Result: encoded}
	}})
	a.syncView("child")
	require.Equal(t, []agent.TurnState{{Active: true, Steerable: true}}, child.TurnStates())
	refusing := &finalizationSink{Sink: child, failEnd: 100}
	state.sink = agent.NewProviderServices(refusing)
	end := childLifecycleFrameAtSource(t, contracts.MuseMethodTurnCompleted, "opaque-current-end", turn, 2)
	a.HandleOutput(end)
	a.syncView("child")
	require.Len(t, state.finalizations, 1, "a hard refusal leaves the provider owning its exact native end")
	assert.False(t, child.OutputQueue().HasPending(), "this failure has not transferred publication ownership to the queue")
	assert.Empty(t, child.Messages())
	assert.Equal(t, []string{"turn_active:true"}, child.TurnLifecycle(), "idle cannot precede a hard-refused native divider")

	refusing.failEnd = 0
	a.HandleOutput(end)
	a.syncView("child")
	assert.Empty(t, state.finalizations)
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, child.TurnLifecycle())
	rows := child.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, []byte(end), rows[0].Content)
	assert.True(t, rows[0].TurnEnd)
	a.HandleOutput(end)
	a.syncView("child")
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, child.TurnLifecycle(), "native replay must not duplicate finalization or idle publication")
	assert.Empty(t, root.LeapMuxNotifications())
}

func TestUnservedChildHistoryAcceptsAProvedEmptyOrigin(t *testing.T) {
	for _, mode := range []string{"none", "futureHistoryMode"} {
		for _, head := range []string{"", "opaque-empty-head"} {
			t.Run(fmt.Sprintf("mode=%s/head=%q", mode, head), func(t *testing.T) {
				a, root := testAgent(t)
				state, child := nativeHistoryChild(t, a, root)
				a.viewWork.CloseAndWait()
				state.viewRecovery = &viewRecovery{}
				state.childViewRequested = true
				var methods []string
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
					methods = append(methods, method)
					var result any
					switch method {
					case methodSessionRead:
						result = map[string]any{
							"session":    map[string]any{"sessionId": "child", "status": "notLoaded", "activeTurnId": nil},
							"viewCursor": head,
							"history":    map[string]any{"mode": mode, "noneReason": "projectionUnavailable", "items": nil, "snapshot": nil},
						}
					case methodViewPage:
						var params map[string]any
						require.NoError(t, json.Unmarshal(raw, &params))
						assert.NotContains(t, params, "cursor")
						result = map[string]any{"events": []any{}, "nextCursor": nil}
					case methodViewSubscribe:
						var params map[string]any
						require.NoError(t, json.Unmarshal(raw, &params))
						assert.Equal(t, head, params["after"])
						a.HandleOutput(childLifecycleFrameAtSource(t, methodTurnStarted, "later-live-start", "current-turn", 1))
						result = map[string]any{"viewCursor": "later-live-start"}
					default:
						t.Errorf("unexpected unserved-history method %s", method)
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected unserved-history request"}`)}
					}
					encoded, err := json.Marshal(result)
					require.NoError(t, err)
					return agenttest.RPCReply{Result: encoded}
				}})
				a.syncView("child")
				assert.Equal(t, []string{methodSessionRead, methodViewPage, methodViewSubscribe}, methods)
				assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
				assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, a.ActiveChildTurnState(state.childKey))
				assert.Equal(t, []agent.TurnState{{Active: true, Steerable: true}}, child.TurnStates())
				assert.Empty(t, child.Messages())
				assert.Empty(t, root.LeapMuxNotifications())
			})
		}
	}
}

func TestEmptyChildViewRetainsItsObservedHeadWithoutInventingActivity(t *testing.T) {
	for _, tc := range []struct {
		name, head, wantError                 string
		live, nonemptyItems, nextPage, refuse bool
	}{
		{name: "an explicit empty head admits a genuine live start", live: true},
		{name: "an explicit empty head without live activity stays unknown"},
		{name: "an opaque head admits a genuine live start", head: "opaque-empty-session-start", live: true},
		{name: "an opaque head without live activity stays unknown", head: "opaque-empty-session-start"},
		{name: "an empty head subscription refusal keeps activity unknown", refuse: true, wantError: "session child not found"},
		{name: "nonempty items cannot use an empty page", head: "opaque-empty-session-start", nonemptyItems: true, wantError: "no accepted history cursor"},
		{name: "an empty page cannot promise another page", head: "opaque-empty-session-start", nextPage: true, wantError: "cursor did not advance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			state.childViewRequested = true
			const liveCursor = "opaque-live-start"
			pages, subscriptions := 0, 0
			var methods []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				methods = append(methods, method)
				var result any
				switch method {
				case methodSessionRead:
					items := []json.RawMessage{}
					if tc.nonemptyItems {
						items = append(items, json.RawMessage(`{"itemId":"unserved","revision":1,"kind":"agentMessage","status":"completed","text":"This item still requires native view proof."}`))
					}
					result = map[string]any{
						"session":    map[string]any{"sessionId": "child", "kind": "subagent", "status": "notLoaded", "activeTurnId": nil},
						"viewCursor": tc.head, "history": map[string]any{"mode": "inline", "items": items},
					}
				case methodViewPage:
					pages++
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.NotContains(t, params, "cursor", "only a successful origin page can establish an empty view")
					var next any
					if tc.nextPage {
						next = "invalid-empty-page-continuation"
					}
					result = map[string]any{"events": []any{}, "nextCursor": next}
				case methodViewSubscribe:
					subscriptions++
					var params map[string]any
					require.NoError(t, json.Unmarshal(raw, &params))
					assert.Equal(t, tc.head, params["after"], "an empty view retains its exact observed opaque head")
					if tc.refuse {
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"session child not found","data":{"kind":"sessionNotFound","retryable":false,"sessionId":"child"}}`)}
					}
					cursor := tc.head
					if tc.live {
						a.HandleOutput(childLifecycleFrameAtSource(t, methodTurnStarted, liveCursor, "genuine-live-turn", 1))
						cursor = liveCursor
					}
					result = map[string]any{"viewCursor": cursor}
				default:
					t.Errorf("unexpected empty view method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected empty view request"}`)}
				}
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				return agenttest.RPCReply{Result: encoded}
			}})
			a.syncView("child")
			wantPages := 1
			if tc.refuse {
				wantPages = 2
			}
			assert.Equal(t, wantPages, pages)
			assert.Empty(t, child.Messages(), "an empty view or a live start must not fabricate a chat item")
			if tc.wantError != "" {
				if tc.refuse {
					assert.Equal(t, 1, subscriptions)
				} else {
					assert.Zero(t, subscriptions)
				}
				assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
				require.NotNil(t, state.projection)
				require.ErrorContains(t, state.projection.failed, tc.wantError)
				assert.Nil(t, state.acceptedViewCursor)
				assert.False(t, state.nativeTurn.known)
				assert.Empty(t, child.TurnStates(), "a failed empty view must not publish invented idle activity")
				if tc.refuse {
					var refusal *providerkit.JSONRPCResponseError
					require.ErrorAs(t, state.projection.failed, &refusal)
					assert.Equal(t, -32020, refusal.Code)
					assert.Equal(t, "session child not found", refusal.Message)
					require.ErrorIs(t, a.SendChildInput(state.childKey, "cannot bypass unknown activity", nil), agent.ErrChildRouteNotReady)
					assert.NotContains(t, methods, methodChildFollowup)
					assert.NotContains(t, methods, methodChildSend)
				}
				return
			}
			assert.Equal(t, 1, subscriptions)
			assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
			require.NotNil(t, state.nativeHead)
			assert.False(t, state.nativeHead.activityKnown, "an empty page is not an idle lifecycle fact")
			assert.Equal(t, tc.live, state.nativeTurn.known)
			assert.Equal(t, agent.TurnState{Active: tc.live, Steerable: tc.live}, a.ActiveChildTurnState(state.childKey))
			require.NotNil(t, state.acceptedViewCursor)
			if tc.live {
				assert.Equal(t, liveCursor, *state.acceptedViewCursor)
				assert.Equal(t, []agent.TurnState{{Active: true, Steerable: true}}, child.TurnStates())
			} else {
				assert.Equal(t, tc.head, *state.acceptedViewCursor)
				assert.Empty(t, child.TurnStates(), "unknown activity must not publish invented idle state")
			}
			assert.Empty(t, root.LeapMuxNotifications())
		})
	}
}
