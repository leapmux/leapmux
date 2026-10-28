package muse

import (
	"context"
	"encoding/json"

	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type museManualRequest struct {
	ID     int64
	Method string
	Params json.RawMessage
}
type museManualPeer struct{ requests chan museManualRequest }

func (peer *museManualPeer) Write(raw []byte) (int, error) {
	var request museManualRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return 0, err
	}
	peer.requests <- request
	return len(raw), nil
}
func (*museManualPeer) Close() error { return nil }

type museSubscriptionEvent struct {
	method, sessionID string
	subscriptionID    int64
}

func acknowledgeMuseRequest(a *Agent, request museManualRequest, result string) {
	a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(result)}))
}
func replyMuseRetirement(t *testing.T, a *Agent, request museManualRequest, events *[]museSubscriptionEvent) {
	t.Helper()
	var params struct {
		SessionID      string
		SubscriptionID int64
	}
	require.NoError(t, json.Unmarshal(request.Params, &params))
	*events = append(*events, museSubscriptionEvent{method: request.Method, sessionID: params.SessionID, subscriptionID: params.SubscriptionID})
	switch request.Method {
	case methodSessionStart:
		acknowledgeMuseRequest(a, request, "{\"session\":{\"sessionId\":\"replacement\",\"activeTurnId\":null},\"viewCursor\":\"replacement-head\"}")
	case methodViewSubscribe:
		acknowledgeMuseRequest(a, request, "{\"viewCursor\":\"prefix\"}")
	case methodViewUnsubscribe, methodLogUnsubscribe:
		acknowledgeMuseRequest(a, request, "{}")
	case methodLogStreams:
		a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"the retired session is unavailable\"}")}))
	default:
		t.Errorf("unexpected retirement request %s", request.Method)
		a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"unexpected request\"}")}))
	}
}
func museReadHeadReply() string {
	return "{\"session\":{\"sessionId\":\"child\",\"status\":\"idle\",\"activeTurnId\":null},\"viewCursor\":\"prefix\",\"history\":{\"mode\":\"none\",\"items\":null}}"
}

func TestChildReadCannotSubscribeAfterContextClear(t *testing.T) {
	for _, holdPage := range []bool{false, true} {
		name := "session read"
		if holdPage {
			name = "view page"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, root := testAgent(t)
				state, _ := nativeHistoryChild(t, a, root)
				a.viewWork.CloseAndWait()
				a.handshake.GrantedCapabilities = nil
				state.viewRecovery = &viewRecovery{}
				peer := &museManualPeer{requests: make(chan museManualRequest, 16)}
				a.SetStdinForTest(peer)
				synchronized := make(chan struct{})
				go func() { defer close(synchronized); a.syncChildView("child", state) }()
				held := <-peer.requests
				require.Equal(t, methodSessionRead, held.Method)
				if holdPage {
					acknowledgeMuseRequest(a, held, museReadHeadReply())
					held = <-peer.requests
					require.Equal(t, methodViewPage, held.Method)
				}
				cleared := make(chan error, 1)
				go func() { _, err := a.ClearContext(); cleared <- err }()
				var events []museSubscriptionEvent
			clearing:
				for {
					select {
					case err := <-cleared:
						require.NoError(t, err)
						break clearing
					case request := <-peer.requests:
						replyMuseRetirement(t, a, request, &events)
					}
				}
				require.True(t, state.retired)
				afterClear := len(events)
				if holdPage {
					frame := childHistoryItemEvent(t, "prefix", json.RawMessage("{\"itemId\":\"old\",\"revision\":1,\"kind\":\"agentMessage\",\"status\":\"completed\",\"text\":\"Old history\"}"), 1)
					raw, err := json.Marshal(map[string]any{"events": []json.RawMessage{frame}, "nextCursor": "prefix"})
					require.NoError(t, err)
					acknowledgeMuseRequest(a, held, string(raw))
				} else {
					acknowledgeMuseRequest(a, held, museReadHeadReply())
				}
			syncing:
				for {
					select {
					case <-synchronized:
						break syncing
					case request := <-peer.requests:
						replyMuseRetirement(t, a, request, &events)
					}
				}
				for _, event := range events[afterClear:] {
					assert.False(t, event.sessionID == "child" && (event.method == methodViewSubscribe || event.method == methodLogSubscribe), "retirement must precede admission of any later subscription")
				}
			})
		})
	}
}

