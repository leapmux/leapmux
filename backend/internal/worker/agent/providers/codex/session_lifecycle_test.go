package codex

import (
	"encoding/json"
	"sync"
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

// The frames that a retry exchanges with Codex.
const (
	retryTurnCompleted = `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted"}}}`
	retryRevertReply   = `{"thread":{"id":"main-thread","historyMode":"paginated","ephemeral":false}}`
	retryTurnStarted   = `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`
)

// retryResponder answers the requests of one turn retry as Codex does. The
// turn/interrupt ends turn-1, the thread/revert returns main-thread on the model of
// the source thread, and the turn/start starts turn-2 on main-thread. A non-nil
// override answers a method in place of the default when it returns true.
func retryResponder(a **Agent, override func(method string) (agenttest.RPCReply, bool)) func(string) agenttest.RPCReply {
	return func(method string) agenttest.RPCReply {
		if override != nil {
			if reply, ok := override(method); ok {
				return reply
			}
		}
		switch method {
		case "turn/interrupt":
			handleCodexOutput(*a, providerkit.ParseLine([]byte(retryTurnCompleted)))
		case "thread/revert":
			return agenttest.RPCReply{Result: json.RawMessage(retryRevertReply)}
		case "turn/start":
			handleCodexOutput(*a, providerkit.ParseLine([]byte(retryTurnStarted)))
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

// armRetryableTurn makes turn-1 the running main turn with the input
// "Hello.", and a buffering notice offers its retry on gpt-5.5. Each step runs
// through the path that a live turn takes.
func armRetryableTurn(t *testing.T, a *Agent, sink *agenttest.Sink) {
	t.Helper()
	a.Mu.Lock()
	a.threadID = "main-thread"
	a.turnRetrySupported = true
	sink.UpdateSessionID("main-thread")
	a.pendingStart = newCodexTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "Hello."}}, a.turnSettingsLocked())
	a.Mu.Unlock()
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.5"}}`)))
	offer, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
	require.True(t, ok, "the buffering notice offers the retry")
	require.NotNil(t, offer, "the buffering notice offers the retry")
}

func (a *Agent) retryingForTest() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.retry != nil
}

func (a *Agent) modelAndEffortForTest() (string, string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.model, a.effort
}

func requestMethods(requests []codexRecordedRequest) []string {
	methods := make([]string, len(requests))
	for i, request := range requests {
		methods[i] = request.Method
	}
	return methods
}

