package mimo

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListenPattern(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]string{
		"mimocode server listening on http://127.0.0.1:4096":                  "http://127.0.0.1:4096",
		"mimocode server listening on http://127.0.0.1:53311 (pid 42)":        "http://127.0.0.1:53311",
		"\x1b[0mmimocode server listening on http://127.0.0.1:4097\x1b[0m":    "http://127.0.0.1:4097",
		"INFO  service=server mimocode server listening on http://[::1]:4096": "http://[::1]:4096",
	} {
		match := mimoListenPattern.FindStringSubmatch(line)
		require.Len(t, match, 2, "line %q", line)
		assert.Equal(t, want, match[1], "line %q", line)
	}
	for _, line := range []string{"", "Warning: no model configured", "opencode server listening on http://127.0.0.1:4096"} {
		assert.Nil(t, mimoListenPattern.FindStringSubmatch(line), "line %q", line)
	}
}

func TestIsConnectedEvent(t *testing.T) {
	t.Parallel()
	assert.True(t, isConnectedEvent([]byte(`{"type":"server.connected","properties":{}}`)))
	assert.False(t, isConnectedEvent([]byte(`{"type":"server.heartbeat","properties":{}}`)))
	assert.False(t, isConnectedEvent([]byte(`{"type":"tui.toast.show","properties":{"message":"server.connected"}}`)),
		"a payload that only mentions the type is another event")
	assert.False(t, isConnectedEvent([]byte(`not json server.connected`)))
}

// The worker sets the credential over whatever it inherited: an inherited
// password would lock the worker out of its own server.
func TestLaunchEnvPinsTheCredential(t *testing.T) {
	t.Parallel()
	env := mimoLaunchEnv([]string{
		"PATH=/usr/bin",
		"MIMOCODE_SERVER_PASSWORD=inherited",
		"mimocode_server_username=someone",
		"MIMOCODE_ENABLE_QUESTION_TOOL=0",
		"MIMOCODE_HOME=/opt/mimo",
		"MIMOCODE=1",
		"MIMOCODE_PID=77",
	}, "fresh-secret", agent.Options{})

	assert.Equal(t, []string{"fresh-secret"}, envutil.ValuesFor(env, envServerPassword))
	assert.Equal(t, []string{serverUser}, envutil.ValuesFor(env, envServerUsername))
	assert.Equal(t, []string{"1"}, envutil.ValuesFor(env, envQuestionTool))
	assert.Equal(t, []string{"/opt/mimo"}, envutil.ValuesFor(env, "MIMOCODE_HOME"), "the user's MiMo home stays")
	assert.False(t, envutil.HasKey(env, "MIMOCODE"), "a nesting marker of an outer MiMo is scrubbed")
	assert.False(t, envutil.HasKey(env, "MIMOCODE_PID"))
	assert.Equal(t, []string{"1"}, envutil.ValuesFor(env, "LEAPMUX_WORKER"))
}

// startTestStream opens the agent's event stream against its fake server, as
// Start does, and ends it with the test.
func startTestStream(t *testing.T, a *Agent, server *fakeServer) {
	t.Helper()
	require.NoError(t, a.openEventStream(a.Context(), testTimeout))
	awaitStreamOpened(t, server)
	t.Cleanup(func() {
		a.streamCancel()
		select {
		case <-a.streamDone:
		case <-time.After(testTimeout):
			t.Error("the event stream did not end")
		}
	})
}

func TestEventStreamDispatchesEvents(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	startTestStream(t, a, server)

	server.emit(string(statusEvent(t, contracts.MiMoStatusTypeBusy)))
	waitFor(t, func() bool { return len(sink.TurnActives()) == 1 }, "the stream dispatches the event")
	assert.Equal(t, []bool{true}, sink.TurnActives())
	requests := server.requestsTo("GET /event")
	require.Len(t, requests, 1)
	assert.Equal(t, "text/event-stream", requests[0].Header.Get("Accept"))
	assert.Equal(t, a.workingDir, requests[0].Header.Get(directoryHeader))
}

// A stream that drops can lose the idle that ends a turn, and a request raised
// while it was down. The reconnect asks the server for both.
func TestEventStreamReconnectRestatesTheState(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, sink, server := newControlTestAgent(t)
	clock := useMockClock(t, a)
	reconnect := clock.Trap().NewTimer(mimoStreamReconnectTimerTag)
	defer reconnect.Close()
	startTestStream(t, a, server)
	server.emit(string(statusEvent(t, contracts.MiMoStatusTypeBusy)))
	waitFor(t, func() bool { return len(sink.TurnActives()) == 1 }, "the turn starts")

	var pending mimoEvent
	require.NoError(t, json.Unmarshal(permissionAskedEvent(t, "per_gap", testSessionID, ""), &pending))
	server.respond("GET /permission", http.StatusOK, "["+string(pending.Properties)+"]")
	server.endStreams()
	clock.Advance(testutil.WaitForTimer(t, ctx, reconnect)).MustWait(ctx)
	awaitStreamOpened(t, server)

	waitFor(t, func() bool { return len(sink.TurnActives()) == 2 }, "the reconnect ends the turn the server no longer runs")
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, turnLifecycleWithoutResets(sink.TurnLifecycle()))
	waitFor(t, func() bool { return sink.PublishedControlCount() == 1 }, "the reconnect publishes a request raised in the gap")
	assert.Equal(t, "mimo-permission:per_gap", sink.LastPublishedControl().RequestID)
}

