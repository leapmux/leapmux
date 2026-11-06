package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexChildRouteFailureServices struct{ agent.ProviderServices }

func (codexChildRouteFailureServices) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	return "", assert.AnError
}

func TestCodexAuthenticationObservationPrecedesChildRouting(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing route", "route write failure", "event count limit", "event byte limit", "ordinary failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "main-thread"
			a.inputAuthenticationChecked = true
			feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)
			originalServices := a.sink
			if scenario == "route write failure" {
				a.sink = codexChildRouteFailureServices{ProviderServices: a.sink}
				a.Mu.Lock()
				state := a.codexChildStateLocked("unrouted-child")
				state.spawnCorrelationID = "spawn-unrouted"
				state.parentThreadID = "main-thread"
				a.Mu.Unlock()
			}
			if scenario == "event count limit" {
				for range codexPendingChildEventLimit {
					a.enqueuePendingCodexChildEvent("unrouted-child", codexPendingChildEvent{
						kind: codexPendingTurnStarted, params: json.RawMessage(`{"threadId":"unrouted-child","turn":{"id":"child-turn"}}`),
					})
				}
			}
			if scenario == "event byte limit" {
				params := `{"threadId":"unrouted-child","turn":{"id":"child-turn"},"padding":"` + strings.Repeat("x", codexPendingChildEventBytesLimit-128) + `"}`
				a.enqueuePendingCodexChildEvent("unrouted-child", codexPendingChildEvent{kind: codexPendingTurnStarted, params: json.RawMessage(params)})
			}
			info := "unauthorized"
			if scenario == "ordinary failure" {
				info = "serverOverloaded"
			}
			feedCodexLines(a, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"unrouted-child","turn":{"id":"child-turn","status":"failed","error":{"message":"request failed","codexErrorInfo":%q}}}}`, info))
			if scenario == "ordinary failure" {
				assert.False(t, recoveryRequired(a))
				require.NoError(t, a.PrepareInputProcess())
				require.NoError(t, a.SteerInput("ordinary input", nil))
				require.Len(t, requests(), 1)
				assert.Equal(t, "turn/steer", requests()[0].Method)
				return
			}
			require.ErrorIs(t, a.PrepareInputProcess(), agent.ErrInputProcessRestartRequired)
			require.ErrorIs(t, a.SteerInput("new input", nil), agent.ErrInputProcessRestartRequired)
			assert.Empty(t, requests())
			assert.Equal(t, "main-turn", a.mainTurn.id, "the child failure does not end its parent turn")
			if scenario == "event count limit" || scenario == "event byte limit" {
				a.Mu.Lock()
				dropped := a.collabChildren["unrouted-child"].pendingOutputDropped
				a.Mu.Unlock()
				require.True(t, dropped, "the completion exceeded the pending-route limit")
			}
			a.sink = originalServices
			feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"main-turn","item":{"type":"subAgentActivity","id":"spawn-unrouted","kind":"started","agentThreadId":"unrouted-child","agentPath":"/root/unrouted-child"}}}`)
			assert.True(t, recoveryRequired(a), "route creation cannot erase process authentication evidence")
		})
	}
}

func TestCodexRetiredAuthenticationOutputOnlyChangesLiveProcessHealth(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"retired main", "retired child", "stopped process", "exited process"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "main-thread"
			a.inputAuthenticationChecked = true
			feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"current-turn"}}}`)
			threadID := "retired-thread"
			if scenario == "retired child" {
				threadID = "retired-child"
			}
			a.retiredCodexThreads = map[string]struct{}{threadID: {}}
			ended := scenario == "stopped process" || scenario == "exited process"
			if ended {
				a.SimulateExitForTest()
			}
			if scenario == "stopped process" {
				a.Stop()
			}
			messages := len(sink.Messages())
			notifications := len(sink.PersistedNotifications())
			feedCodexLines(a, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"old-turn","status":"failed","error":{"message":"request failed","codexErrorInfo":"unauthorized"}}}}`, threadID))
			assert.Equal(t, !ended, recoveryRequired(a))
			assert.Equal(t, "main-thread", a.currentThreadID())
			if !ended {
				assert.Equal(t, "current-turn", a.mainTurn.id)
				require.ErrorIs(t, a.SendInput("later input", nil), agent.ErrInputProcessRestartRequired)
			}
			assert.Len(t, sink.Messages(), messages, "retired output writes no current transcript message")
			assert.Len(t, sink.PersistedNotifications(), notifications)
			assert.Empty(t, requests())
		})
	}
}