// Retry removes the stopped attempt from native history and resends its retained input.
// The thread keeps its settings until the new turn overrides the model and effort.
func TestCodexTurnRetrySendsTheRetainedInputAfterRevert(t *testing.T) {
	t.Parallel()

	gapRefusals := make(chan error, 2)
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		switch method {
		case "turn/start":
			a.Mu.Lock()
			original := a.mainTurn.id == "" && a.retry == nil
			a.Mu.Unlock()
			if original {
				handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)))
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
			}
		case "thread/revert":
			// Inside the gap: the input queue must not drain into it.
			gapRefusals <- a.SendInput("more", nil)
			gapRefusals <- a.SteerInput("more", nil)
		}
		return agenttest.RPCReply{}, false
	}))
	a.threadID = "main-thread"
	a.turnRetrySupported = true

	// The turn carries a message and one steer, so the retry sends both again.
	require.NoError(t, a.sendTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "Hello."}},
		turnSettings{model: "gpt-5.4", effort: "high", collaborationMode: contracts.CodexOptionDefaultCollaborationMode}))
	require.NoError(t, a.SteerInput("guide", nil))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.5"}}`)))
	beforeRetry := len(sink.TurnKinds())

	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, retry)
	assert.Len(t, requests(), 2, "BeginTurnRetry sends no request")
	assert.True(t, a.retryingForTest(), "the input queue holds at once")

	// The accepted retry spends its offer. The banner must not offer the same
	// retry again while the new turn runs.
	spent, offered := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
	require.True(t, offered)
	assert.Nil(t, spent, "the accepted retry withdraws the offer that it spent")

	require.NoError(t, retry.Replace())
	require.ErrorIs(t, <-gapRefusals, agent.ErrAgentBusy, "SendInput refuses during the retry")
	require.ErrorIs(t, <-gapRefusals, agent.ErrAgentBusy, "SteerInput refuses during the retry")
	model, effort := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.4", model, "the revert keeps the model until the new turn starts")
	assert.Equal(t, "high", effort)
	assert.Equal(t, "main-thread", a.threadID)

	require.NoError(t, retry.Start())
	sent := requests()
	require.Equal(t, []string{"turn/start", "turn/steer", "turn/interrupt", "thread/revert", "turn/start"}, requestMethods(sent))
	assert.Equal(t, "main-thread", sent[2].Params["threadId"])
	assert.Equal(t, "turn-1", sent[2].Params["turnId"])

	revert := sent[3]
	assert.Equal(t, map[string]interface{}{"threadId": "main-thread", "beforeTurnId": "turn-1"}, revert.Params)

	turn := sent[4]
	assert.Equal(t, "main-thread", turn.Params["threadId"])
	assert.Equal(t, "gpt-5.5", turn.Params["model"])
	assert.Equal(t, "low", turn.Params["effort"])
	assert.Equal(t, []interface{}{
		map[string]interface{}{"type": "text", "text": "Hello."},
		map[string]interface{}{"type": "text", "text": "\n"},
		map[string]interface{}{"type": "text", "text": "guide"},
	}, turn.Params["input"])
	assert.Equal(t, map[string]interface{}{
		"mode": "default",
		"settings": map[string]interface{}{
			"model":                  "gpt-5.5",
			"reasoning_effort":       "low",
			"developer_instructions": nil,
		},
	}, turn.Params["collaborationMode"])

	// The transcript records the retry, and the settings display follows the
	// override that Codex keeps for later turns.
	notifications := codexLeapMuxNotices(t, sink)
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.NotificationTypeTurnRetried, notifications[0][contracts.NotificationFieldType])
	assert.Equal(t, "gpt-5.5", notifications[0][contracts.NotificationFieldModel])
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.5", refresh.Model)
	assert.Equal(t, "low", refresh.Effort)
	model, effort = a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.5", model)
	assert.Equal(t, "low", effort)

	// The retry's own turn/started cleared the hold and opened the new turn.
	assert.False(t, a.retryingForTest())
	assert.Equal(t, "turn-2", a.mainTurn.id)
	assert.Zero(t, sink.GoalClears(), "the revert keeps the existing goal")

	// The gap never looked idle, so no queued item drained into it. It is also
	// not steerable: the new turn carries the retained input again. Only the two
	// real turns accept a steer.
	assert.NotContains(t, sink.TurnActives(), false)
	kinds := sink.TurnKinds()
	require.GreaterOrEqual(t, len(kinds), 3)
	assert.Contains(t, kinds[:beforeRetry], leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, kinds[len(kinds)-1])
	accepted := false
	for _, kind := range kinds[beforeRetry:] {
		if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE {
			accepted = true
		}
		if accepted {
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, kind, "the accepted turn remains steerable")
		} else {
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, kind, "the retry gap is not steerable")
		}
	}
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, kinds[beforeRetry], "retry acceptance closes steering immediately")
}

// The offer identifies one turn and one model. BeginTurnRetry refuses anything
// else, and a refusal changes nothing.
func TestCodexBeginTurnRetryRefusesARetryThatIsNotOffered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		turnID  string
		model   string
		prepare func(a *Agent)
	}{
		{name: "a different turn", turnID: "turn-2", model: "gpt-5.5"},
		{name: "a different model", turnID: "turn-1", model: "gpt-5.4"},
		{name: "a withdrawn offer", turnID: "turn-1", model: "gpt-5.5", prepare: func(a *Agent) { a.withdrawTurnRetryOffer() }},
		{name: "a retry in flight", turnID: "turn-1", model: "gpt-5.5", prepare: func(a *Agent) {
			a.Mu.Lock()
			a.retry = &codexTurnRetry{a: a}
			a.Mu.Unlock()
		}},
		{name: "a stopped agent", turnID: "turn-1", model: "gpt-5.5", prepare: func(a *Agent) { a.SetStoppedForTest(true) }},
		{name: "no retained input", turnID: "turn-1", model: "gpt-5.5", prepare: func(a *Agent) {
			a.Mu.Lock()
			a.mainTurn.input = nil
			a.Mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			armRetryableTurn(t, a, sink)
			if tc.prepare != nil {
				tc.prepare(a)
			}
			retryingBefore := a.retryingForTest()
			turnStates := len(sink.TurnActives())

			retry, err := a.BeginTurnRetry(tc.turnID, tc.model)

			assert.ErrorIs(t, err, agent.ErrTurnRetryUnavailable)
			assert.Nil(t, retry)
			assert.Empty(t, requests(), "a refusal sends no request")
			assert.Equal(t, "turn-1", a.mainTurn.id, "a refusal leaves the running turn alone")
			assert.Equal(t, retryingBefore, a.retryingForTest(), "a refusal leaves the retry hold alone")
			assert.Len(t, sink.TurnActives(), turnStates, "a refusal publishes no turn state")
		})
	}
}

