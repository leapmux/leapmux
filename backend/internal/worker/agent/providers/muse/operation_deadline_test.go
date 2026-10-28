package muse

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildBootstrapKeepsTheFirstReceivedFramesDeadline(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, root := testAgentWithClock(t, clock)
	state, _ := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	recovery := &viewRecovery{}
	state.viewRecovery = recovery
	started := clock.Now()
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "held", "delta": "unproved"})
	clock.Advance(5 * time.Second).MustWait(testutil.DeadlineContext(t))
	a.acceptChildView("child", state, recovery, childViewStart{})
	require.Len(t, state.projection.frames, 1)
	assert.Equal(t, started.Add(a.childProofTimeout()), state.projection.frames[0].deadline,
		"bootstrap must consume the same allowance as the subsequent proof wait")
}

func TestNativeLogPagesShareOneOperationDeadline(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, _ := testAgentWithClock(t, clock)
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodLogPage, method)
		calls++
		clock.Advance(20 * time.Second).MustWait(testutil.DeadlineContext(t))
		record := map[string]any{"id": "record-" + string(rune('0'+calls)), "sequence": calls,
			"stream": nativeStream{Kind: "session", ID: "session"}, "payload": map[string]any{"kind": "control"}}
		raw, err := json.Marshal(map[string]any{"records": []any{record}, "nextCursor": map[string]any{
			"stream": nativeStream{Kind: "session", ID: "session"},
			"after":  nativePosition{ID: record["id"].(string), Sequence: int64(calls)},
		}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: raw}
	}})
	_, err := a.readLogPages("session", nil, nativePosition{ID: "record-3", Sequence: 3}, 30*time.Second)
	require.Error(t, err, "each log page must consume the original operation allowance")
	assert.LessOrEqual(t, calls, 2, "an expired operation must not write another page request")
}

func TestChildSubscriptionDeadlineIncludesItsAdmissionWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		a, root := testAgentWithClock(t, clock)
		state, _ := nativeHistoryChild(t, a, root)
		a.viewWork.CloseAndWait()
		release, err := a.acquireSessionSubscription(state)
		require.NoError(t, err)
		peer := &museManualPeer{requests: make(chan museManualRequest, 4)}
		a.SetStdinForTest(peer)
		finished := make(chan error, 1)
		go func() { finished <- a.subscribeChildView("child", state, "before", clock.Now().Add(30*time.Second)) }()
		synctest.Wait()
		clock.Advance(30 * time.Second).MustWait(testutil.DeadlineContext(t))
		synctest.Wait()
		select {
		case err := <-finished:
			require.Error(t, err)
		default:
			t.Error("the operation deadline did not end the subscription admission wait")
			release()
			require.Error(t, <-finished)
		}
		release()
		assert.Empty(t, peer.requests, "an expired operation cannot create a subscription")
	})
}

func TestExpiredChildSubscriptionRetainsItsLateResourceAcknowledgement(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, root := testAgentWithClock(t, clock)
	state, _ := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	state.log.subscriptionID = 0
	a.sessions["session"].log.subscriptionID = 99
	peer := &museManualPeer{requests: make(chan museManualRequest, 16)}
	a.SetStdinForTest(peer)
	opctx, finish := a.newChildProofContext(clock.Now().Add(a.childProofTimeout()))
	defer finish()
	done := make(chan error, 1)
	go func() {
		_, err := a.registerNativeLogContext(opctx, "child", state)
		done <- err
	}()
	wait := testutil.DeadlineContext(t)
	var held museManualRequest
	select {
	case held = <-peer.requests:
	case <-wait.Done():
		t.Fatal("the subscription was not sent")
	}
	require.Equal(t, methodLogSubscribe, held.Method)
	clock.Advance(a.childProofTimeout()).MustWait(wait)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-wait.Done():
		t.Fatal("the admitted resource prevented operation expiry")
	}
	a.stateMu.Lock()
	state.retired = true
	a.stateMu.Unlock()
	cleaned := make(chan struct{})
	go func() { defer close(cleaned); a.unsubscribeRetiredSession("child", state) }()
	require.True(t, a.Deliver(held.ID, agenttest.JSONRPCResponse(held.ID, agenttest.RPCReply{
		Result: json.RawMessage(`{"subscriptionId":42,"tail":{"id":"source-3","sequence":3}}`),
	})), "operation expiry must preserve the admitted resource acknowledgement")
	var removed []int64
	for {
		select {
		case request := <-peer.requests:
			switch request.Method {
			case methodViewUnsubscribe:
				acknowledgeMuseRequest(a, request, `{}`)
			case methodLogUnsubscribe:
				var params struct {
					ID int64 `json:"subscriptionId"`
				}
				require.NoError(t, json.Unmarshal(request.Params, &params))
				removed = append(removed, params.ID)
				acknowledgeMuseRequest(a, request, `{}`)
			default:
				t.Fatalf("unexpected cleanup request %s", request.Method)
			}
		case <-cleaned:
			a.subscriptionWork.CloseAndWait()
			assert.Equal(t, []int64{42}, removed)
			assert.Zero(t, state.log.subscriptionID)
			assert.Equal(t, int64(99), a.sessions["session"].log.subscriptionID)
			return
		case <-wait.Done():
			t.Fatal("cleanup did not detach the late subscription")
		}
	}
}
