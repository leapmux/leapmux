package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	agenttest.AssertRejectsMissingAndReplacedSessions(t, &Agent{threadID: "current"})
}

// TestCodexRecoveryRefusesNewInputAdmission covers the admission rule: once a
// failure marked the process, SendInput and SendInputForSession write no
// native request, whatever their session states.
func TestCodexRecoveryRefusesNewInputAdmission(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	a.markAuthenticationRecoveryRequired()

	err := a.SendInput("hello", nil)
	require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	err = a.SendInputForSession("main-thread", "hello", nil)
	require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	assert.Empty(t, requests(), "no request reached the stale process")
}

// TestCodexTurnStartAuthenticationRejectionMarksAndRefusesLaterInput covers the
// explicit turn/start rejection: it settles the request, marks the process, and
// a second input sends no second turn/start.
func TestCodexTurnStartAuthenticationRejectionMarksAndRefusesLaterInput(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Method == "turn/start" {
			return agenttest.RPCReply{Error: json.RawMessage(
				`{"code":-32000,"message":"Your access token could not be refreshed because you have since logged out or signed in to another account. Please sign in again."}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"

	err := a.SendInput("first", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "an explicit rejection is a known failure")
	require.Len(t, requests(), 1)
	assert.True(t, recoveryRequired(a), "the rejection marks the process before the request settles")

	err = a.SendInput("second", nil)
	require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	assert.Len(t, requests(), 1, "the stale process receives no second turn/start")
}

// TestCodexLateTurnStartRejectionDoesNotMarkTheNextSession covers a rejection
// that arrives after a context clear retired its request: the late reply must
// not mark the process that no longer waits for it.
func TestCodexLateTurnStartRejectionDoesNotMarkTheNextSession(t *testing.T) {
	t.Parallel()
	held := make(chan func(agenttest.RPCReply), 1)
	a, _, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
		switch request.Method {
		case "turn/start":
			select {
			case held <- reply:
			default:
			}
		case "thread/start":
			reply(agenttest.RPCReply{Result: json.RawMessage(
				`{"thread":{"id":"new-thread","historyMode":"paginated","ephemeral":false},"model":"gpt-5.4"}`)})
		default:
			reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
		}
	})
	a.threadID = "main-thread"

	first := make(chan error, 1)
	go func() { first <- a.SendInput("held", nil) }()
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)

	// A context clear retires the pending turn/start and replaces the thread.
	_, err := a.ClearContext()
	require.NoError(t, err)
	select {
	case err := <-first:
		require.Error(t, err, "the clear settles the retired request's waiter")
	case <-time.After(time.Second):
		t.Fatal("the retired turn/start did not settle its waiter")
	}

	// The retired request's authentication rejection arrives now.
	select {
	case deliver := <-held:
		deliver(agenttest.RPCReply{Error: json.RawMessage(
			`{"code":-32000,"message":"workspace routing discovery unauthorized (401)"}`)})
	case <-time.After(time.Second):
		t.Fatal("the fixture did not capture the held reply")
	}
	assert.False(t, recoveryRequired(a),
		"a late reply for a retired request must not mark the current thread's process")
}

// TestCodexMarkedProcessRefusesCompactionAndClear covers the remaining
// admission sites on one marked process: a native compaction writes nothing,
// and a context clear takes the restart fallback instead of the stale process.
func TestCodexMarkedProcessRefusesCompactionAndClear(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	a.markAuthenticationRecoveryRequired()

	err := a.CompactContext()
	require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	_, err = a.ClearContext()
	require.ErrorIs(t, err, agent.ErrContextClearUnsupported,
		"a context clear on a stale process routes through the restart fallback")
	assert.Empty(t, requests(), "the stale process received no native request")
}

// TestCodexCompactionAuthenticationRejectionMarksRecovery covers an explicit
// compaction rejection that states a credential failure.
func TestCodexCompactionAuthenticationRejectionMarksRecovery(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Method == "thread/compact/start" {
			return agenttest.RPCReply{Error: json.RawMessage(
				`{"code":-32000,"message":"Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again."}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	err := a.CompactContext()
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "an explicit rejection is known")
	assert.True(t, recoveryRequired(a), "the compaction rejection marks the process")
	require.Len(t, requests(), 1)
}

// TestCodexCompactionTimeoutStaysUncertainAndUnmarked drives the compaction
// ack timer on a mock clock: a lost compaction answer stays delivery-uncertain
// and marks no recovery.
func TestCodexCompactionTimeoutStaysUncertainAndUnmarked(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clock := testutil.NewQuartzMock(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, codexCompactionAckTimerTag)
	a, _, _ := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
		if request.Method == "thread/compact/start" {
			return // The answer never arrives.
		}
		reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	}, clock)
	a.threadID = "main-thread"

	uncertain := make(chan error, 1)
	go func() { uncertain <- a.CompactContext() }()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)

	select {
	case err := <-uncertain:
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	case <-ctx.Done():
		t.Fatal("the unanswered compaction did not resolve at its deadline")
	}
	assert.False(t, recoveryRequired(a), "an uncertain compaction marks nothing")
}

// TestCodexRecoveryRefusesNewPromptSteeringButDeliversValidatedAnswers covers
// steering on a process that a child authentication failure marked while its
// main turn still runs: a new prompt is refused and retained by the caller; a
// session-validated question answer still reaches the active turn so its work
// can finish.
func TestCodexRecoveryRefusesNewPromptSteeringButDeliversValidatedAnswers(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-thread","agentPath":"/root/child"}}}`,
		`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"failed","error":{"message":"signed in to another account","codexErrorInfo":"unauthorized"}}}}`,
	)
	require.True(t, recoveryRequired(a))

	err := a.SteerInput("a new prompt", nil)
	require.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	assert.Empty(t, requests(), "no turn/steer reached the stale process for a new prompt")

	err = a.SteerInputForSession("main-thread", "the answer", nil)
	require.NoError(t, err, "a validated answer still steers into the active turn")
	require.Len(t, requests(), 1)
	assert.Equal(t, "turn/steer", requests()[0].Method)
}