// A revert error can follow a history change or pending runtime shutdown.
// The retry reports the error and keeps input paused until recovery.
func TestCodexTurnRetryRevertFailureKeepsInputPaused(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, _ := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "thread/revert" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"revert refused"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	require.NoError(t, retry.Replace())
	err = retry.Start()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "revert refused", "the server's refusal is the failure")
	assert.True(t, a.retryingForTest(), "the input queue remains paused")
	assert.Equal(t, "main-thread", a.threadID, "a failed revert must not replace the session identity")
	last, _ := sink.LastTurnActive()
	assert.True(t, last, "an uncertain runtime must not accept input")
	texts := agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink))
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], "Cannot confirm the retry's history change:")
	assert.Contains(t, texts[0], "revert refused")
	assert.Contains(t, texts[0], "Clear context or restart this agent to recover.")
	model, effort := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.4", model)
	assert.Equal(t, "high", effort)
}

// The turn can finish by itself after the acceptance and before Replace stops
// it. Its answer stands: the retry sends nothing and states nothing.
func TestCodexTurnRetryOfATurnThatEndedByItselfSendsNothing(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, nil))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))

	assert.ErrorIs(t, retry.Replace(), errCodexTurnRetryTurnEnded)

	assert.Empty(t, requests(), "no request reaches a turn that already ended")
	assert.False(t, a.retryingForTest())
	assert.Equal(t, "main-thread", a.threadID)
	assert.Empty(t, codexLeapMuxNotices(t, sink))
	last, _ := sink.LastTurnActive()
	assert.False(t, last, "the input queue resumes")
}

// The turn can also finish while the turn/interrupt is in flight. Codex then
// refuses the interrupt, after the turn's turn/completed. The answer stands,
// so the refusal is not a failure to state.
func TestCodexTurnRetryInterruptRefusedForAnEndedTurnStatesNothing(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/interrupt" {
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"no active turn to interrupt"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	assert.ErrorIs(t, retry.Replace(), errCodexTurnRetryTurnEnded)

	assert.Equal(t, []string{"turn/interrupt"}, requestMethods(requests()))
	assert.False(t, a.retryingForTest())
	assert.Empty(t, codexLeapMuxNotices(t, sink))
}

// A retry of an agent that the user stopped fails because the process goes
// away. The user asked for the stop, so the transcript states no failure.
func TestCodexTurnRetryOfAStoppedAgentStatesNoFailure(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, _ := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "thread/revert" {
			a.SetStoppedForTest(true)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"the process stops"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	require.NoError(t, retry.Replace())
	require.Error(t, retry.Start())

	assert.False(t, a.retryingForTest())
	assert.Empty(t, codexLeapMuxNotices(t, sink))
}

// The stage of a failure reads as a word in the log.
func TestCodexTurnRetryStageString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "stopping", codexTurnRetryStopping.String())
	assert.Equal(t, "preparing", codexTurnRetryPreparing.String())
	assert.Equal(t, "reverting", codexTurnRetryReverting.String())
	assert.Equal(t, "starting", codexTurnRetryStarting.String())
	assert.Equal(t, "codexTurnRetryStage(9)", codexTurnRetryStage(9).String())
}

