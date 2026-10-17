package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexRetryRevertsTheCurrentNativeThread(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "thread/revert" {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"main-thread","historyMode":"paginated","ephemeral":false}}`)}, true
		}
		if method == "turn/start" {
			feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`)
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	sink.UpdateSessionID("main-thread")
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	assert.Equal(t, "main-thread", a.currentThreadID())
	assert.Equal(t, "main-thread", sink.LastSessionID())
	// The protocol proof does not wait for a start on an incorrectly replaced thread.
	if a.currentThreadID() != "main-thread" {
		return
	}
	require.NoError(t, retry.Start())
	sent := requests()
	require.Equal(t, []string{"turn/interrupt", "thread/revert", "turn/start"}, requestMethods(sent))
	assert.Equal(t, map[string]interface{}{"threadId": "main-thread", "beforeTurnId": "turn-1"}, sent[1].Params)
	assert.Equal(t, "main-thread", sent[2].Params["threadId"])
	assert.Zero(t, sink.GoalClears())
}

func TestCodexPendingRevertKeepsItsIdentityAndQueueHold(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "user stop", "context clear", "process exit", "error after mutation", "wrong thread"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			ctx := codexLifecycleTestContext(t)
			trap := clock.Trap().NewTimer(codexTurnRevertTimerTag)
			t.Cleanup(trap.Close)
			replyReady := make(chan func(agenttest.RPCReply), 1)
			var a *Agent
			a, base, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
				switch request.Method {
				case "turn/interrupt":
					feedCodexLines(a, retryTurnCompleted)
				case "thread/revert":
					replyReady <- reply
					return
				case "thread/start":
					reply(agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-thread","historyMode":"paginated","ephemeral":false},"model":"gpt-5.6"}`)})
					return
				case "turn/start":
					feedCodexLines(a, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"turn-2"}}}`, request.Params["threadId"]))
				}
				reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
			}, clock)
			sink := &codexRetryUncertaintySink{Sink: base, message: "thread/revert has no reply", reported: make(chan struct{})}
			a.sink = agent.NewProviderServices(sink)
			armRetryableTurn(t, a, base)
			retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			require.NoError(t, err)
			require.NoError(t, retry.Replace())
			reply := <-replyReady
			done := make(chan error, 1)
			go func() { done <- retry.Start() }()
			timeout := testutil.WaitForTimer(t, ctx, trap)
			clock.Advance(timeout).MustWait(ctx)
			<-sink.reported
			feedCodexLines(a, `{"method":"thread/reverted","params":{"threadId":"main-thread"}}`, `{"method":"thread/reverted","params":{"threadId":"main-thread"}}`)
			require.ErrorIs(t, a.SendInput("Must wait.", nil), agent.ErrAgentBusy)
			require.Equal(t, []string{"turn/interrupt", "thread/revert"}, requestMethods(requests()))
			switch outcome {
			case "context clear":
				_, err := a.ClearContext()
				require.NoError(t, err)
				require.ErrorIs(t, <-done, errCodexTurnRetryCancelled)
				reply(agenttest.RPCReply{Result: json.RawMessage(retryRevertReply)})
				assert.Equal(t, "fresh-thread", a.currentThreadID())
				model, _ := a.modelAndEffortForTest()
				assert.Equal(t, "gpt-5.6", model)
			case "process exit":
				a.SimulateExitForTest()
				require.Error(t, <-done)
				assert.False(t, a.retryingForTest())
			case "user stop":
				require.NoError(t, a.Interrupt(agent.StopContext{}))
				require.ErrorIs(t, a.SendInput("Still must wait.", nil), agent.ErrAgentBusy)
				reply(agenttest.RPCReply{Result: json.RawMessage(retryRevertReply)})
				require.ErrorIs(t, <-done, errCodexTurnRetryCancelled)
				assert.False(t, a.retryingForTest())
			case "error after mutation", "wrong thread":
				if outcome == "wrong thread" {
					reply(agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"unexpected-thread"}}`)})
				} else {
					reply(agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"History changed, but reload failed."}`)})
				}
				require.Error(t, <-done)
				require.ErrorIs(t, a.SendInput("Unsafe after a partial mutation.", nil), agent.ErrAgentBusy)
				_, err := a.ClearContext()
				require.NoError(t, err)
				assert.False(t, a.retryingForTest())
			case "success":
				reply(agenttest.RPCReply{Result: json.RawMessage(retryRevertReply)})
				require.NoError(t, <-done)
				require.Equal(t, []string{"turn/interrupt", "thread/revert", "turn/start"}, requestMethods(requests()))
				return
			}
			assert.NotContains(t, requestMethods(requests()), "turn/start", "only a confirmed current retry can send input")
		})
	}
}

func TestCodexRetryRequiresAConfirmedDurablePaginatedStart(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, method, metadata string
		eligible               bool
	}{
		{"durable paginated", "thread/start", `"historyMode":"paginated","ephemeral":false`, true},
		{"ephemeral", "thread/start", `"historyMode":"paginated","ephemeral":true`, false},
		{"legacy history", "thread/start", `"historyMode":"legacy","ephemeral":false`, false},
		{"absent history mode", "thread/start", `"ephemeral":false`, false},
		{"absent durability", "thread/start", `"historyMode":"paginated"`, false},
		{"resume without raw events", "thread/resume", `"historyMode":"paginated","ephemeral":false`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var a *Agent
			a, sink, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
				if method == "turn/start" {
					feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(`{"thread":{"id":"main-thread",%s},"model":"gpt-5.4"}`, test.metadata))}
			})
			resumeID := ""
			if test.method == "thread/resume" {
				resumeID = "main-thread"
			}
			thread, err := a.startOrResumeThread(codexThreadParams("gpt-5.4", "/work", a.permissionSettingsLocked(), ""), resumeID, a.APITimeout())
			require.NoError(t, err)
			a.applyThreadResult(thread)
			a.threadID = thread.ID
			require.NoError(t, a.SendInput("Retained input.", nil))
			feedCodexLines(a, `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.5"}}`)
			offer, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
			assert.Equal(t, test.eligible, exists && offer != nil)
			retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
			if test.eligible {
				require.NoError(t, err)
				retry.Abandon(nil)
			} else {
				require.ErrorIs(t, err, agent.ErrTurnRetryUnavailable)
			}
			params := requests()[0].Params
			if test.method == "thread/start" {
				assert.Equal(t, "paginated", params["historyMode"])
				assert.Equal(t, false, params["ephemeral"])
				assert.Equal(t, true, params["experimentalRawEvents"])
			} else {
				for _, field := range []string{"historyMode", "ephemeral", "experimentalRawEvents"} {
					assert.NotContains(t, params, field)
				}
			}
		})
	}
}

func TestCodexRevertFailureAfterProcessExitReleasesTheHold(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	a.SimulateExitForTest()
	retry.(*codexTurnRetry).fail(codexTurnRetryReverting, a.ProcessExitError())
	assert.False(t, a.retryingForTest(), "a finished process cannot later change history")
}