func TestContextClearWaitsForAdmittedChildSubscription(t *testing.T) {
	for _, logSubscription := range []bool{false, true} {
		name := "view"
		if logSubscription {
			name = "log"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, root := testAgent(t)
				state, _ := nativeHistoryChild(t, a, root)
				a.viewWork.CloseAndWait()
				replacement := newNativeLog("replacement")
				replacement.subscriptionID = 99
				a.sessions["replacement"] = &sessionState{sink: a.sink, log: replacement}
				peer := &museManualPeer{requests: make(chan museManualRequest, 16)}
				a.SetStdinForTest(peer)
				finished := make(chan error, 1)
				if logSubscription {
					state.log.subscriptionID = 0
					go func() { finished <- a.subscribeLog("child", a.APITimeout()) }()
				} else {
					state.viewRecovery = &viewRecovery{}
					go func() { a.syncChildView("child", state); finished <- nil }()
				}
				held := <-peer.requests
				if logSubscription {
					require.Equal(t, methodLogSubscribe, held.Method)
				} else {
					require.Equal(t, methodSessionRead, held.Method)
					acknowledgeMuseRequest(a, held, "{\"session\":{\"sessionId\":\"child\",\"status\":\"idle\",\"activeTurnId\":null},\"viewCursor\":\"prefix\",\"history\":{\"mode\":\"inline\",\"items\":[]}}")
					held = <-peer.requests
					require.Equal(t, methodViewPage, held.Method)
					assert.JSONEq(t, `{"sessionId":"child","direction":"forward","limit":200}`, string(held.Params))
					acknowledgeMuseRequest(a, held, `{"events":[],"nextCursor":null}`)
					held = <-peer.requests
					require.Equal(t, methodViewSubscribe, held.Method)
					assert.JSONEq(t, `{"sessionId":"child","after":"prefix"}`, string(held.Params))
				}
				cleared := make(chan error, 1)
				go func() { _, err := a.ClearContext(); cleared <- err }()
				var events []museSubscriptionEvent
				request := <-peer.requests
				require.Equal(t, methodSessionStart, request.Method)
				replyMuseRetirement(t, a, request, &events)
			quiescent:
				for {
					synctest.Wait()
					select {
					case request := <-peer.requests:
						replyMuseRetirement(t, a, request, &events)
					default:
						break quiescent
					}
				}
				clearPending := cleared
				select {
				case err := <-cleared:
					require.NoError(t, err)
					t.Error("context clear returned before an admitted subscription supplied its native acknowledgement")
					clearPending = nil
				default:
				}
				for _, event := range events {
					assert.False(t, event.method == methodViewUnsubscribe && event.sessionID == "child", "an admitted subscription must complete before its retirement unsubscribe")
				}
				if logSubscription {
					acknowledgeMuseRequest(a, held, "{\"subscriptionId\":42,\"tail\":{\"id\":\"source-3\",\"sequence\":3}}")
				} else {
					acknowledgeMuseRequest(a, held, "{\"viewCursor\":\"prefix\"}")
				}
				actorPending := finished
				for clearPending != nil || actorPending != nil {
					select {
					case err := <-clearPending:
						require.NoError(t, err)
						clearPending = nil
					case <-actorPending:
						actorPending = nil
					case request := <-peer.requests:
						replyMuseRetirement(t, a, request, &events)
					}
				}
				childUnsubscribed := 0
				lateLogUnsubscribed := 0
				for _, event := range events {
					if event.method == methodViewUnsubscribe && event.sessionID == "child" {
						childUnsubscribed++
					}
					if event.method == methodLogUnsubscribe && event.subscriptionID == 42 {
						lateLogUnsubscribed++
					}
					assert.False(t, event.method == methodLogUnsubscribe && event.subscriptionID == 99, "the replacement subscription remains attached")
				}
				assert.Equal(t, 1, childUnsubscribed)
				if logSubscription {
					assert.Equal(t, 1, lateLogUnsubscribed, "cleanup owns the native ID returned after retirement")
				}
			})
		})
	}
}