// A turn/interrupt that Codex refuses stops nothing. The attempt continues,
// so the notice must not tell the user to send the message again.
func TestCodexTurnRetryInterruptFailureLetsTheAttemptContinue(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/interrupt" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"interrupt refused"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	require.Error(t, retry.Replace())

	assert.Equal(t, []string{"turn/interrupt"}, requestMethods(requests()))
	assert.False(t, a.retryingForTest())
	assert.Equal(t, "turn-1", a.mainTurn.id, "the attempt still runs")
	last, _ := sink.LastTurnActive()
	assert.True(t, last, "the turn state reports the attempt that still runs")
	texts := agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink))
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], "Failed to retry with a faster model: ")
	assert.Contains(t, texts[0], "interrupt refused")
	assert.Contains(t, texts[0], ". The current attempt continues.")
	assert.NotContains(t, texts[0], "Send the message again")
}

// A stop while the retry waits for the stopped turn wins: the retry sends
// nothing more, and a stop is not an error.
func TestCodexTurnRetryStopDuringTheWaitCancelsTheRetry(t *testing.T) {
	t.Parallel()

	interruptErr := make(chan error, 1)
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/interrupt" {
			handleCodexOutput(a, providerkit.ParseLine([]byte(retryTurnCompleted)))
			// The responder runs before the interrupt reply returns, so the stop
			// lands before the retry resumes.
			interruptErr <- a.Interrupt(agent.StopContext{})
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	assert.ErrorIs(t, retry.Replace(), errCodexTurnRetryCancelled)
	require.NoError(t, <-interruptErr)

	assert.Equal(t, []string{"turn/interrupt"}, requestMethods(requests()), "the cancelled retry stops the turn and nothing else")
	assert.Empty(t, agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink)), "the user's stop is not an error")
	assert.False(t, a.retryingForTest())
	assert.Equal(t, "main-thread", a.threadID)
	model, _ := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.4", model, "a cancelled retry keeps the model")
}

// A stop during revert prevents resubmission without retiring the retained thread.
func TestCodexTurnRetryStopDuringRevertKeepsTheThread(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "thread/revert" {
			require.NoError(t, a.Interrupt(agent.StopContext{}))
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	require.ErrorIs(t, retry.Start(), errCodexTurnRetryCancelled)
	assert.Equal(t, "main-thread", a.currentThreadID())
	assert.False(t, a.isRetiredCodexThread("main-thread"))
	assert.Equal(t, []string{"turn/interrupt", "thread/revert"}, requestMethods(requests()))
	assert.False(t, a.retryingForTest())
	assert.Empty(t, codexLeapMuxNotices(t, sink))
}

// A stop after revert submission and before the start wins the same way: Start sends no
// turn, states no retry, and keeps the model.
func TestCodexTurnRetryStopBeforeTheStartSkipsTheTurn(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, nil))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	assert.ErrorIs(t, retry.Start(), errCodexTurnRetryCancelled)

	assert.Equal(t, []string{"turn/interrupt", "thread/revert"}, requestMethods(requests()),
		"no turn starts, and the retired thread is still released")
	assert.Empty(t, codexLeapMuxNotices(t, sink), "no retry ran, and the user's stop is not an error")
	assert.False(t, a.retryingForTest())
	model, effort := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.4", model)
	assert.Equal(t, "high", effort)
	value, offered := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
	require.True(t, offered)
	assert.Nil(t, value, "the banner shows no offer")
}

// A stop that arrives after Start checked for one, but before the retry's
// turn/started, finds no turn to stop. The retry stops its new turn as soon as
// the turn starts, so the user's stop is never lost.
func TestCodexTurnRetryStopDuringTheStartStopsTheNewTurn(t *testing.T) {
	t.Parallel()

	stopErr := make(chan error, 1)
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/start" {
			// The stop finds no turn yet.
			stopErr <- a.Interrupt(agent.StopContext{})
			handleCodexOutput(a, providerkit.ParseLine([]byte(retryTurnStarted)))
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())

	require.NoError(t, retry.Start())
	require.NoError(t, <-stopErr)

	sent := requests()
	require.Equal(t, []string{"turn/interrupt", "thread/revert", "turn/start", "turn/interrupt"}, requestMethods(sent))
	assert.Equal(t, "main-thread", sent[3].Params["threadId"])
	assert.Equal(t, "turn-2", sent[3].Params["turnId"], "the new turn stops")
	assert.Empty(t, agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink)))
}

