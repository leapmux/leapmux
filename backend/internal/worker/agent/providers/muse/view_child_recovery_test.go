package muse

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const childRangeFailure = `{"code":-32603,"message":"view range read failed","data":{"kind":"internal","sessionId":"child"}}`

func TestChildViewReadKeepsTheRecoveryThatOwnedItsHead(t *testing.T) {
	for _, replaceAfter := range []string{methodSessionRead, methodViewPage} {
		t.Run(replaceAfter, func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			replacement := &viewRecovery{deadline: a.Clock().Now().Add(time.Hour)}
			pages, subscriptions := 0, 0
			frame := childHistoryItemEvent(t, "prefix", json.RawMessage(`{"itemId":"prefix","revision":1,"kind":"agentMessage","status":"completed","text":"prefix text"}`), 1)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				if method == methodSessionRead {
					return agenttest.RPCReply{Result: json.RawMessage(museReadHeadReply())}
				}
				if method == methodViewSubscribe {
					subscriptions++
					return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"prefix"}`)}
				}
				require.Equal(t, methodViewPage, method)
				pages++
				encoded, err := json.Marshal(map[string]any{"events": []json.RawMessage{frame}, "nextCursor": nil})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: encoded}
			}, afterReply: func(method string) {
				if method == replaceAfter {
					// The reply has arrived, but the caller has not resumed.
					a.stateMu.Lock()
					state.viewRecovery = replacement
					a.stateMu.Unlock()
				}
			}})
			a.syncView("child")
			assert.Equal(t, map[string]int{methodSessionRead: 0, methodViewPage: 1}[replaceAfter], pages,
				"an older head must not admit a read through a replacement recovery")
			assert.Zero(t, subscriptions, "an older page must not subscribe through a replacement recovery")
			assert.Same(t, replacement, state.viewRecovery)
			assert.Equal(t, childViewPrefixPending, state.childViewPrefix)
			assert.Empty(t, child.Messages())
			assert.Empty(t, root.LeapMuxNotifications())
		})
	}
}

func TestChildViewRecoversItsOwnedNativeRangeRead(t *testing.T) {
	for _, laterPage := range []bool{false, true} {
		t.Run(map[bool]string{false: "origin", true: "later page"}[laterPage], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := testutil.NewQuartzMock(t)
				a, root := testAgentWithClock(t, clock)
				state, child := nativeHistoryChild(t, a, root)
				a.viewWork.CloseAndWait()
				deadline := clock.Now().Add(a.childProofTimeout())
				state.viewRecovery = &viewRecovery{deadline: deadline}
				first := childHistoryItemEvent(t, "opaque:first", json.RawMessage(`{"itemId":"first","revision":1,"kind":"agentMessage","status":"completed","text":"first"}`), 1)
				second := childHistoryItemEvent(t, "opaque:second", json.RawMessage(`{"itemId":"second","revision":1,"kind":"agentMessage","status":"completed","text":"second"}`), 2)
				pages := 0
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
					switch method {
					case methodSessionRead:
						return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child","status":"idle","activeTurnId":null},"viewCursor":"opaque:second","history":{"mode":"none","items":null}}`)}
					case methodViewPage:
						pages++
						var params map[string]any
						require.NoError(t, json.Unmarshal(raw, &params))
						if laterPage && pages > 1 {
							assert.Equal(t, "opaque:first", params["cursor"])
						} else {
							assert.NotContains(t, params, "cursor")
						}
						if !laterPage && pages == 1 || laterPage && pages == 2 {
							return agenttest.RPCReply{Error: json.RawMessage(childRangeFailure)}
						}
						frame, next := first, any("opaque:first")
						if laterPage && pages > 2 {
							frame, next = second, nil
						}
						encoded, err := json.Marshal(map[string]any{"events": []json.RawMessage{frame}, "nextCursor": next})
						require.NoError(t, err)
						return agenttest.RPCReply{Result: encoded}
					case methodViewSubscribe:
						if laterPage {
							return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"session child not found"}`)}
						}
						// Overlap must retain the exact native order without duplicate rows.
						a.HandleOutput(first)
						a.HandleOutput(second)
						return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"opaque:second"}`)}
					default:
						t.Errorf("unexpected child read method %s", method)
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unexpected method"}`)}
					}
				}})
				created, stopped := testutil.NewTimerTraps(t, clock, "muse-child-view-read-recovery")
				done := make(chan struct{})
				go func() { defer close(done); a.syncView("child") }()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("a native range failure ended the child operation before recovery")
				default:
				}
				assert.Empty(t, child.Messages(), "unaccepted history must remain unpublished during recovery")
				wait := testutil.DeadlineContext(t)
				delay := testutil.WaitForTimer(t, wait, created)
				testutil.AdvanceAndAwaitStop(t, wait, clock, delay, stopped)
				<-done
				assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
				rows := child.Messages()
				require.Len(t, rows, 2)
				assert.Equal(t, []byte(first), rows[0].Content)
				assert.Equal(t, []byte(second), rows[1].Content)
				assert.Empty(t, root.LeapMuxNotifications())
				assert.Equal(t, map[bool]int{false: 2, true: 3}[laterPage], pages)
			})
		})
	}
}