func TestCodexConcurrentInputAuthenticationSharesNativeCheck(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	held := make(chan func(agenttest.RPCReply), 2)
	a, _, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
		if request.Method == codexMethodAccountRead {
			held <- reply
			return
		}
		reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	})
	first := a.beginInputAuthenticationCheck()
	var reply func(agenttest.RPCReply)
	select {
	case reply = <-held:
	case <-ctx.Done():
		t.Fatal("the first native authentication check did not arrive")
	}
	require.True(t, a.Mu.TryLock(), "authentication waits without the process state mutex")
	a.Mu.Unlock()
	require.True(t, a.outputMu.TryLock(), "authentication waits without the output mutex")
	a.outputMu.Unlock()
	second := a.beginInputAuthenticationCheck()
	valid := accountReadReply("", false)
	reply(valid)
	require.NoError(t, (<-first).Err)
	// If the calls did not join one check, settle the extra native request so
	// the test reports a count failure rather than leaving a blocked goroutine.
	select {
	case result := <-second:
		require.NoError(t, result.Err)
	case extra := <-held:
		extra(valid)
		require.NoError(t, (<-second).Err)
	case <-ctx.Done():
		t.Fatal("the concurrent check did not settle")
	}
	assert.Len(t, requests(), 1, "concurrent preparation shares one native account read")
	require.NoError(t, a.PrepareInputProcess())
	assert.Len(t, requests(), 1, "a successful check remains cached")
}

func TestCodexMalformedAccountResponseRemainsRetryable(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"false", "0", `""`, `"account"`, "[]", "{}",
		`{"type":false}`, `{"type":"unknown"}`,
		`{"type":"chatgpt","email":null}`,
		`{"type":"chatgpt","email":false,"planType":"pro"}`,
		`{"type":"chatgpt","email":null,"planType":3}`,
		`{"type":"amazonBedrock","usesCodexManagedCredentials":"yes"}`,
	} {
		for _, requiresAuth := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/auth required %t", raw, requiresAuth), func(t *testing.T) {
				t.Parallel()
				reads := 0
				a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
					if request.Method == codexMethodAccountRead {
						reads++
						if reads == 1 {
							return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(
								`{"account":%s,"requiresOpenaiAuth":%t}`, raw, requiresAuth))}
						}
						return agenttest.RPCReply{Result: json.RawMessage(
							`{"account":{"type":"chatgpt","email":null,"planType":"pro"},"requiresOpenaiAuth":true}`)}
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				})
				require.Error(t, a.PrepareInputProcess())
				assert.False(t, recoveryRequired(a))
				assert.False(t, inputChecked(a))
				require.NoError(t, a.PrepareInputProcess())
				assert.True(t, inputChecked(a))
				assert.Len(t, requests(), 2)
			})
		}
	}
}

func TestCodexMultiVariantErrorDetailsDoNotMarkAuthenticationRecovery(t *testing.T) {
	t.Parallel()
	for _, details := range []string{
		`{}`,
		`{"futureError":{"detail":"unknown"}}`,
		`{"httpConnectionFailed":{"httpStatusCode":401},"responseStreamDisconnected":{"httpStatusCode":500}}`,
		`{"httpConnectionFailed":{"httpStatusCode":401},"futureError":{}}`,
	} {
		t.Run(details, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agent.NewProviderServices(sink))
			frame := fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","error":{"message":"request failed","codexErrorInfo":%s}}}}`, details)
			line := providerkit.ParseLine([]byte(frame))
			handleCodexOutput(a, line)
			assert.False(t, recoveryRequired(a), "an object without one native variant is no authentication signal")
			require.Len(t, sink.Messages(), 1)
			assert.JSONEq(t, string(line.Params), string(sink.Messages()[0].Content))
		})
	}
}

func TestCodexNativeAccountVariantsAndOptionalAccount(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`{"requiresOpenaiAuth":false}`,
		`{"account":null,"requiresOpenaiAuth":false}`,
		`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`,
		`{"account":{"type":"gcp"},"requiresOpenaiAuth":false}`,
		`{"account":{"type":"amazonBedrock"},"requiresOpenaiAuth":false}`,
		`{"account":{"type":"amazonBedrock","usesCodexManagedCredentials":true},"requiresOpenaiAuth":false}`,
		`{"account":{"type":"chatgpt","email":null,"planType":"pro"},"requiresOpenaiAuth":true}`,
		`{"account":{"type":"chatgpt","email":"user@example.invalid","planType":"unknown","workspaceName":null},"requiresOpenaiAuth":true}`,
	} {
		t.Run(response, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(response)}
			})
			require.NoError(t, a.PrepareInputProcess())
			assert.True(t, inputChecked(a))
			assert.False(t, recoveryRequired(a))
			assert.Len(t, requests(), 1)
		})
	}
}