// Only the retry's own turn/started ends the retry hold. A turn that Codex
// starts by itself before the revert reply, leaves the queue held.
func TestCodexTurnRetryHoldSurvivesATurnThatCodexStarts(t *testing.T) {
	t.Parallel()

	retryingInGap := make(chan bool, 1)
	var a *Agent
	a, sink, _ := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		switch method {
		case "turn/interrupt":
			handleCodexOutput(a, providerkit.ParseLine([]byte(retryTurnCompleted)))
			// A goal continuation that Codex starts on the old thread.
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-x"}}}`)))
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		case "thread/revert":
			retryingInGap <- a.retryingForTest()
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	require.NoError(t, retry.Replace())
	assert.True(t, <-retryingInGap, "a turn that Codex starts keeps the hold")
	assert.True(t, a.retryingForTest(), "the pending revert keeps the hold")

	require.NoError(t, retry.Start())
	assert.False(t, a.retryingForTest(), "the retry's own turn/started ends the hold")
	assert.Equal(t, "turn-2", a.mainTurn.id)
}

// A turn/start that Codex refuses after the revert leaves no running turn. The
// transcript states the retry and then its failure, the input queue resumes,
// and the model stays, because no turn ran on the faster one.
func TestCodexTurnRetryStartFailureStatesTheFailure(t *testing.T) {
	t.Parallel()

	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/start" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"start refused"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())

	startErr := retry.Start()

	require.Error(t, startErr)
	assert.Contains(t, startErr.Error(), "start refused", "the server's refusal is the failure")
	notifications := codexLeapMuxNotices(t, sink)
	require.Len(t, notifications, 2)
	assert.Equal(t, contracts.NotificationTypeTurnRetried, notifications[0][contracts.NotificationFieldType])
	assert.Equal(t, contracts.NotificationTypeAgentError, notifications[1][contracts.NotificationFieldType])
	assert.Contains(t, notifications[1][contracts.NotificationFieldError], "Failed to retry with a faster model: ")
	assert.Contains(t, notifications[1][contracts.NotificationFieldError], ". Send the message again to retry.")
	assert.False(t, a.retryingForTest(), "the input queue resumes")
	last, _ := sink.LastTurnActive()
	assert.False(t, last, "no turn runs after the failed start")
	model, effort := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.4", model, "the model changes only when the retry's turn starts")
	assert.Equal(t, "high", effort, "the effort changes only when the retry's turn starts")
	sent := requests()
	assert.Equal(t, []string{"turn/interrupt", "thread/revert", "turn/start"}, requestMethods(sent), "a failed input does not replace the subscription")
}

// A turn/start without its turn/started in time can still start the turn. The
// notice says so, and the retained input stays for the late turn.
func TestCodexTurnRetryUnconfirmedStartSaysTheRetryCanStillStart(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	ackTimer := clock.Trap().NewTimer(codexTurnStartAckTimerTag)
	t.Cleanup(ackTimer.Close)
	var a *Agent
	a, sink, _ := newCodexAgentForRPCWithClock(t, clock, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/start" {
			// Codex accepts the request, but its turn/started is late.
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())

	started := make(chan error, 1)
	go func() { started <- retry.Start() }()
	assert.Equal(t, turnStartAckTimeout, testutil.WaitForTimer(t, ctx, ackTimer))
	clock.Advance(turnStartAckTimeout).MustWait(ctx)

	assert.ErrorIs(t, <-started, agent.ErrDeliveryUncertain)
	texts := agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink))
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], "Failed to retry with a faster model: ")
	assert.Contains(t, texts[0], ". The retry can still start. Wait before you send the message again.")
	assert.NotContains(t, texts[0], "Send the message again to retry.")
	assert.True(t, a.retryingForTest(), "an unresolved delivery keeps the queue hold")
	model, _ := a.modelAndEffortForTest()
	assert.Equal(t, "gpt-5.4", model, "an unconfirmed turn changes no setting")

	// The late turn is the retry's turn, so it keeps the retained input.
	handleCodexOutput(a, providerkit.ParseLine([]byte(retryTurnStarted)))
	a.Mu.Lock()
	retained := a.mainTurn.input
	a.Mu.Unlock()
	assert.Equal(t, []map[string]interface{}{{"type": "text", "text": "Hello."}}, retained)
}