func TestChildViewRangeRecoveryKeepsItsDeadlineEvidenceAndControls(t *testing.T) {
	for _, laterPage := range []bool{false, true} {
		t.Run(map[bool]string{false: "origin", true: "later page"}[laterPage], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := testutil.NewQuartzMock(t)
				a, root := testAgentWithClock(t, clock)
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
				deadline := clock.Now().Add(250 * time.Millisecond)
				recovery := &viewRecovery{deadline: deadline}
				state.viewRecovery = recovery
				held := feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "child", "itemId": "held", "delta": "unproved text"})
				prefix := childHistoryItemEvent(t, "opaque:prefix", json.RawMessage(`{"itemId":"prefix","revision":1,"kind":"agentMessage","status":"completed","text":"prefix text"}`), 1)
				pages := 0
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
					if method == methodSessionRead {
						return agenttest.RPCReply{Result: json.RawMessage(museReadHeadReply())}
					}
					if method == methodViewSubscribe && laterPage {
						return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32020,"message":"session child not found"}`)}
					}
					require.Equal(t, methodViewPage, method)
					pages++
					if laterPage && pages == 1 {
						encoded, err := json.Marshal(map[string]any{"events": []json.RawMessage{prefix}, "nextCursor": "opaque:prefix"})
						require.NoError(t, err)
						return agenttest.RPCReply{Result: encoded}
					}
					return agenttest.RPCReply{Error: json.RawMessage(childRangeFailure)}
				}})
				created, stopped := testutil.NewTimerTraps(t, clock, "muse-child-view-read-recovery")
				done := make(chan struct{})
				go func() { defer close(done); a.syncView("child") }()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("a native range failure discarded the original recovery allowance")
				default:
				}
				wait := testutil.DeadlineContext(t)
				firstDelay := testutil.WaitForTimer(t, wait, created)
				assert.Greater(t, firstDelay, time.Duration(0), "recovery must not spin")
				testutil.AdvanceAndAwaitStop(t, wait, clock, firstDelay, stopped)
				lastDelay := testutil.WaitForTimer(t, wait, created)
				assert.Equal(t, deadline.Sub(clock.Now()), lastDelay, "the last wait must consume only the original allowance")
				testutil.AdvanceAndAwaitStop(t, wait, clock, lastDelay, stopped)
				<-done
				assert.Equal(t, deadline, recovery.deadline)
				assert.Equal(t, map[bool]int{false: 2, true: 3}[laterPage], pages, "expiry must prevent another native request")
				assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
				assert.Empty(t, child.Messages(), "recovery cannot fabricate source proof")
				rows := root.Messages()
				require.Len(t, rows, map[bool]int{false: 1, true: 2}[laterPage])
				if laterPage {
					assert.Equal(t, []byte(prefix), rows[0].Content, "a later-page failure must retain the already-read native prefix")
				}
				assert.Equal(t, held, rows[len(rows)-1].Content)
				require.Len(t, root.LeapMuxNotifications(), 1)
				assert.Contains(t, root.LeapMuxNotifications()[0][contracts.NotificationFieldText], "view range read failed")
				assert.Contains(t, recorder.pending, key, "failed history must not discard an owned control")
			})
		})
	}
}

func TestChildViewRangeRecoveryStopsWhenItsOwnerEnds(t *testing.T) {
	for _, scenario := range []string{"process cancellation", "retirement", "session replacement", "recovery replacement", "proof failure"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := testutil.NewQuartzMock(t)
				a, root := testAgentWithClock(t, clock)
				state, child := nativeHistoryChild(t, a, root)
				a.viewWork.CloseAndWait()
				state.viewRecovery = &viewRecovery{}
				pages := 0
				a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
					if method == methodSessionRead {
						return agenttest.RPCReply{Result: json.RawMessage(museReadHeadReply())}
					}
					require.Equal(t, methodViewPage, method)
					pages++
					return agenttest.RPCReply{Error: json.RawMessage(childRangeFailure)}
				}})
				created, stopped := testutil.NewTimerTraps(t, clock, "muse-child-view-read-recovery")
				done := make(chan struct{})
				go func() { defer close(done); a.syncView("child") }()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("the native range read did not remain recoverable")
				default:
				}
				wait := testutil.DeadlineContext(t)
				testutil.WaitForTimer(t, wait, created)
				switch scenario {
				case "retirement":
					a.stateMu.Lock()
					a.retireSessionTreeLocked("session")
					a.stateMu.Unlock()
				case "session replacement", "recovery replacement":
					a.stateMu.Lock()
					if scenario == "session replacement" {
						a.sessions["child"] = &sessionState{}
					} else {
						state.viewRecovery = &viewRecovery{}
					}
					state.projection.signal()
					a.stateMu.Unlock()
				case "proof failure":
					a.dispatcher.mu.Lock()
					a.failChildProjectionLocked("child", state, errors.New("the source proof failed"), nil)
					a.dispatcher.mu.Unlock()
					a.drainDeferredOutputActions()
				case "process cancellation":
					a.CancelForTest()
				}
				stopped.MustWait(wait).MustRelease(wait)
				<-done
				assert.Equal(t, 1, pages)
				assert.Empty(t, child.Messages())
			})
		})
	}
}

func TestChildViewDoesNotRecoverOtherNativeFailures(t *testing.T) {
	for _, failure := range []string{
		`{"code":-32020,"message":"session child not found"}`,
		`{"code":-32020,"message":"view range read failed","data":{"kind":"internal","sessionId":"child"}}`,
		`{"code":-32603,"message":"another internal failure","data":{"kind":"internal","sessionId":"child"}}`,
		`{"code":-32603,"message":"view range read failed","data":{"kind":"internal","sessionId":"foreign"}}`,
		`{"code":-32603,"message":"view range read failed","data":{"kind":"invalidCursor","sessionId":"child"}}`,
		`{"code":-32603,"message":"view range read failed","data":{"kind":"internal","sessionId":"child","retryable":false}}`,
		`{"code":-32603,"message":"view range read failed"}`,
		`{"code":-32603,"message":"view range read failed","data":"invalid data"}`,
		`{"code":-32603,"message":"view range read failed","data":null}`,
	} {
		t.Run(failure, func(t *testing.T) {
			a, root := testAgent(t)
			state, child := nativeHistoryChild(t, a, root)
			a.viewWork.CloseAndWait()
			state.viewRecovery = &viewRecovery{}
			pages := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				if method == methodSessionRead {
					return agenttest.RPCReply{Result: json.RawMessage(museReadHeadReply())}
				}
				require.Equal(t, methodViewPage, method)
				pages++
				return agenttest.RPCReply{Error: json.RawMessage(failure)}
			}})
			a.syncView("child")
			assert.Equal(t, 1, pages)
			assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
			assert.Empty(t, child.Messages())
			require.Len(t, root.LeapMuxNotifications(), 1)
		})
	}
}

func TestChildViewRangeRecoveryCapsItsWaitAndKeepsTheOriginalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testutil.NewQuartzMock(t)
		a, root := testAgentWithClock(t, clock)
		state, child := nativeHistoryChild(t, a, root)
		a.viewWork.CloseAndWait()
		deadline := clock.Now().Add(a.childProofTimeout())
		recovery := &viewRecovery{deadline: deadline}
		state.viewRecovery = recovery
		pages := 0
		a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
			switch method {
			case methodSessionRead:
				return agenttest.RPCReply{Result: json.RawMessage(museReadHeadReply())}
			case methodViewPage:
				pages++
				if pages <= 6 {
					return agenttest.RPCReply{Error: json.RawMessage(childRangeFailure)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"events":[],"nextCursor":null}`)}
			case methodViewSubscribe:
				return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":"prefix"}`)}
			default:
				t.Fatalf("unexpected recovery method %s", method)
				return agenttest.RPCReply{}
			}
		}})
		created, stopped := testutil.NewTimerTraps(t, clock, "muse-child-view-read-recovery")
		done := make(chan struct{})
		go func() { defer close(done); a.syncView("child") }()
		wait := testutil.DeadlineContext(t)
		for _, expected := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second, time.Second} {
			delay := testutil.WaitForTimer(t, wait, created)
			assert.Equal(t, expected, delay)
			testutil.AdvanceAndAwaitStop(t, wait, clock, delay, stopped)
		}
		<-done
		assert.Equal(t, 7, pages)
		assert.Equal(t, deadline, recovery.deadline)
		assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
		assert.Empty(t, child.Messages())
		assert.Empty(t, root.LeapMuxNotifications())
	})
}