func failCodexChildAuthentication(a *Agent) {
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"subAgentActivity","id":"auth-child-spawn","kind":"started","agentThreadId":"auth-child","agentPath":"/root/auth-child"}}}`,
		`{"method":"turn/completed","params":{"threadId":"auth-child","turn":{"id":"auth-child-turn","status":"failed","error":{"message":"signed in to another account","codexErrorInfo":"unauthorized"}}}}`,
	)
}

func TestCodexAuthenticationRecoveryPreservesOutstandingNativeControlReplies(t *testing.T) {
	t.Parallel()
	a, _, stdin := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))
	require.True(t, a.OutstandingControlForTest("jsonrpc:7"))
	a.markAuthenticationRecoveryRequired()
	answer := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.JSONEq(t, `{"decision":"accept"}`, string(stdinResponse(t, stdin, "7")))
	assert.False(t, a.OutstandingControlForTest("jsonrpc:7"))
	assert.True(t, recoveryRequired(a), "a control answer does not clear process recovery state")
	require.ErrorIs(t, a.SendRawInput(answer, agent.StopContext{}), providerkit.ErrControlRequestNotPending)
}

func TestCodexAuthenticationRetryAdmission(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"before acceptance", "before interrupt", "before revert"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			var a *Agent
			a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
				if phase == "before revert" && method == "turn/interrupt" {
					failCodexChildAuthentication(a)
				}
				return agenttest.RPCReply{}, false
			}))
			armRetryableTurn(t, a, sink)
			if phase == "before acceptance" {
				failCodexChildAuthentication(a)
				retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
				require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
				assert.Nil(t, retry)
				assert.Equal(t, "turn-1", a.mainTurn.id)
			} else {
				retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
				require.NoError(t, err)
				if phase == "before interrupt" {
					failCodexChildAuthentication(a)
				}
				require.ErrorIs(t, retry.Replace(), agent.ErrInputProcessRestartRequired)
				assert.False(t, a.retryingForTest(), "a refused retry releases its own queue hold")
				if phase == "before interrupt" {
					assert.Equal(t, "turn-1", a.mainTurn.id)
				}
			}
			if phase == "before revert" {
				assert.Equal(t, []string{"turn/interrupt"}, requestMethods(requests()))
			} else {
				assert.Empty(t, requests(), "the stale retry must mutate no native state")
			}
		})
	}
}

func TestCodexAuthenticationFailureAfterRevertSubmissionPreservesItsOutcome(t *testing.T) {
	t.Parallel()
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("revert rejected %t", rejected), func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			held := make(chan func(agenttest.RPCReply), 1)
			var a *Agent
			responder := retryResponder(&a, nil)
			a, sink, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
				if request.Method == "thread/revert" {
					held <- reply
					return
				}
				reply(responder(request.Method))
			})
			armRetryableTurn(t, a, sink)
			retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			require.NoError(t, err)
			require.NoError(t, retry.Replace())
			var deliver func(agenttest.RPCReply)
			select {
			case deliver = <-held:
			case <-ctx.Done():
				t.Fatal("the history revert did not reach the peer")
			}
			failCodexChildAuthentication(a)
			if rejected {
				deliver(agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"revert refused after admission"}`)})
			} else {
				deliver(agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"main-thread"}}`)})
			}
			err = retry.Start()
			if rejected {
				require.ErrorContains(t, err, "revert refused after admission")
				assert.True(t, a.retryingForTest(), "a failed admitted history mutation retains its queue hold")
			} else {
				require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
				assert.False(t, a.retryingForTest())
			}
			assert.Equal(t, []string{"turn/interrupt", "thread/revert"}, requestMethods(requests()))
			assert.True(t, recoveryRequired(a))
			assert.Equal(t, "main-thread", a.currentThreadID())
		})
	}
}

func TestCodexAuthenticationFailureDoesNotAutoRetry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"stream disconnected before completion: Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again.","codexErrorInfo":"other"}}}}`
	line := providerkit.ParseLine([]byte(input))
	handleCodexOutput(a, line)

	assert.True(t, recoveryRequired(a))
	assert.Zero(t, sink.AutoScheduleCount(), "authentication recovery requires later user input")
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(line.Params), string(sink.Messages()[0].Content))
	assert.True(t, sink.Messages()[0].TurnEnd)
}