// The wait for the stopped turn has a limit. A turn/completed that never
// arrives ends the retry with a notice, not a hold that lasts for ever.
func TestCodexTurnRetryWaitForTheStoppedTurnTimesOut(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	endTimer := clock.Trap().NewTimer(codexTurnRetryEndTimerTag)
	t.Cleanup(endTimer.Close)
	var a *Agent
	a, sink, requests := newCodexAgentForRPCWithClock(t, clock, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/interrupt" {
			// Codex accepts the interrupt, but its turn/completed never arrives.
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	replaced := make(chan error, 1)
	go func() { replaced <- retry.Replace() }()
	assert.Equal(t, a.APITimeout(), testutil.WaitForTimer(t, ctx, endTimer))
	clock.Advance(a.APITimeout()).MustWait(ctx)

	replaceErr := <-replaced
	require.Error(t, replaceErr)
	assert.Contains(t, replaceErr.Error(), "the stopped turn did not end within")
	assert.Equal(t, []string{"turn/interrupt"}, requestMethods(requests()), "no revert follows a turn that did not end")
	assert.False(t, a.retryingForTest())
	texts := agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink))
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], "the stopped turn did not end within")
	assert.Contains(t, texts[0], ". Send the message again to retry.")
}

// A process that exits while the retry waits for the stopped turn ends the
// retry at once, with the exit as the stated failure.
func TestCodexTurnRetryProcessExitDuringTheWaitEndsTheRetry(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	endTimer := clock.Trap().NewTimer(codexTurnRetryEndTimerTag)
	t.Cleanup(endTimer.Close)
	var a *Agent
	a, sink, requests := newCodexAgentForRPCWithClock(t, clock, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "turn/interrupt" {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	replaced := make(chan error, 1)
	go func() { replaced <- retry.Replace() }()
	// The process exits while the retry waits, after the interrupt reply.
	call := endTimer.MustWait(ctx)
	a.SimulateExitForTest()
	call.MustRelease(ctx)

	replaceErr := <-replaced
	require.Error(t, replaceErr)
	assert.Equal(t, a.ProcessExitError().Error(), replaceErr.Error(), "the exit is the failure, not a wrapper around it")
	assert.Equal(t, []string{"turn/interrupt"}, requestMethods(requests()))
	assert.False(t, a.retryingForTest())
	texts := agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink))
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], a.ProcessExitError().Error())
}

// Abandon ends a retry whose provider instance changed before its phases ran.
// It sends nothing, and a lost retry is not an error to state.
func TestCodexTurnRetryAbandonEndsTheHoldWithoutANotice(t *testing.T) {
	t.Parallel()

	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)

	retry.Abandon(assert.AnError)

	assert.Empty(t, requests())
	assert.False(t, a.retryingForTest())
	assert.Empty(t, codexLeapMuxNotices(t, sink))
	last, _ := sink.LastTurnActive()
	assert.True(t, last, "the untouched turn still runs")
}

// Retry keeps the existing subscription, even when unsubscribe would fail.
func TestCodexTurnRetryKeepsItsSubscription(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "thread/unsubscribe" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unsubscribe refused"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	sink.UpdateSessionID("main-thread")
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, retry.Replace())
	require.NoError(t, retry.Start())
	assert.Equal(t, "main-thread", sink.LastSessionID())
	assert.Equal(t, []string{"turn/interrupt", "thread/revert", "turn/start"}, requestMethods(requests()))
	assert.Empty(t, agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink)))
	before := sink.NotificationCount()
	feedCodexLines(a, `{"method":"warning","params":{"threadId":"main-thread","message":"still subscribed"}}`)
	assert.Equal(t, before+1, sink.NotificationCount())
}

// orderRecordingSink records the order of the session calls that a thread swap
// makes.
type orderRecordingSink struct {
	*agenttest.Sink
	mu    sync.Mutex
	calls []string
}

func (s *orderRecordingSink) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *orderRecordingSink) SetTurnState(state agent.TurnState, seq uint64) {
	s.record("turn_state")
	s.Sink.SetTurnState(state, seq)
}

func (s *orderRecordingSink) ReportProgress(update agent.ProgressUpdate) {
	s.record("progress")
	s.Sink.ReportProgress(update)
}