func TestDetachedNativeLogSubscriptionIDCanBeReused(t *testing.T) {
	for _, name := range []string{"detached", "still attached", "invalid acknowledgement"} {
		refused := name != "detached"
		t.Run(name, func(t *testing.T) {
			a, _ := testAgent(t)
			old := a.sessions["session"]
			old.log.subscriptionID = 7
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				switch method {
				case methodSessionStart:
					return agenttest.RPCReply{Result: json.RawMessage("{\"session\":{\"sessionId\":\"replacement\"},\"viewCursor\":\"new-head\"}")}
				case methodViewUnsubscribe:
					return agenttest.RPCReply{Result: json.RawMessage("{}")}
				case methodLogUnsubscribe:
					if name == "invalid acknowledgement" {
						return agenttest.RPCReply{Result: json.RawMessage("null")}
					}
					if refused {
						return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"subscription remains attached\"}")}
					}
					return agenttest.RPCReply{Result: json.RawMessage("{}")}
				case methodLogSubscribe:
					return agenttest.RPCReply{Result: json.RawMessage("{\"subscriptionId\":7,\"tail\":{\"id\":\"replacement-record\",\"sequence\":1}}")}
				case methodLogPage:
					return agenttest.RPCReply{Result: json.RawMessage("{\"records\":[{\"id\":\"replacement-record\",\"sequence\":1,\"stream\":{\"kind\":\"session\",\"id\":\"replacement\"},\"payload\":{}}],\"nextCursor\":{\"stream\":{\"kind\":\"session\",\"id\":\"replacement\"},\"after\":{\"id\":\"replacement-record\",\"sequence\":1}}}")}
				default:
					t.Errorf("unexpected subscription reuse method %s", method)
					return agenttest.RPCReply{Error: json.RawMessage("{\"code\":-32000,\"message\":\"unexpected method\"}")}
				}
			}})
			_, err := a.ClearContext()
			require.NoError(t, err)
			a.handshake.GrantedCapabilities = []string{"rawLog"}
			require.NoError(t, a.subscribeLog("replacement", a.APITimeout()))
			current := a.sessions["replacement"].log
			if refused {
				assert.Equal(t, int64(7), old.log.subscriptionID)
				assert.Zero(t, current.subscriptionID)
				assert.Contains(t, current.unavailable, "another session")
			} else {
				assert.Zero(t, old.log.subscriptionID)
				assert.Equal(t, int64(7), current.subscriptionID)
				assert.Empty(t, current.unavailable)
			}
		})
	}
}

func TestSessionSubscriptionWaitEndsWithItsOwningProcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		a := &Agent{connection: &connection{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: ctx, Cancel: cancel})}}}
		state := &sessionState{}
		release, err := a.acquireSessionSubscription(state)
		require.NoError(t, err)
		defer release()
		waiting := make(chan error, 1)
		go func() {
			next, err := a.acquireSessionSubscription(state)
			if next != nil {
				next()
			}
			waiting <- err
		}()
		// Channel waits are durable in synctest; sync.Mutex waits prevent this check.
		synctest.Wait()
		select {
		case <-waiting:
			t.Error("another registration bypassed the admitted operation")
		default:
		}
		cancel()
		require.ErrorIs(t, <-waiting, context.Canceled)
		release()
		release()
		next, err := a.acquireSessionSubscription(state)
		assert.Nil(t, next)
		assert.ErrorIs(t, err, context.Canceled)
	})
}