func TestCodexCompactionRetainsLateAuthenticationRejection(t *testing.T) {
	t.Parallel()
	for _, completedBy := range []string{"acknowledgment", "timeout"} {
		t.Run(completedBy, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			clock := testutil.NewQuartzMock(t)
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, codexCompactionAckTimerTag)
			held := make(chan func(agenttest.RPCReply), 1)
			a, sink, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
				if request.Method == "thread/compact/start" {
					held <- reply
					return
				}
				reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
			}, clock)
			a.threadID = "main-thread"
			done := make(chan error, 1)
			go func() { done <- a.CompactContext() }()
			var deliver func(agenttest.RPCReply)
			select {
			case deliver = <-held:
			case <-ctx.Done():
				t.Fatal("the compaction request did not reach the peer")
			}
			delay := testutil.WaitForTimer(t, ctx, newTimer)
			if completedBy == "acknowledgment" {
				feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-1"}}}`)
				stopTimer.MustWait(ctx).MustRelease(ctx)
			} else {
				testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
			}
			var original error
			select {
			case original = <-done:
			case <-ctx.Done():
				t.Fatal("the compaction caller did not finish")
			}
			if completedBy == "acknowledgment" {
				require.NoError(t, original)
			} else {
				require.ErrorIs(t, original, agent.ErrDeliveryUncertain)
			}
			require.False(t, recoveryRequired(a))
			if completedBy == "acknowledgment" {
				armRetryableTurn(t, a, sink)
			}
			deliver(agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"workspace routing discovery unauthorized (401)"}`)})
			require.True(t, recoveryRequired(a), "the response owner retains a late explicit authentication rejection")
			if completedBy == "acknowledgment" {
				offer, present := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
				require.True(t, present)
				assert.Nil(t, offer, "a late authentication rejection withdraws a visible retry offer")
			}
			before := len(requests())
			require.ErrorIs(t, a.SendInput("later input", nil), agent.ErrInputProcessRestartRequired)
			assert.Len(t, requests(), before)
			if completedBy == "timeout" {
				assert.ErrorIs(t, original, agent.ErrDeliveryUncertain, "a later reply does not rewrite the caller's earlier result")
			}
		})
	}
}

func TestCodexRetiredCompactionMarksTheProcessWithoutClearingTheNewRequest(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	held := make(chan func(agenttest.RPCReply), 2)
	a, _, _ := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
		switch request.Method {
		case "thread/compact/start":
			held <- reply
		case "thread/start":
			reply(agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"new-thread","historyMode":"paginated","ephemeral":false},"model":"gpt-5.4"}`)})
		default:
			reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
		}
	})
	a.threadID = "main-thread"
	first := make(chan error, 1)
	go func() { first <- a.CompactContext() }()
	var oldReply func(agenttest.RPCReply)
	select {
	case oldReply = <-held:
	case <-ctx.Done():
		t.Fatal("the first compaction did not arrive")
	}
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-1"}}}`)
	select {
	case err := <-first:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the first compaction acknowledgment did not settle")
	}
	_, err := a.ClearContext()
	require.NoError(t, err)
	second := make(chan error, 1)
	go func() { second <- a.CompactContext() }()
	var newReply func(agenttest.RPCReply)
	select {
	case newReply = <-held:
	case <-ctx.Done():
		t.Fatal("the new thread's compaction did not arrive")
	}
	a.Mu.Lock()
	newAck := a.compactionStartAck
	a.Mu.Unlock()
	require.NotNil(t, newAck)
	oldReply(agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"workspace routing discovery unauthorized (401)"}`)})
	// A later round trip proves that the ordered reader consumed the old reply.
	_, err = a.SendRequest("fixture/barrier", json.RawMessage(`{}`), a.APITimeout())
	require.NoError(t, err)
	assert.True(t, recoveryRequired(a), "a native thread change retains the physical process's authentication state")
	a.Mu.Lock()
	assert.Equal(t, newAck, a.compactionStartAck)
	a.Mu.Unlock()
	newReply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	select {
	case err := <-second:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the new thread's compaction did not settle")
	}
}