func (s *orderRecordingSink) ClearGoal(snapshot bool) {
	s.record("clear_goal")
	s.Sink.ClearGoal(snapshot)
}

func (s *orderRecordingSink) UpdateSessionID(sessionID string) {
	s.record("session_id")
	s.Sink.UpdateSessionID(sessionID)
}

// Reset forgets the calls so far, so a test records one operation alone.
func (s *orderRecordingSink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

func (s *orderRecordingSink) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// A clear installs the session identity before it publishes the replacement state.
// Retired-thread cleanup runs independently of the completed clear.
func TestCodexClearContextPublishesTheSwapInOrder(t *testing.T) {
	t.Parallel()

	unsubscribed := make(chan struct{})
	a, _, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		if method == "thread/unsubscribe" {
			close(unsubscribed)
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-1"},"model":"gpt-5.4"}`)}
	})
	recorder := &orderRecordingSink{Sink: &agenttest.Sink{}}
	a.sink = agent.NewProviderServices(recorder)
	a.threadID = "old-1"

	sessionID, err := a.ClearContext()

	require.NoError(t, err)
	assert.Equal(t, "fresh-1", sessionID)
	assert.Equal(t, []string{"session_id", "turn_state", "progress", "clear_goal"}, recorder.Calls())
	assert.Equal(t, 1, recorder.GoalClears(), "a fresh thread has no goal")
	<-unsubscribed
	sent := requests()
	require.Equal(t, []string{"thread/start", "thread/unsubscribe"}, requestMethods(sent))
	assert.Equal(t, "old-1", sent[1].Params["threadId"])
}

// A same-ID revert preserves the goal and transcript identity.
func TestCodexTurnRetryKeepsTheGoalAndSession(t *testing.T) {
	t.Parallel()
	recorder := &orderRecordingSink{Sink: &agenttest.Sink{}}
	var a *Agent
	a, _, _ = newCodexAgentForRPC(t, retryResponder(&a, nil))
	a.sink = agent.NewProviderServices(recorder)
	armRetryableTurn(t, a, recorder.Sink)
	recorder.UpdateSessionID("main-thread")
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	recorder.Reset()
	require.NoError(t, retry.Replace())
	require.NoError(t, retry.Start())
	assert.NotContains(t, recorder.Calls(), "session_id")
	assert.NotContains(t, recorder.Calls(), "clear_goal")
	assert.Equal(t, "main-thread", recorder.LastSessionID())
	assert.Zero(t, recorder.GoalClears())
}

// A clear replaces the thread that a running retry targets. The retry stops
// before it sends a request. A late retry must not restore the cleared conversation.
func TestCodexClearContextCancelsARunningRetry(t *testing.T) {
	t.Parallel()

	unsubscribed := make(chan struct{})
	var a *Agent
	a, sink, requests := newCodexAgentForRPC(t, retryResponder(&a, func(method string) (agenttest.RPCReply, bool) {
		if method == "thread/unsubscribe" {
			close(unsubscribed)
		}
		if method == "thread/start" {
			return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-1"},"model":"gpt-5.4"}`)}, true
		}
		return agenttest.RPCReply{}, false
	}))
	armRetryableTurn(t, a, sink)
	retry, err := a.BeginTurnRetry("turn-1", "gpt-5.5")
	require.NoError(t, err)
	_, err = a.ClearContext()
	require.NoError(t, err)

	assert.ErrorIs(t, retry.Replace(), errCodexTurnRetryCancelled)

	<-unsubscribed
	assert.Equal(t, []string{"thread/start", "thread/unsubscribe"}, requestMethods(requests()), "the retry sends nothing")
	assert.Equal(t, "fresh-1", a.threadID)
	assert.False(t, a.retryingForTest())
	assert.Empty(t, agenttest.AgentErrorTexts(codexLeapMuxNotices(t, sink)))
}

// A clear replaces the thread, so the offer for the replaced turn is
// withdrawn before the reader can act on it.
func TestCodexClearContextWithdrawsTheTurnRetryOffer(t *testing.T) {
	t.Parallel()

	a, sink, _ := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		if method == "thread/unsubscribe" {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-1"},"model":"gpt-5.4"}`)}
	})
	armRetryableTurn(t, a, sink)

	_, err := a.ClearContext()

	require.NoError(t, err)
	values := sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer)
	require.Len(t, values, 2)
	assert.NotNil(t, values[0], "the notice offered the retry")
	assert.Nil(t, values[1], "the clear withdraws the offer")
	a.Mu.Lock()
	offer := a.retryOffer
	a.Mu.Unlock()
	assert.Nil(t, offer)
}