// The first connection only releases the start path. No gap precedes it, so it
// restates nothing: the session and its requests do not exist yet.
func TestFirstConnectionRestatesNothing(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	startTestStream(t, a, server)

	// The stream goroutine handles server.connected before any later event, so
	// once the busy is dispatched, the connection handler has returned.
	server.emit(string(statusEvent(t, contracts.MiMoStatusTypeBusy)))
	waitFor(t, func() bool { return len(sink.TurnActives()) == 1 }, "the stream dispatches the event")
	for _, route := range []string{"GET /session/status", "GET /permission", "GET /question", "GET /bash-interactive"} {
		assert.Empty(t, server.requestsTo(route), "route %s", route)
	}
}

// An agent that holds no session has no turn that the server could run.
func TestReconcileTurnWithoutASessionReadsNothing(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	a.sessionID = ""

	a.reconcileTurn()
	assert.Empty(t, server.allRequests())
	assert.Empty(t, sink.TurnActives())
}

func TestReconcileTurnStartsATurnTheServerRuns(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/status", http.StatusOK, `{"ses_test":{"type":"busy"},"ses_other":{"type":"busy"}}`)

	a.reconcileTurn()
	assert.Equal(t, []bool{true}, sink.TurnActives())
	a.reconcileTurn()
	assert.Equal(t, []bool{true}, sink.TurnActives(), "a turn that runs is not started twice")

	server.respond("GET /session/status", http.StatusOK, `{"ses_test":{"type":"idle"}}`)
	a.reconcileTurn()
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "an idle entry ends the turn as an absent one does")
}

// A status that cannot be read states nothing, so the turn stays as it is.
func TestReconcileTurnKeepsTheTurnWhenTheStatusIsUnreadable(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	server.respond("GET /session/status", http.StatusInternalServerError, `{}`)

	a.reconcileTurn()
	assert.Equal(t, []bool{true}, sink.TurnActives())
}

func TestOpenEventStreamFailsWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("GET /event", http.StatusServiceUnavailable, `{}`)
	a.SimulateExitForTest()

	err := a.openEventStream(a.Context(), testTimeout)
	assert.Error(t, err)
	select {
	case <-a.streamDone:
	case <-time.After(testTimeout):
		t.Fatal("the stream loop ends with the process")
	}
}

func TestOpenEventStreamFailsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("GET /event", http.StatusServiceUnavailable, `{}`)
	ctx, cancel := context.WithCancel(a.Context())
	cancel()

	assert.ErrorIs(t, a.openEventStream(ctx, testTimeout), context.Canceled)
}

// awaitStreamOpened waits until the fake server opens one more event stream.
func awaitStreamOpened(t *testing.T, server *fakeServer) {
	t.Helper()
	select {
	case <-server.streamOpened:
	case <-time.After(testTimeout):
		t.Fatal("the agent opened no event stream")
	}
}

// Each connection that fails doubles the wait before the next one, up to the
// cap. A connection that reached server.connected starts the wait over. The
// test reads the delay that the loop asks the clock for, so the host's timer
// granularity cannot blur two steps.
func TestEventStreamReconnectBackoffDoublesUpToItsCap(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	reconnect := clock.Trap().NewTimer(mimoStreamReconnectTimerTag)
	defer reconnect.Close()
	startTestStream(t, a, server)

	server.respond("GET "+routeEvents, http.StatusServiceUnavailable, `{}`)
	server.endStreams()
	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond,
		1600 * time.Millisecond, 3200 * time.Millisecond, mimoStreamRetryMax, mimoStreamRetryMax,
	}
	for i, delay := range want {
		require.Equal(t, delay, testutil.WaitForTimer(t, ctx, reconnect), "the wait before connection %d", i+1)
		if i == len(want)-1 {
			// The server serves the stream again, so the last attempt connects.
			server.handle("GET "+routeEvents, nil)
		}
		clock.Advance(delay).MustWait(ctx)
	}
	awaitStreamOpened(t, server)
	assert.Len(t, server.requestsTo("GET "+routeEvents), 1+len(want),
		"the first stream, the refused attempts, and the attempt that connected")

	server.endStreams()
	assert.Equal(t, mimoStreamRetryFirst, testutil.WaitForTimer(t, ctx, reconnect),
		"a connection that reached server.connected starts the wait over")
}

// A process that exits while the stream waits to connect again ends the loop,
// and the wait's timer with it.
func TestEventStreamStopsWaitingWhenTheProcessExits(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	reconnect := clock.Trap().NewTimer(mimoStreamReconnectTimerTag)
	defer reconnect.Close()
	startTestStream(t, a, server)

	server.endStreams()
	assert.Equal(t, mimoStreamRetryFirst, testutil.WaitForTimer(t, ctx, reconnect))
	a.SimulateExitForTest()
	select {
	case <-a.streamDone:
	case <-ctx.Done():
		t.Fatal("the stream loop ends with the process")
	}
	_, pending := clock.Peek()
	assert.False(t, pending, "the loop stops the timer of the wait that it left")
	assert.Len(t, server.requestsTo("GET "+routeEvents), 1, "the loop connects no more")
}

func TestSyntheticIdleEventReadsAsTheRealOne(t *testing.T) {
	t.Parallel()
	assert.JSONEq(t, string(statusEvent(t, contracts.MiMoStatusTypeIdle)), string(syntheticIdleEvent(testSessionID)))
}