// A steer adds to the retained input of a turn that LeapMux started. A turn
// that Codex starts by itself retains nothing, and a steer alone must not make
// it retryable: the retry would send the steer without the request it steers.
func TestCodexSteerOfATurnWithoutRetainedInputRetainsNothing(t *testing.T) {
	t.Parallel()

	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.threadID = "main-thread"
	a.turnRetrySupported = true
	// A goal continuation: Codex starts the turn, so LeapMux holds no input.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)))

	require.NoError(t, a.SteerInput("guide", nil))

	a.Mu.Lock()
	retained := a.mainTurn.input
	a.Mu.Unlock()
	assert.Empty(t, retained, "the steer alone is not the input of the turn")
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.5"}}`)))
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer), "the turn offers no retry")
}

// A clear drops the input that waits for a turn/started. A later turn that
// Codex starts by itself must not take it as its own, and a slice of nil
// blocks must not count as retained input either.
func TestCodexClearContextDropsThePendingTurnInput(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		if method == "thread/unsubscribe" {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"thread":{"id":"fresh-1"},"model":"gpt-5.4"}`)}
	})
	a.threadID = "old-1"
	a.pendingStart = newCodexTurnStart("old-1", []map[string]interface{}{{"type": "text", "text": "stale"}}, turnSettings{})

	_, err := a.ClearContext()
	require.NoError(t, err)
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"fresh-1","turn":{"id":"turn-9"}}}`)))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Nil(t, a.pendingStart)
	assert.Nil(t, a.mainTurn.input, "the turn that Codex started retains nothing")
}

// A turn/start that fails starts no turn, so its input does not stay pending
// for the next turn that Codex starts by itself. Both a refused request and a
// process exit drop it.
func TestCodexFailedTurnStartDropsItsPendingInput(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		reply   agenttest.RPCReply
		prepare func(a *Agent)
	}{
		{name: "a refused request", reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"thread is active"}`)}},
		{name: "a process exit", reply: agenttest.RPCReply{Result: json.RawMessage(`{}`)}, prepare: func(a *Agent) { a.SimulateExitForTest() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return tc.reply })
			a.threadID = "main-thread"
			a.turnRetrySupported = true
			if tc.prepare != nil {
				tc.prepare(a)
			}
			require.Error(t, a.SendInput("Hello.", nil))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-9"}}}`)))

			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Nil(t, a.pendingStart)
			assert.Nil(t, a.mainTurn.input, "the turn that Codex started retains nothing")
		})
	}
}

// A turn/start without its turn/started in time can still start its turn, so
// its input stays pending, and the late turn retains it.
func TestCodexUnconfirmedTurnStartKeepsItsPendingInput(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	ackTimer := clock.Trap().NewTimer(codexTurnStartAckTimerTag)
	t.Cleanup(ackTimer.Close)
	a, _, _ := newCodexAgentForRPCWithClock(t, clock, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	a.turnRetrySupported = true

	sent := make(chan error, 1)
	go func() { sent <- a.SendInput("Hello.", nil) }()
	assert.Equal(t, turnStartAckTimeout, testutil.WaitForTimer(t, ctx, ackTimer))
	clock.Advance(turnStartAckTimeout).MustWait(ctx)
	require.ErrorIs(t, <-sent, agent.ErrDeliveryUncertain)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, []map[string]interface{}{{"type": "text", "text": "Hello."}}, a.mainTurn.input)
}

// codexLeapMuxNotices reads worker notices from both sink entry points.
func codexLeapMuxNotices(t *testing.T, sink *agenttest.Sink) []map[string]interface{} {
	t.Helper()
	notices := sink.LeapMuxNotifications()
	for _, message := range sink.PersistedNotifications() {
		if message.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
			continue
		}
		var notice map[string]interface{}
		require.NoError(t, json.Unmarshal(message.Content, &notice))
		notices = append(notices, notice)
	}
	return notices
}
